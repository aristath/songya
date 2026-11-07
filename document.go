// Package songya stores connected Markdown records.
package songya

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

type Relation struct {
	Type   string `yaml:"type" json:"type"`
	Target string `yaml:"target" json:"target"`
}

type Metadata struct {
	User      string     `yaml:"user,omitempty"`
	Title     string     `yaml:"title"`
	CreatedAt time.Time  `yaml:"created_at"`
	UpdatedAt time.Time  `yaml:"updated_at"`
	Relations []Relation `yaml:"relations,omitempty"`
}

// Document keeps the complete YAML tree and Markdown body, including damaged
// fields. Diagnostics reports problems without making the record unavailable.
type Document struct {
	Kind        Destination `yaml:"-" json:"-"`
	source      []byte
	ID          string `yaml:"-"`
	Revision    string `yaml:"-"`
	Pending     bool   `yaml:"-"`
	Unconfirmed bool   `yaml:"-"`
	Frontmatter yaml.Node
	Body        string
	warnings    []string
}

func NewDocument(metadata Metadata, body string) (*Document, error) {
	metadata.CreatedAt = metadata.CreatedAt.UTC()
	metadata.UpdatedAt = metadata.UpdatedAt.UTC()
	d := &Document{Body: body}
	err := d.Frontmatter.Encode(metadata)
	return d, err
}

// Parse always retains the content. Unreadable YAML is kept verbatim in an
// unparsed_frontmatter field; when boundaries are unknown, the full source is body.
func Parse(data []byte) *Document {
	d := &Document{Frontmatter: yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}, Body: string(data)}
	content := bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	line, rest, _ := bytes.Cut(content, []byte("\n"))
	if !delimiter(line) {
		d.warnings = append(d.warnings, "No frontmatter; original content retained as body")
		return d
	}
	var header bytes.Buffer
	for len(rest) > 0 {
		line, remaining, newline := bytes.Cut(rest, []byte("\n"))
		if delimiter(line) {
			d.Body = string(remaining)
			var parsed yaml.Node
			decoder := yaml.NewDecoder(bytes.NewReader(header.Bytes()))
			err := decoder.Decode(&parsed)
			node := &parsed
			decoded := err == nil
			if err == io.EOF {
				err = nil
			}
			if err == nil {
				var extra yaml.Node
				if trailing := decoder.Decode(&extra); trailing != io.EOF {
					err = fmt.Errorf("content after the first YAML document is retained")
				}
			}
			if node.Kind == yaml.DocumentNode && len(node.Content) == 1 {
				node = node.Content[0]
			}
			if decoded && node.Kind == yaml.MappingNode {
				assignNode(&d.Frontmatter, node)
			}
			if err == nil && node.Kind == 0 {
				return d // Empty frontmatter is an empty mapping.
			}
			if err != nil || node.Kind != yaml.MappingNode {
				_ = d.Set("unparsed_frontmatter", header.String())
				message := "Frontmatter is not a mapping; original retained"
				if err != nil {
					message = "Original frontmatter retained: " + err.Error()
				}
				d.warnings = append(d.warnings, message)
			}
			return d
		}
		header.Write(line)
		if newline {
			header.WriteByte('\n')
		}
		rest = remaining
	}
	d.warnings = append(d.warnings, "Unclosed frontmatter; entire original retained as body")
	return d
}

func delimiter(line []byte) bool {
	return bytes.Equal(bytes.TrimRight(line, " \t\r"), []byte("---"))
}

func field(node *yaml.Node, name string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if key, ok := fieldName(node.Content[i]); ok && key == name {
			if value := node.Content[i+1]; value != nil {
				return value
			}
			return yamlValue(nil)
		}
	}
	return nil
}

// Decode scalar keys and their aliases the same way for reads, edits, and
// duplicate detection. Null and compound keys have no string field identity.
func fieldName(node *yaml.Node) (string, bool) {
	node = dereference(node)
	if node == nil || node.Kind != yaml.ScalarNode || node.ShortTag() == "!!null" {
		return "", false
	}
	var name string
	err := node.Decode(&name)
	return name, err == nil
}

func dereference(node *yaml.Node) *yaml.Node {
	seen := map[*yaml.Node]bool{}
	for node != nil && node.Kind == yaml.AliasNode {
		if seen[node] {
			return nil
		}
		seen[node] = true
		node = node.Alias
	}
	return node
}

// Resolve inherited metadata without changing the source tree. Explicit fields
// win; merge sequences use their first matching source. Each source is searched
// once per field, so shared defaults and cycles cannot multiply the work.
func metadataField(root *yaml.Node, name string) *yaml.Node {
	visited := map[*yaml.Node]bool{}
	var lookup func(*yaml.Node) *yaml.Node
	lookup = func(node *yaml.Node) *yaml.Node {
		if node == nil || visited[node] {
			return nil
		}
		visited[node] = true
		switch node.Kind {
		case yaml.AliasNode:
			return lookup(node.Alias)
		case yaml.SequenceNode:
			for _, source := range node.Content {
				if value := lookup(source); value != nil {
					return value
				}
			}
		case yaml.MappingNode:
			if value := field(node, name); value != nil {
				return value
			}
			for i := 0; i+1 < len(node.Content); i += 2 {
				if key := node.Content[i]; key != nil && key.Kind == yaml.ScalarNode && key.ShortTag() == "!!merge" {
					return lookup(node.Content[i+1])
				}
			}
		}
		return nil
	}
	return lookup(root)
}

func decodeField(root *yaml.Node, name string, value any) error {
	node := dereference(metadataField(root, name))
	if node == nil {
		return nil
	}
	err := node.Decode(value)
	if timestamp, ok := value.(*time.Time); ok {
		if err != nil {
			for _, layout := range []string{time.RFC3339Nano, time.DateTime, time.DateOnly} {
				if parsed, parseErr := time.Parse(layout, node.Value); parseErr == nil {
					*timestamp, err = parsed, nil
					break
				}
			}
		}
		if err == nil {
			*timestamp = timestamp.UTC()
		}
	}
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

func duplicateFields(node *yaml.Node) []string {
	var warnings []string
	seen := map[string]bool{}
	if node == nil || node.Kind != yaml.MappingNode {
		return warnings
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		name, ok := fieldName(node.Content[i])
		if !ok {
			continue
		}
		if seen[name] {
			warnings = append(warnings, "Duplicate field "+name+" retained; the metadata view uses its first value")
		}
		seen[name] = true
	}
	return warnings
}

func (d *Document) metadata() (m Metadata, warnings []string) {
	m.User = documentUser(d)
	for _, err := range []error{
		decodeField(&d.Frontmatter, "title", &m.Title),
		decodeField(&d.Frontmatter, "created_at", &m.CreatedAt),
		decodeField(&d.Frontmatter, "updated_at", &m.UpdatedAt),
	} {
		if err != nil {
			warnings = append(warnings, err.Error())
		}
	}
	if m.CreatedAt.IsZero() {
		warnings = append(warnings, "created_at is unknown")
	}
	if m.UpdatedAt.IsZero() {
		warnings = append(warnings, "updated_at is unknown")
	}
	warnings = append(warnings, duplicateFields(&d.Frontmatter)...)
	node := dereference(metadataField(&d.Frontmatter, "relations"))
	if node == nil || node.ShortTag() == "!!null" {
		return m, warnings
	}
	nodes := node.Content
	if node.Kind != yaml.SequenceNode {
		nodes = []*yaml.Node{node}
	}
	for i, entry := range nodes {
		var relation Relation
		for _, err := range []error{decodeField(entry, "type", &relation.Type), decodeField(entry, "target", &relation.Target)} {
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("relation %d: %v", i, err))
			}
		}
		for _, warning := range duplicateFields(dereference(entry)) {
			warnings = append(warnings, fmt.Sprintf("relation %d: %s", i, warning))
		}
		relation.Type = strings.TrimSpace(relation.Type)
		if normalized := relationType(relation.Type); normalized != "" {
			relation.Type = normalized
		} else if relation.Type != "" {
			warnings = append(warnings, fmt.Sprintf("relation %d uses unsupported type %q; original data retained", i, relation.Type))
		}
		relation.Target = strings.TrimSpace(relation.Target)
		if relation.Type == "" || !ValidID(relation.Target) {
			warnings = append(warnings, fmt.Sprintf("relation %d is unresolved", i))
		}
		if relation.Type != "" || relation.Target != "" {
			m.Relations = append(m.Relations, relation)
		}
	}
	return m, warnings
}

// Metadata reads independent fields; one broken value cannot hide the others.
func (d *Document) Metadata() Metadata {
	metadata, _ := d.metadata()
	return metadata
}

func (d *Document) Diagnostics() []string {
	metadata, warnings := d.metadata()
	if node := metadataField(&d.Frontmatter, "time"); node != nil {
		if _, issue := recordTimeNode(node, d.timeKind()); issue != "" {
			warnings = append(warnings, issue)
		}
	}
	if userOwned(kindOf(d.ID)) && (metadata.User == "" || metadata.User == "all") {
		warnings = append(warnings, "user field is missing or invalid; original content retained")
	}
	warnings = append(warnings, valueIssues(&d.Frontmatter, "metadata")...)
	if unresolved := dereference(metadataField(&d.Frontmatter, "unresolved_connections")); unresolved != nil && len(unresolved.Content) > 0 {
		warnings = append(warnings, fmt.Sprintf("%d connections need identity resolution; their details remain in unresolved_connections", len(unresolved.Content)))
	}
	if field(&d.Frontmatter, "unparsed_frontmatter") != nil {
		warnings = append(warnings, "Original unparsed frontmatter is retained")
	}
	return append(append([]string{}, d.warnings...), warnings...)
}

// Set edits one field, leaving other fields and their original representations intact.
func (d *Document) Set(name string, value any) (err error) {
	// Keep unexpected encoding failures local to this field.
	defer func() {
		if failure := recover(); failure != nil {
			err = fmt.Errorf("encode %s: %v", name, failure)
		}
	}()
	node, warnings := encodeValue(value)
	d.warnings = append(d.warnings, warnings...)
	root, warnings := normalizeNode(&d.Frontmatter)
	d.warnings = append(d.warnings, warnings...)
	if d.Frontmatter.IsZero() {
		root = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	} else if root.Kind != yaml.MappingNode {
		root = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{yamlValue("unparsed_frontmatter"), root}}
	}
	assignNode(&d.Frontmatter, root)
	if previous := field(&d.Frontmatter, name); previous != nil {
		if previous.Anchor != "" {
			node.Anchor = previous.Anchor
		}
		mergeComments(node, previous)
		assignNode(previous, node)
	} else {
		d.Frontmatter.Content = append(d.Frontmatter.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name}, node)
	}
	root, warnings = normalizeNode(&d.Frontmatter)
	d.warnings = append(d.warnings, warnings...)
	assignNode(&d.Frontmatter, root)
	return nil
}

// Content is a tree after normalization; alias pointers may be recursive.
func walkNodes(node *yaml.Node, visit func(*yaml.Node)) {
	visit(node)
	for _, child := range node.Content {
		walkNodes(child, visit)
	}
}

func assignNode(destination, source *yaml.Node) {
	*destination = *source
	walkNodes(destination, func(node *yaml.Node) {
		if node.Kind == yaml.AliasNode && node.Alias == source {
			node.Alias = destination
		}
	})
}

// Marshal preserves damaged metadata as data rather than rejecting the record.
func (d *Document) Marshal() ([]byte, error) {
	if node := metadataField(&d.Frontmatter, "time"); node != nil {
		value, issue := recordTimeNode(node, d.timeKind())
		if issue == "" {
			_ = d.Set("time", value)
		} else {
			d.warnings = append(d.warnings, issue)
		}
	}
	var output bytes.Buffer
	output.WriteString("---\n")
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	node, _ := normalizeNode(&d.Frontmatter)
	if d.Frontmatter.IsZero() {
		node = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	}
	if err := encoder.Encode(node); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	output.WriteString("---\n")
	output.WriteString(d.Body)
	return output.Bytes(), nil
}

// Record returns the same tolerant metadata view used by search and timelines.
func (d *Document) Record(kind Destination) Record { return recordFromDocument("", d, kind) }

// OriginalSource returns the exact snapshot captured by Read, before parsing or
// edits. Its revision belongs to these same bytes, including malformed headers.
func (d *Document) OriginalSource() []byte { return append([]byte(nil), d.source...) }
