package songya

import (
	"context"
	"sort"
)

type Edge struct {
	Source           string `json:"source"`
	SourceTitle      string `json:"source_title,omitempty"`
	TargetTitle      string `json:"target_title,omitempty"`
	Type             string `json:"type"`
	ReverseType      string `json:"reverse_type"`
	Target           string `json:"target"`
	Resolved         bool   `json:"resolved"`
	Unconfirmed      bool   `json:"unconfirmed,omitempty"`
	DetailsTruncated bool   `json:"details_truncated,omitempty"`
}
type GraphQuery struct {
	ID        string `json:"id"`
	Direction string `json:"direction,omitempty"`
	Type      string `json:"type,omitempty"`
	Depth     int    `json:"depth,omitempty"`
	Offset    int    `json:"offset,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}
type GraphResult struct {
	Resolution  *Resolution  `json:"resolution,omitempty"`
	Edges       []Edge       `json:"edges"`
	Next        int          `json:"next,omitempty"`
	Truncated   bool         `json:"truncated"`
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
}
type Resolution struct {
	ID          string       `json:"id"`
	Targets     []string     `json:"targets"`
	Ambiguous   bool         `json:"ambiguous"`
	Unconfirmed bool         `json:"unconfirmed,omitempty"`
	Truncated   bool         `json:"truncated,omitempty"`
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
}

func catalogMap(c Catalog) map[string]Record {
	records := map[string]Record{}
	for _, r := range c.Records {
		if r.ID != "" {
			records[r.ID] = r
		}
	}
	return records
}
func resolve(records map[string]Record, id string) Resolution {
	result := Resolution{ID: id, Targets: []string{}}
	active, seen := map[string]bool{}, map[string]bool{}
	var visit func(string)
	visit = func(current string) {
		if active[current] {
			result.Diagnostics = append(result.Diagnostics, Diagnostic{current, "redirect cycle retained"})
			return
		}
		if seen[current] {
			return
		}
		seen[current] = true
		record, ok := records[current]
		if !ok {
			result.Diagnostics = append(result.Diagnostics, Diagnostic{current, "redirect target is missing"})
			return
		}
		if record.Unconfirmed {
			result.Unconfirmed = true
			result.Targets = append(result.Targets, current)
			result.Diagnostics = append(result.Diagnostics, Diagnostic{current, "unconfirmed snapshot; redirect resolution deferred"})
			return
		}
		if len(record.Redirects) == 0 {
			result.Targets = append(result.Targets, current)
			return
		}
		if len(record.Redirects) > 1 {
			result.Ambiguous = true
		}
		active[current] = true
		for _, target := range record.Redirects {
			visit(target)
		}
		delete(active, current)
	}
	visit(id)
	sort.Strings(result.Targets)
	result.Ambiguous = result.Ambiguous || len(result.Targets) > 1
	return result
}
func (s *Store) Resolve(ctx context.Context, id string) (Resolution, error) {
	c, err := s.Scan(ctx)
	if err != nil {
		return Resolution{}, err
	}
	result := resolve(catalogMap(c), id)
	result.Diagnostics = boundedDiagnostics(append(c.Diagnostics, result.Diagnostics...))
	return result, nil
}

func graphEdges(records map[string]Record) []Edge {
	edges := []Edge{}
	for _, record := range records {
		if len(record.Redirects) > 0 && !record.Unconfirmed {
			continue
		}
		for _, relation := range record.Relations {
			resolution := resolve(records, relation.Target)
			// A split remains an explicit edge to the retired ID; consumers may inspect
			// all alternatives, but must not pretend one replacement was established.
			target := relation.Target
			if !resolution.Ambiguous && len(resolution.Targets) == 1 {
				target = resolution.Targets[0]
			}
			edges = append(edges, Edge{Source: record.ID, Type: relation.Type, ReverseType: reverseRelation(relation.Type), Target: target, Resolved: !resolution.Ambiguous && !resolution.Unconfirmed && len(resolution.Targets) == 1, Unconfirmed: record.Unconfirmed || resolution.Unconfirmed})
		}
	}
	sort.Slice(edges, func(i, j int) bool {
		a, b := edges[i], edges[j]
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		return a.Target < b.Target
	})
	unique := edges[:0]
	for _, edge := range edges {
		if len(unique) == 0 || edge != unique[len(unique)-1] {
			unique = append(unique, edge)
		}
	}
	return unique
}
func graphFromCatalog(c Catalog, q GraphQuery) GraphResult {
	records := catalogMap(c)
	visible := catalogMap(c)
	all := graphEdges(records)
	diagnostics := append([]Diagnostic{}, c.Diagnostics...)
	var resolved *Resolution
	if q.ID != "" {
		resolution := resolve(records, q.ID)
		diagnostics = append(diagnostics, resolution.Diagnostics...)
		if !resolution.Ambiguous && len(resolution.Targets) == 1 {
			q.ID = resolution.Targets[0]
		}
		if len(resolution.Targets) > 20 {
			resolution.Targets = append([]string{}, resolution.Targets[:20]...)
			resolution.Truncated = true
		}
		resolution.Diagnostics = boundedDiagnostics(resolution.Diagnostics)
		resolved = &resolution
	}
	edges := []Edge{}
	depth := q.Depth
	if depth <= 0 {
		depth = 1
	}
	if depth > 5 {
		depth = 5
	}
	visited := map[string]bool{q.ID: true}
	frontier := map[string]bool{q.ID: true}
	selected := map[Edge]bool{}
	for level := 0; level < depth; level++ {
		next := map[string]bool{}
		for _, edge := range all {
			if _, included := visible[edge.Source]; !included {
				continue
			}
			incoming := q.Direction != "outgoing" && frontier[edge.Target]
			outgoing := q.Direction != "incoming" && frontier[edge.Source]
			if q.Type != "" && edge.Type != q.Type && !((incoming || q.ID == "") && edge.ReverseType == q.Type) {
				continue
			}
			if q.ID != "" && !incoming && !outgoing {
				continue
			}
			selected[edge] = true
			for _, id := range []string{edge.Source, edge.Target} {
				if !visited[id] {
					visited[id] = true
					next[id] = true
				}
			}
		}
		frontier = next
	}
	for _, edge := range all {
		if selected[edge] {
			edges = append(edges, edge)
		}
	}
	start, end := pageRange(q.Offset, q.Limit, len(edges))
	result := GraphResult{Edges: append([]Edge{}, edges[start:end]...), Truncated: end < len(edges), Resolution: resolved}
	for i, edge := range result.Edges {
		if !edge.Resolved {
			diagnostics = append(diagnostics, Diagnostic{edge.Source, "unresolved relationship target retained; inspect the source record"})
		}
		result.Edges[i].SourceTitle = clip(visible[edge.Source].Title, 128)
		result.Edges[i].TargetTitle = clip(visible[edge.Target].Title, 128)
		result.Edges[i].Type = clip(edge.Type, 128)
		result.Edges[i].ReverseType = clip(edge.ReverseType, 128)
		result.Edges[i].Target = clip(edge.Target, 256)
		result.Edges[i].DetailsTruncated = result.Edges[i].Type != edge.Type || result.Edges[i].Target != edge.Target || len(visible[edge.Source].Title) > 128 || len(visible[edge.Target].Title) > 128
	}
	result.Diagnostics = boundedDiagnostics(diagnostics)
	if result.Truncated {
		result.Next = end
	}
	return result
}
func (s *Store) Graph(ctx context.Context, q GraphQuery) (GraphResult, error) {
	c, err := s.Scan(ctx)
	if err != nil {
		return GraphResult{}, err
	}
	return graphFromCatalog(c, q), nil
}

// Connections returns all direct source links touching id, without paging or
// redirect resolution. Editors use the actual stored targets when changing links.
func (c Catalog) Connections(id string) []Edge {
	records := catalogMap(c)
	edges := []Edge{}
	seen := map[Edge]bool{}
	for _, r := range c.Records {
		relations := append([]Relation{}, r.Relations...)
		for _, target := range r.Redirects {
			relations = append(relations, Relation{Type: "redirects", Target: target})
		}
		for _, relation := range relations {
			if r.ID != id && relation.Target != id {
				continue
			}
			edge := Edge{Source: r.ID, SourceTitle: r.Title, Target: relation.Target, TargetTitle: records[relation.Target].Title, Type: relation.Type, ReverseType: reverseRelation(relation.Type), Resolved: records[relation.Target].ID != ""}
			if !seen[edge] {
				edges = append(edges, edge)
				seen[edge] = true
			}
		}
	}
	return edges
}
