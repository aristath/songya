package songya

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

type Version struct {
	ID       string `json:"id"`
	Revision string `json:"-"`
}
type MergeRequest struct {
	Key     string         `json:"key"`
	Sources []Version      `json:"sources"`
	Kind    Destination    `json:"kind,omitempty"`
	Fields  map[string]any `json:"fields,omitempty"`
	Body    *string        `json:"body,omitempty"`
}
type Part struct {
	Kind   Destination    `json:"kind,omitempty"`
	Fields map[string]any `json:"fields,omitempty"`
	Body   string         `json:"body"`
}
type SplitRequest struct {
	Key    string  `json:"key"`
	Source Version `json:"source"`
	Parts  []Part  `json:"parts"`
}

func copyFields(fields map[string]any) map[string]any {
	copy := map[string]any{}
	for key, value := range fields {
		copy[key] = value
	}
	return copy
}
func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, value := range values {
		if !seen[value] && value != "" {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}
func kindOf(id string) Destination {
	for _, kind := range destinations {
		prefix, _ := kind.prefix()
		if strings.HasPrefix(id, prefix) {
			return kind
		}
	}
	return ""
}

// Merge preserves every source body under its retired ID. The default canonical
// body concatenates those originals; a reviewed replacement may be supplied.
func (s *Store) Merge(ctx context.Context, request MergeRequest) (Receipt, error) {
	if len(request.Sources) < 2 {
		return Receipt{}, fmt.Errorf("merge needs at least two source records")
	}
	fields := copyFields(request.Fields)
	title := ""
	aliasNodes := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	seenAliases := map[string]*yaml.Node{}
	addAliases := func(source *yaml.Node) {
		for _, node := range sequenceNode(source).Content {
			if name, scalar := fieldName(node); scalar {
				if previous := seenAliases[name]; previous != nil {
					mergeComments(previous, node)
					continue
				}
				seenAliases[name] = node
			}
			aliasNodes.Content = append(aliasNodes.Content, node)
		}
	}
	suppliedAliases, _ := encodeValue(fields["aliases"])
	addAliases(suppliedAliases)
	relations := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	ids := []string{}
	bodies := []string{}
	changes := []Change{}
	for _, source := range request.Sources {
		if err := ctx.Err(); err != nil {
			return Receipt{}, err
		}
		doc, err := s.sourceForBatch(request.Key, source)
		if err != nil {
			return Receipt{}, err
		}
		if source.Revision != "" && doc.Revision != source.Revision {
			return Receipt{}, ErrConflict
		}
		if len(nodeStrings("redirects", doc)) > 0 {
			return Receipt{}, fmt.Errorf("%s is retired; resolve its replacements first", source.ID)
		}
		r := recordFromDocument("", doc, kindOf(source.ID))

		if title == "" {
			title = r.Title
		}
		addAliases(metadataField(&doc.Frontmatter, "aliases"))
		addAliases(yamlValue(r.Title))
		relations.Content = append(relations.Content, sequenceNode(metadataField(&doc.Frontmatter, "relations")).Content...)
		ids = append(ids, source.ID)
		bodies = append(bodies, "## "+r.Title+"\n\n"+doc.Body)
		changes = append(changes, Change{ID: source.ID, Revision: doc.Revision, Fields: map[string]any{"redirects": []string{"$merged"}}})
	}
	if len(uniqueStrings(ids)) != len(ids) {
		return Receipt{}, fmt.Errorf("merge sources must identify different records")
	}
	if request.Kind == "" {
		request.Kind = kindOf(ids[0])
	}
	if _, exists := fields["title"]; !exists {
		fields["title"] = title
	}
	fields["aliases"] = aliasNodes
	fields["merged_from"] = ids
	if _, exists := fields["relations"]; !exists {
		fields["relations"] = relations
	}
	body := strings.Join(bodies, "\n\n")
	if request.Body != nil {
		body = *request.Body
	}
	changes = append([]Change{{Ref: "merged", Kind: request.Kind, Fields: fields, Body: &body}}, changes...)
	return s.ApplyBatch(ctx, BatchRequest{Key: request.Key, Changes: changes})
}

// Split makes the one-to-many redirect explicit. The original remains readable;
// neither incoming links nor stories are assigned to an arbitrary replacement.
func (s *Store) Split(ctx context.Context, request SplitRequest) (Receipt, error) {
	if len(request.Parts) < 2 {
		return Receipt{}, fmt.Errorf("split needs at least two reviewed parts")
	}
	original, err := s.sourceForBatch(request.Key, request.Source)
	if err != nil {
		return Receipt{}, err
	}
	if request.Source.Revision != "" && original.Revision != request.Source.Revision {
		return Receipt{}, ErrConflict
	}
	if len(nodeStrings("redirects", original)) > 0 {
		return Receipt{}, fmt.Errorf("%s is retired; resolve its replacements first", original.ID)
	}
	changes := []Change{}
	redirects := []string{}
	for i, part := range request.Parts {
		if err := ctx.Err(); err != nil {
			return Receipt{}, err
		}
		ref := fmt.Sprintf("part_%d", i+1)
		fields := copyFields(part.Fields)
		fields["split_from"] = original.ID
		kind := part.Kind
		if kind == "" {
			kind = kindOf(original.ID)
		}
		changes = append(changes, Change{Ref: ref, Kind: kind, Fields: fields, Body: textValue(part.Body)})
		redirects = append(redirects, "$"+ref)
	}
	changes = append(changes, Change{ID: original.ID, Revision: original.Revision, Fields: map[string]any{"redirects": redirects}})
	return s.ApplyBatch(ctx, BatchRequest{Key: request.Key, Changes: changes})
}
func textValue(value string) *string { return &value }

type Ambiguity struct {
	Name       string      `json:"name"`
	Kind       Destination `json:"kind"`
	Candidates []string    `json:"candidates"`
	Truncated  bool        `json:"truncated,omitempty"`
}
type Preparation struct {
	Key   string `json:"key"`
	Query string `json:"query"`

	Matches            []Match      `json:"matches"`
	Ambiguities        []Ambiguity  `json:"ambiguities"`
	PossibleDuplicates [][]string   `json:"possible_duplicates"`
	Next               int          `json:"next,omitempty"`
	Truncated          bool         `json:"truncated"`
	Diagnostics        []Diagnostic `json:"diagnostics,omitempty"`
}

// Prepare performs deterministic local retrieval. The caller interprets the
// shortlist and submits one complete ApplyBatch request; no inference is hidden.
func (s *Store) Prepare(ctx context.Context, account string, limit int) (Preparation, error) {
	catalog, err := s.Scan(ctx)
	if err != nil {
		return Preparation{}, err
	}

	matches := searchCatalog(catalog, Query{Text: account, Limit: limit})
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		return Preparation{}, err
	}
	result := Preparation{Key: fmt.Sprintf("capture_%x", random[:]), Query: clip(account, 1024), Matches: matches.Matches, Ambiguities: []Ambiguity{}, PossibleDuplicates: [][]string{}, Truncated: matches.Truncated, Next: matches.Next, Diagnostics: matches.Diagnostics}
	names := map[string][]string{}
	kinds := map[string]Destination{}
	labels := map[string]string{}
	for _, r := range catalog.Records {
		if r.ID == "" || len(r.Redirects) > 0 && !r.Unconfirmed {
			continue
		}
		for _, name := range append([]string{r.Title}, r.Aliases...) {
			normalized := strings.Join(words(name), " ")
			if normalized == "" {
				continue
			}
			key := string(r.Kind) + ":" + normalized
			names[key] = append(names[key], r.ID)
			kinds[key] = r.Kind
			labels[key] = name
		}
	}
	keys := []string{}
	for key := range names {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	query := " " + strings.Join(words(account), " ") + " "
	matched := map[string]bool{}
	for _, match := range matches.Matches {
		matched[match.ID] = true
	}
	for _, key := range keys {
		ids := uniqueStrings(names[key])
		sort.Strings(ids)
		if len(ids) < 2 {
			continue
		}
		normalized := strings.TrimPrefix(key, string(kinds[key])+":")
		mentioned := strings.Contains(query, " "+normalized+" ")
		relevant := mentioned
		for _, id := range ids {
			relevant = relevant || matched[id]
		}
		if !relevant {
			continue
		}
		if len(result.PossibleDuplicates) < 8 {
			group := ids
			if len(group) > 10 {
				result.Truncated = true
				group = append([]string{}, group[:10]...)
			}
			result.PossibleDuplicates = append(result.PossibleDuplicates, group)
		} else {
			result.Truncated = true
		}
		if mentioned {
			truncated := len(ids) > 10
			if truncated {
				ids = append([]string{}, ids[:10]...)
			}
			if len(result.Ambiguities) < 8 {
				result.Ambiguities = append(result.Ambiguities, Ambiguity{Name: clip(labels[key], 128), Kind: kinds[key], Candidates: ids, Truncated: truncated})
			} else {
				result.Truncated = true
			}
		}
	}
	return result, nil
}

// Organization retries reconstruct their original inputs from the durable
// journal, so a successful redirect does not turn the same request into a stale
// update or cause a second consolidated record to be created.
func (s *Store) sourceForBatch(key string, version Version) (*Document, error) {
	if s == nil {
		return nil, errors.New("store is nil")
	}
	root, err := os.OpenRoot(s.path)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if journal, err := loadJournal(root, batchDirectory(key)); err == nil {
		for _, entry := range journal.Entries {
			if entry.ID == version.ID && (version.Revision == "" || entry.Base == version.Revision) {
				doc := Parse(entry.Before)
				doc.ID, doc.Revision = version.ID, entry.Base
				return doc, nil
			}
		}
	}
	return s.Read(version.ID)
}
