package songya

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

// Keep semantic metadata available without copying storage provenance into recall.
func recallContext(d *Document) string {
	node, _ := normalizeNode(&d.Frontmatter)
	context := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for i := 0; i+1 < len(node.Content); i += 2 {
		name, _ := fieldName(node.Content[i])
		switch name {
		case "id", "created_at", "updated_at", "source", "original_accounts", "user", "title", "name", "aliases", "description", "time", "relations", "entities", "topics", "broader", "redirects", "unparsed_frontmatter":
			continue
		}
		context.Content = append(context.Content, node.Content[i], node.Content[i+1])
	}
	if len(context.Content) == 0 {
		return ""
	}
	data, err := yaml.Marshal(context)
	if err != nil {
		return "[metadata needs inspection with view=source]"
	}
	return string(data)
}

// RecallQuery selects connected accounts. Offset and Limit count narrative bytes.
type RecallQuery struct {
	Text   string `json:"text"`
	User   string `json:"user,omitempty"`
	Offset int    `json:"offset,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

type RecallPage struct {
	Text        string       `json:"text,omitempty"`
	Base64      string       `json:"base64,omitempty"`
	Offset      int          `json:"offset"`
	Next        int          `json:"next,omitempty"`
	Truncated   bool         `json:"truncated"`
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
}

// Recall returns canonical prose with record labels and story context. It does
// not generate summaries, choose a primary story, or modify stored accounts.
func (s *Store) Recall(ctx context.Context, q RecallQuery) (RecallPage, error) {
	if strings.TrimSpace(q.Text) == "" {
		return RecallPage{}, fmt.Errorf("provide a name, subject or record ID in text")
	}
	c, err := s.Scan(ctx)
	if err != nil {
		return RecallPage{}, err
	}
	c = userCatalog(c, q.User)
	records := catalogMap(c)
	for _, r := range c.Records {
		if r.ID == "" {
			records[r.Source] = r
		}
	}
	roots := []string{}
	if ValidID(strings.TrimSpace(q.Text)) {
		id := strings.TrimSpace(q.Text)
		resolution := resolve(records, id)
		roots = append(roots, resolution.Targets...)
		c.Diagnostics = append(c.Diagnostics, resolution.Diagnostics...)
	} else {
		matches := rankMatches(c, Query{Text: q.Text})
		// A named identity is the hub. A subject without a matching identity falls
		// back to lexical accounts. Equally named identities are all retained.
		for _, m := range matches {
			if (m.Kind == Entities || m.Kind == Topics) && len(m.Reasons) > 0 {
				id := m.ID
				if id == "" {
					id = m.Source
				}
				roots = append(roots, id)
			}
		}
		if len(roots) == 0 {
			for _, m := range matches {
				id := m.ID
				if id == "" {
					id = m.Source
				}
				roots = append(roots, id)
			}
		}
	}
	selected := map[string]bool{}
	for _, id := range roots {
		selected[id] = true
	}
	edges := graphEdges(records)
	hubs := map[string]bool{}
	for _, id := range roots {
		if r := records[id]; r.Kind == Entities || r.Kind == Topics {
			hubs[id] = true
		}
	}
	parents := map[string][]string{}
	for _, edge := range edges {
		if hubs[edge.Source] {
			selected[edge.Target] = true
		}
		if hubs[edge.Target] {
			selected[edge.Source] = true
		}
	}
	for _, story := range c.Records {
		for _, member := range storyMembers(story) {
			parents[member] = append(parents[member], story.ID)
		}
	}
	// Include enclosing narratives even when their other participants do not link
	// to the queried identity. Walk ancestors once, including nested stories.
	queue := []string{}
	for id := range selected {
		queue = append(queue, id)
	}
	for i := 0; i < len(queue); i++ {
		for _, parent := range parents[queue[i]] {
			if !selected[parent] {
				selected[parent] = true
				queue = append(queue, parent)
			}
		}
	}
	stories := []string{}
	for id := range selected {
		if records[id].Kind == Stories {
			stories = append(stories, id)
		}
	}
	// Put the most directly connected account first; no story owns an identity.
	coverage := map[string]int{}
	for _, story := range c.Records {
		for _, member := range storyMembers(story) {
			if selected[member] {
				coverage[story.ID]++
			}
		}
	}
	sort.Slice(stories, func(i, j int) bool {
		if coverage[stories[i]] != coverage[stories[j]] {
			return coverage[stories[i]] > coverage[stories[j]]
		}
		return stories[i] < stories[j]
	})
	order := []string{}
	for _, id := range roots {
		if r := records[id]; r.Kind == Entities || r.Kind == Topics {
			order = append(order, id)
		}
	}
	order = append(order, stories...)
	// Search order is relevance order; remaining connected accounts use stable IDs.
	order = append(order, roots...)
	rest := []string{}
	for id := range selected {
		rest = append(rest, id)
	}
	sort.Strings(rest)
	order = append(order, rest...)

	offset := max(0, q.Offset)
	size := q.Limit
	if size <= 0 {
		size = 16384
	}
	size = min(size, 16384)
	skip := offset
	var output strings.Builder
	valid := true
	write := func(value string) {
		valid = valid && utf8.ValidString(value)
		if skip >= len(value) {
			skip -= len(value)
			return
		}
		value = value[skip:]
		skip = 0
		remaining := size + 2*utf8.UTFMax - output.Len()
		if remaining > 0 {
			output.WriteString(value[:min(len(value), remaining)])
		}
	}
	seen := map[string]bool{}
	var visit func(string, int)
	visit = func(id string, depth int) {
		if output.Len() >= size+2*utf8.UTFMax || ctx.Err() != nil {
			return
		}
		if depth > 32 {
			write("[[" + id + "]] [retrieve nested account separately]\n")
			c.Diagnostics = append(c.Diagnostics, Diagnostic{id, "nested account exceeds this traversal depth; recall its ID separately"})
			return
		}
		if seen[id] {
			write("[[" + id + "]] [shown elsewhere in this result]\n")
			return
		}
		seen[id] = true
		r, ok := records[id]
		if !ok {
			write("[[" + id + "]] [record unavailable in this selection]\n")
			c.Diagnostics = append(c.Diagnostics, Diagnostic{id, "referenced record unavailable in this selection"})
			return
		}
		label := map[string]any{"id": r.ID, "kind": r.Kind, "title": r.Title}
		if r.ID == "" {
			label["source"] = r.Source
		}
		if len(r.Redirects) > 0 {
			label["redirects"] = r.Redirects
		}
		if r.Description != "" && r.Description != r.Body {
			label["description"] = r.Description
		}
		if len(r.Aliases) > 0 {
			label["aliases"] = r.Aliases
		}
		if r.User != "" {
			label["user"] = r.User
		}
		if r.Time.hasDates() {
			label["time"] = r.Time
		}
		if r.Pending {
			label["pending"] = true
		}
		if r.Unconfirmed {
			label["unconfirmed"] = true
		}
		if len(r.Relations) > 0 {
			label["relations"] = r.Relations
		}
		header, _ := json.Marshal(label)
		write("\n\n## ")
		write(string(header))
		write("\n")
		if r.Context != "" {
			write("Context:\n")
			write(r.Context)
			write("\n")
		}
		for _, warning := range r.Diagnostics {
			c.Diagnostics = append(c.Diagnostics, Diagnostic{r.Source, warning})
		}
		if r.Kind != Stories {
			write(r.Body)
			if !r.Unconfirmed {
				for _, target := range r.Redirects {
					visit(target, depth+1)
				}
			}
			return
		}
		write(r.Body)
		for _, member := range storyMembers(r) {
			write("\n\n")
			visit(member, depth+1)
			if output.Len() >= size+2*utf8.UTFMax || ctx.Err() != nil {
				return
			}
		}
	}
	for _, id := range order {
		if !seen[id] {
			visit(id, 0)
		}
		if output.Len() >= size+2*utf8.UTFMax || ctx.Err() != nil {
			break
		}
	}
	if err := ctx.Err(); err != nil {
		return RecallPage{}, err
	}
	text := output.String()
	start := 0
	for valid && start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}
	end := min(start+size, len(text))
	for valid && end > start && end < len(text) && !utf8.RuneStart(text[end]) {
		end--
	}
	if valid && end == start && start < len(text) {
		_, n := utf8.DecodeRuneInString(text[start:])
		end = start + n
	}
	actualOffset := offset - skip + start
	page := RecallPage{Text: text[start:end], Offset: actualOffset, Truncated: end < len(text), Diagnostics: boundedDiagnostics(c.Diagnostics)}
	if !utf8.ValidString(page.Text) {
		page.Base64 = base64.StdEncoding.EncodeToString([]byte(page.Text))
		page.Text = ""
	}
	if page.Truncated {
		page.Next = offset - skip + end
	}
	return page, nil
}
