package songya

import (
	"context"
	"crypto/rand"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Key belongs to the caller's request metadata, never the model's arguments.
type MemoryInput struct {
	Key       string          `json:"-"`
	ID        string          `json:"id,omitempty"`
	User      string          `json:"user,omitempty"`
	Type      *string         `json:"type,omitempty"`
	Title     *string         `json:"title,omitempty"`
	Text      *string         `json:"text,omitempty"`
	Append    string          `json:"append,omitempty"`
	Time      any             `json:"time,omitempty"`
	Metadata  map[string]any  `json:"metadata,omitempty"`
	Relations []RelationInput `json:"relations,omitempty"`
}
type NamedInput struct {
	Key         string          `json:"-"`
	Create      bool            `json:"create,omitempty"`
	ID          string          `json:"id,omitempty"`
	Name        string          `json:"name,omitempty"`
	Type        *string         `json:"type,omitempty"`
	User        string          `json:"user,omitempty"`
	Time        any             `json:"time,omitempty"`
	Aliases     []string        `json:"aliases,omitempty"`
	Description *string         `json:"description,omitempty"`
	Append      string          `json:"append,omitempty"`
	Metadata    map[string]any  `json:"metadata,omitempty"`
	Relations   []RelationInput `json:"relations,omitempty"`
}
type StoryInput struct {
	Key       string          `json:"-"`
	ID        string          `json:"id,omitempty"`
	User      string          `json:"user,omitempty"`
	Type      *string         `json:"type,omitempty"`
	Title     string          `json:"title,omitempty"`
	Text      *string         `json:"text,omitempty"`
	Append    string          `json:"append,omitempty"`
	Metadata  map[string]any  `json:"metadata,omitempty"`
	Relations []RelationInput `json:"relations,omitempty"`
}

func writeKey(key string) string {
	if key != "" {
		return key
	}
	return "write_" + rand.Text()
}

// SaveMemory translates the caller's concepts into one durable related write.
// Entity lookup and creation run under that write's process-wide store lock.
func (s *Store) SaveMemory(ctx context.Context, input MemoryInput) (Receipt, error) {
	if TypeUser(ctx) == "" && input.User != "" {
		ctx = WithTypeUser(ctx, input.User)
	}
	input.Key = writeKey(input.Key)
	if input.ID != "" && kindOf(input.ID) != Memories {
		return Receipt{Key: input.Key, Status: "not_accepted"}, fmt.Errorf("%s is not a memory ID", input.ID)
	}
	fields := copyFields(input.Metadata)
	if input.User != "" {
		fields["user"] = input.User
	}
	if input.Title != nil {
		fields["title"] = *input.Title
	}
	if input.Time != nil {
		fields["time"] = input.Time
	}
	intent := map[string]any{"metadata": fields, "relations": relationData(input.Relations)}
	if input.Type != nil {
		intent["type"] = *input.Type
		intent["type_user"] = TypeUser(ctx)
	}
	request := BatchRequest{Key: input.Key, User: input.User, Changes: []Change{{ID: input.ID, Fields: intent, Body: input.Text, Append: input.Append}}}
	request.prepare = func(root *os.Root) ([]Change, error) {
		if err := validateTimeInput(fields, Memories); err != nil {
			return nil, err
		}
		if input.ID != "" && input.Time != nil {
			name, err := recordName(input.ID)
			if err != nil {
				return nil, err
			}
			doc, err := readCopy(root, input.ID, name)
			if err != nil {
				return nil, err
			}
			fields["time"] = patchRecordTime(metadataField(&doc.Frontmatter, "time"), input.Time, Memories)
		}
		var typeDoc *Document
		if input.ID != "" {
			name, _ := recordName(input.ID)
			var err error
			typeDoc, err = readCopy(root, input.ID, name)
			if err != nil {
				return nil, err
			}
		}
		typeUser := TypeUser(ctx)
		if typeUser == "" && typeDoc != nil {
			typeUser = documentUser(typeDoc)
			ctx = WithTypeUser(ctx, typeUser)
		}
		if err := typeFields(typeDoc, typeUser, input.Type, fields); err != nil {
			return nil, err
		}
		memory := Change{Ref: "memory", ID: input.ID, Kind: Memories, Fields: fields, Body: input.Text, Append: input.Append, preserveUnchanged: true}
		if input.Relations != nil {
			memory.Fields["relations"] = input.Relations
		}
		changes := []Change{memory}
		err := resolveConnections(ctx, root, &changes)
		return changes, err
	}
	return s.ApplyBatch(ctx, request)
}

func namedData(input NamedInput) map[string]any {
	data := map[string]any{"id": input.ID, "name": input.Name, "type": stringValue(input.Type), "aliases": input.Aliases, "metadata": input.Metadata, "append": input.Append, "relations": relationData(input.Relations)}
	if input.Type != nil {
		data["type_user"] = input.User
	}
	if input.Time != nil {
		data["time"] = input.Time
	}
	if input.Create {
		data["create"] = true
	}
	if input.Description != nil {
		data["description"] = *input.Description
	}
	return data
}
func sameName(a, b string) bool {
	return strings.EqualFold(strings.Join(strings.Fields(a), " "), strings.Join(strings.Fields(b), " "))
}

func findNamed(ctx context.Context, root *os.Root, kind Destination, input NamedInput) (*Document, error) {
	if input.ID != "" {
		name, err := recordName(input.ID)
		if err != nil {
			return nil, err
		}
		if kindOf(input.ID) != kind {
			return nil, fmt.Errorf("%s is not in %s", input.ID, kind)
		}
		doc, err := readCopy(root, input.ID, name)
		if err == nil && (doc.Pending || doc.Unconfirmed) {
			return nil, fmt.Errorf("%s needs recovery before reuse", input.ID)
		}
		return doc, err
	}
	entries, err := fs.ReadDir(root.FS(), string(kind))
	if err != nil {
		return nil, err
	}
	var match *Document
	var unavailable error
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		id := entry.Name()
		if !entry.IsDir() || kindOf(id) != kind || !ValidID(id) {
			continue
		}
		doc, err := readCopy(root, id, string(kind)+"/"+entry.Name()+"/record.md")
		if err != nil {
			unavailable = fmt.Errorf("cannot check %s: %w", id, err)
			continue
		}
		record := recordFromDocument("", doc, kind)
		names := append([]string{record.Title}, record.Aliases...)
		for _, name := range names {
			if !sameName(name, input.Name) {
				continue
			}
			storedType := doc.TypeFor(input.User)
			if input.Type != nil && storedType != "" && !sameName(*input.Type, storedType) {
				continue
			}
			if doc.Pending || doc.Unconfirmed {
				return nil, fmt.Errorf("%s needs recovery before reuse", id)
			}
			if match != nil && match.ID != doc.ID {
				return nil, fmt.Errorf("several %s named %q; choose an ID", kind, input.Name)
			}
			match = doc
			break
		}
	}
	if match == nil && unavailable != nil {
		return nil, unavailable
	}
	return match, nil
}

func namedChange(kind Destination, input NamedInput) Change {
	fields := copyFields(input.Metadata)
	if input.Name != "" {
		fields["name"] = input.Name
	}
	if input.Aliases != nil {
		fields["aliases"] = input.Aliases
	}
	if input.Relations != nil {
		fields["relations"] = input.Relations
	}
	return Change{ID: input.ID, Kind: kind, Fields: fields, Body: input.Description, Append: input.Append}
}
func relatedRecord(ctx context.Context, root *os.Root, kind Destination, input NamedInput, changes *[]Change) (string, error) {
	if input.User == "" {
		input.User = TypeUser(ctx)
	}
	if input.Create {
		if input.ID != "" || strings.TrimSpace(input.Name) == "" {
			return "", fmt.Errorf("a new named target needs create:true and name, without id")
		}
		change := namedChange(kind, input)
		if err := namedFields(nil, input, kind, change.Fields); err != nil {
			return "", err
		}
		change.Ref = fmt.Sprintf("related_%d", len(*changes))
		*changes = append(*changes, change)
		return "$" + change.Ref, nil
	}
	// Repeated participants in one capture share one record.
	for i := range *changes {
		change := &(*changes)[i]
		matches := input.ID != "" && change.ID == input.ID
		if input.ID == "" && input.Name != "" {
			node, _ := encodeValue(change.Fields["aliases"])
			for _, name := range append([]string{fieldText(change.Fields["name"])}, stringsFromNode(node)...) {
				matches = matches || sameName(name, input.Name)
			}
			storedType := ""
			if types, ok := change.Fields["types"].(map[string]string); ok {
				storedType = types[input.User]
			}
			matches = matches && (input.Type == nil || storedType == "" || sameName(storedType, *input.Type))
		}
		if change.Kind == kind && matches {
			if change.ID != "" && input.ID == "" {
				continue // A name-only link must not modify an existing identity.
			}
			if err := mergeParticipant(root, change, input); err != nil {
				return "", err
			}
			if change.ID != "" {
				return change.ID, nil
			}
			return "$" + change.Ref, nil
		}
	}
	doc, err := findNamed(ctx, root, kind, input)
	if err != nil {
		return "", err
	}
	change := namedChange(kind, input)
	if err := namedFields(doc, input, kind, change.Fields); err != nil {
		return "", err
	}
	change.Ref = fmt.Sprintf("related_%d", len(*changes))
	if doc != nil {
		if !needsEnrichment(doc, input) {
			return doc.ID, nil
		}
		if input.ID == "" {
			return "", fmt.Errorf("name %q matches existing %s; use that record's id to change it, or create:true for a separate new identity", input.Name, doc.ID)
		}
		change.ID = doc.ID
		change.enrich = true
		// Capture adds knowledge; standalone save_entity/save_topic replace a supplied description.
		change.Fields["name"] = doc.Record(kind).Title
		for i := range *changes {
			previous := &(*changes)[i]
			if previous.ID == doc.ID {
				if err := mergeParticipant(root, previous, input); err != nil {
					return "", err
				}
				return doc.ID, nil
			}
		}
		if input.Description != nil && !strings.Contains(doc.Body, *input.Description) {
			change.Append = *input.Description + "\n" + input.Append
		}
		change.Body = nil
		aliases := input.Aliases
		if input.Name != "" && !sameName(input.Name, doc.Record(kind).Title) {
			aliases = append(append([]string{}, aliases...), input.Name)
		}
		if aliases != nil {
			change.Fields["aliases"] = appendAliases(metadataField(&doc.Frontmatter, "aliases"), aliases)
		}
	}
	*changes = append(*changes, change)
	if change.ID != "" {
		return change.ID, nil
	}
	return "$" + change.Ref, nil
}

func (s *Store) SaveNamed(ctx context.Context, kind Destination, input NamedInput) (Receipt, error) {
	if input.User == "" {
		input.User = TypeUser(ctx)
	}
	if TypeUser(ctx) == "" && input.User != "" {
		ctx = WithTypeUser(ctx, input.User)
	}
	input.Key = writeKey(input.Key)
	request := BatchRequest{Key: input.Key, Changes: []Change{{Kind: kind, Fields: map[string]any{"input": namedData(input)}}}}
	request.prepare = func(root *os.Root) ([]Change, error) {
		if err := validateTimeInput(input.Metadata, kind); err != nil {
			return nil, err
		}
		if input.Create && input.ID != "" {
			return nil, fmt.Errorf("choose one: create:true and name for a new record, or id to update an existing record")
		}
		if input.Create && strings.TrimSpace(input.Name) == "" {
			return nil, fmt.Errorf("a new entity or topic needs a name")
		}
		if !input.Create && input.ID == "" {
			return nil, fmt.Errorf("to update an entity or topic, supply its id; to create a separate new one, supply create:true and name")
		}
		change := namedChange(kind, input)
		change.Ref = "record"
		var previous *Document
		if input.ID != "" {
			var err error
			previous, err = findNamed(ctx, root, kind, input)
			if err != nil {
				return nil, err
			}
		}
		if err := namedFields(previous, input, kind, change.Fields); err != nil {
			return nil, err
		}
		changes := []Change{change}
		err := resolveConnections(ctx, root, &changes)
		return changes, err
	}
	return s.ApplyBatch(ctx, request)
}
func (s *Store) SaveStory(ctx context.Context, input StoryInput) (Receipt, error) {
	if TypeUser(ctx) == "" && input.User != "" {
		ctx = WithTypeUser(ctx, input.User)
	}
	if input.ID != "" && kindOf(input.ID) != Stories {
		return Receipt{Key: input.Key, Status: "not_accepted"}, fmt.Errorf("%s is not a story ID", input.ID)
	}
	fields := copyFields(input.Metadata)
	if input.User != "" {
		fields["user"] = input.User
	}
	if input.Title != "" {
		fields["title"] = input.Title
	}
	intent := map[string]any{"metadata": fields, "relations": relationData(input.Relations)}
	if input.Type != nil {
		intent["type"] = *input.Type
		intent["type_user"] = TypeUser(ctx)
	}
	request := BatchRequest{Key: writeKey(input.Key), User: input.User, Changes: []Change{{ID: input.ID, Kind: Stories, Fields: intent, Body: input.Text, Append: input.Append}}}
	request.prepare = func(root *os.Root) ([]Change, error) {
		if err := validateTimeInput(fields, Stories); err != nil {
			return nil, err
		}
		var typeDoc *Document
		if input.ID != "" {
			name, _ := recordName(input.ID)
			var err error
			typeDoc, err = readCopy(root, input.ID, name)
			if err != nil {
				return nil, err
			}
		}
		typeUser := TypeUser(ctx)
		if typeUser == "" && typeDoc != nil {
			typeUser = documentUser(typeDoc)
			ctx = WithTypeUser(ctx, typeUser)
		}
		if err := typeFields(typeDoc, typeUser, input.Type, fields); err != nil {
			return nil, err
		}
		change := Change{Ref: "story", ID: input.ID, Kind: Stories, Fields: fields, Body: input.Text, Append: input.Append}
		if input.Relations != nil {
			change.Fields["relations"] = input.Relations
		}
		changes := []Change{change}
		err := resolveConnections(ctx, root, &changes)
		return changes, err
	}
	return s.ApplyBatch(ctx, request)
}

func appendAliases(existing *yaml.Node, aliases []string) *yaml.Node {
	node := sequenceNode(existing)
	names := stringsFromNode(existing)
	for _, alias := range aliases {
		found := false
		for _, name := range names {
			found = found || name == alias
		}
		if !found {
			node.Content = append(node.Content, yamlValue(alias))
			names = append(names, alias)
		}
	}
	return node
}

func fieldText(value any) string {
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}
func mergeParticipant(root *os.Root, change *Change, input NamedInput) error {
	pendingTime, hasPendingTime := change.Fields["time"]
	present := change.Append
	if change.Body != nil {
		present += *change.Body
	}
	if change.ID != "" {
		name, _ := recordName(change.ID)
		if doc, err := readCopy(root, change.ID, name); err == nil {
			present += doc.Body
		}
	}
	additions := []string{input.Append}
	if input.Description != nil {
		additions = append(additions, *input.Description)
	}
	for _, addition := range additions {
		if strings.TrimSpace(addition) != "" && !strings.Contains(present, addition) {
			change.Append += "\n" + addition
			present += "\n" + addition
		}
	}
	for key, value := range input.Metadata {
		change.Fields[key] = value
	}
	if input.Type != nil || input.Time != nil || input.Metadata["time"] != nil {
		doc := &Document{}
		if change.ID != "" {
			name, _ := recordName(change.ID)
			if previous, err := readCopy(root, change.ID, name); err == nil {
				doc = previous
			}
		}
		if hasPendingTime {
			if err := doc.Set("time", pendingTime); err != nil {
				return err
			}
		}
		if err := namedFields(doc, input, change.Kind, change.Fields); err != nil {
			return err
		}
	}
	if input.Relations != nil {
		previous, _ := change.Fields["relations"].([]RelationInput)
		change.Fields["relations"] = append(previous, input.Relations...)
	}
	if input.Aliases != nil {
		node, _ := encodeValue(change.Fields["aliases"])
		change.Fields["aliases"] = appendAliases(node, input.Aliases)
	}
	return nil
}

func needsEnrichment(doc *Document, input NamedInput) bool {
	if value, supplied := namedTime(input); supplied {
		before := metadataField(&doc.Frontmatter, "time")
		after, _ := encodeValue(patchRecordTime(before, value, doc.timeKind()))
		if !sameFieldValue(before, after) {
			return true
		}
	}

	if len(input.Metadata) > 0 || input.Relations != nil {
		return true
	}
	if input.Type != nil && !sameName(*input.Type, doc.TypeFor(input.User)) {
		return true
	}
	if input.Description != nil && !strings.Contains(doc.Body, *input.Description) {
		return true
	}
	if input.Append != "" && !strings.Contains(doc.Body, input.Append) {
		return true
	}
	existing := nodeStrings("aliases", doc)
	if input.Name != "" {
		found := sameName(input.Name, doc.Record(kindOf(doc.ID)).Title)
		for _, alias := range existing {
			found = found || sameName(input.Name, alias)
		}
		if !found {
			return true
		}
	}
	for _, alias := range input.Aliases {
		found := false
		for _, name := range existing {
			found = found || name == alias
		}
		if !found {
			return true
		}
	}
	return false
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func namedTime(input NamedInput) (any, bool) {
	if input.Time != nil {
		return input.Time, true
	}
	value, present := input.Metadata["time"]
	return value, present
}
func namedFields(doc *Document, input NamedInput, kind Destination, fields map[string]any) error {
	if err := typeFields(doc, input.User, input.Type, fields); err != nil {
		return err
	}
	if value, present := namedTime(input); present {
		if err := validateTimeInput(map[string]any{"time": value}, kind); err != nil {
			return err
		}
		var before *yaml.Node
		if doc != nil {
			before = metadataField(&doc.Frontmatter, "time")
		}
		fields["time"] = patchRecordTime(before, value, kind)
	}
	return nil
}
