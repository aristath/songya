package songya

import (
	"context"
	"sort"
	"time"
)

type TemporalConstraint struct {
	Before string `json:"before"`
	After  string `json:"after"`
	Usable bool   `json:"usable"`
	Reason string `json:"reason,omitempty"`
}
type TemporalRecord struct {
	User             string     `json:"user,omitempty"`
	ID               string     `json:"id"`
	Title            string     `json:"title"`
	Time             RecordTime `json:"time"`
	Relations        []Relation `json:"relations,omitempty"`
	Unordered        bool       `json:"unordered"`
	Unconfirmed      bool       `json:"unconfirmed,omitempty"`
	DetailsTruncated bool       `json:"details_truncated,omitempty"`
}
type Timeline struct {
	Groups      map[string][]string  `json:"groups,omitempty"`
	Records     []TemporalRecord     `json:"records"`
	Constraints []TemporalConstraint `json:"constraints"`
	Next        int                  `json:"next,omitempty"`
	Truncated   bool                 `json:"truncated"`
	Diagnostics []Diagnostic         `json:"diagnostics,omitempty"`
}

func parseBound(raw string, latest bool) (time.Time, bool) {
	if raw == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.DateTime, time.DateOnly} {
		if parsed, err := time.Parse(layout, raw); err == nil {
			if latest && layout == time.DateOnly {
				parsed = parsed.Add(24*time.Hour - time.Nanosecond)
			}
			return parsed.UTC(), true
		}
	}
	return time.Time{}, false
}

// temporalComponents identifies actual cycles in linear time. Downstream records
// are not cyclic merely because one of their predecessors belongs to a cycle.
func temporalComponents(ids []string, adjacent map[string][]string) map[string]int {
	index, low, component := map[string]int{}, map[string]int{}, map[string]int{}
	active := map[string]bool{}
	stack := []string{}
	sequence, group := 0, 0
	var visit func(string)
	visit = func(id string) {
		sequence++
		index[id], low[id] = sequence, sequence
		stack = append(stack, id)
		active[id] = true
		for _, next := range adjacent[id] {
			if index[next] == 0 {
				visit(next)
				low[id] = min(low[id], low[next])
			} else if active[next] {
				low[id] = min(low[id], index[next])
			}
		}
		if low[id] != index[id] {
			return
		}
		group++
		for {
			last := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			active[last] = false
			component[last] = group
			if last == id {
				break
			}
		}
	}
	for _, id := range ids {
		if index[id] == 0 {
			visit(id)
		}
	}
	return component
}

// Timeline returns a partial chronology, never a guessed total ordering. Global
// analysis visits explicit edges; date-only edges are derived for this page,
// avoiding a quadratic graph of every dated pair in the entire store.
func (s *Store) Timeline(ctx context.Context, q Query) (Timeline, error) {
	c, err := s.Scan(ctx)
	if err != nil {
		return Timeline{}, err
	}
	return timeline(ctx, c, q, false)
}

// Timeline analyzes a catalogue snapshot for an application view. It returns all
// matching records and explicit constraints, without the quadratic date-pair
// expansion used by the bounded tool page. Dates themselves remain authoritative.
func (c Catalog) Timeline(ctx context.Context, q Query) (Timeline, error) {
	return timeline(ctx, c, q, true)
}

func timeline(ctx context.Context, c Catalog, q Query, full bool) (Timeline, error) {
	c = userCatalog(c, q.User)
	records := catalogMap(c)
	diagnostics := append([]Diagnostic{}, c.Diagnostics...)
	ids := []string{}
	for id, r := range records {
		if len(r.Redirects) == 0 || r.Unconfirmed {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	earliest, latest := map[string]time.Time{}, map[string]time.Time{}
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return Timeline{}, err
		}
		r := records[id]
		if r.Unconfirmed {
			continue
		}
		start, end := r.Time.bounds()
		if value, ok := parseBound(start, false); ok {
			earliest[id] = value
		} else if start != "" {
			diagnostics = append(diagnostics, Diagnostic{id, "unrecognized earliest episode bound retained"})
		}
		if value, ok := parseBound(end, true); ok {
			latest[id] = value
		} else if end != "" {
			diagnostics = append(diagnostics, Diagnostic{id, "unrecognized latest episode bound retained"})
		}
		if !earliest[id].IsZero() && !latest[id].IsZero() && earliest[id].After(latest[id]) {
			diagnostics = append(diagnostics, Diagnostic{id, "episode bounds conflict"})
			delete(earliest, id)
			delete(latest, id)
		}
	}
	constraints := []TemporalConstraint{}
	seen := map[[2]string]bool{}
	add := func(a, b, reason string) {
		key := [2]string{a, b}
		if !seen[key] {
			seen[key] = true
			constraints = append(constraints, TemporalConstraint{a, b, true, reason})
		}
	}
	for _, edge := range graphEdges(records) {
		if !edge.Resolved || edge.Unconfirmed {
			continue
		}
		switch edge.Type {
		case "after":
			add(edge.Target, edge.Source, "explicit after relation")
		case "caused_by":
			add(edge.Target, edge.Source, "cause precedes consequence")
		}
	}
	adjacent := map[string][]string{}
	for _, edge := range constraints {
		adjacent[edge.Before] = append(adjacent[edge.Before], edge.After)
	}
	components := temporalComponents(ids, adjacent)
	indegree := map[string]int{}
	adjacent = map[string][]string{}
	for i, edge := range constraints {
		if components[edge.Before] == components[edge.After] {
			constraints[i].Usable = false
			diagnostics = append(diagnostics, Diagnostic{edge.Before, "ordering cycle involving " + edge.After})

		} else {
			adjacent[edge.Before] = append(adjacent[edge.Before], edge.After)
			indegree[edge.After]++
		}
	}
	// Topological propagation detects incompatible bounds through arbitrarily long
	// chains without repeatedly scanning the whole graph. Derived bounds are used
	// only for validation; the displayed time remains the original recollection.
	low, high := map[string]time.Time{}, map[string]time.Time{}
	for id, value := range earliest {
		low[id] = value
	}
	for id, value := range latest {
		high[id] = value
	}
	order := []string{}
	for _, id := range ids {
		if indegree[id] == 0 {
			order = append(order, id)
		}
	}
	for cursor := 0; cursor < len(order); cursor++ {
		if err := ctx.Err(); err != nil {
			return Timeline{}, err
		}
		id := order[cursor]
		for _, next := range adjacent[id] {
			if value := low[id]; !value.IsZero() {
				value = value.Add(time.Nanosecond)
				if low[next].IsZero() || low[next].Before(value) {
					low[next] = value
				}
			}
			indegree[next]--
			if indegree[next] == 0 {
				order = append(order, next)
			}
		}
	}
	for i := len(order) - 1; i >= 0; i-- {
		id := order[i]
		for _, next := range adjacent[id] {
			if value := high[next]; !value.IsZero() {
				value = value.Add(-time.Nanosecond)
				if high[id].IsZero() || high[id].After(value) {
					high[id] = value
				}
			}
		}
	}
	for _, id := range ids {
		if !low[id].IsZero() && !high[id].IsZero() && low[id].After(high[id]) {
			diagnostics = append(diagnostics, Diagnostic{id, "date bounds and relative order are incompatible"})
		}
	}
	related := map[string]bool{}
	for i, edge := range constraints {
		if !low[edge.Before].IsZero() && !high[edge.After].IsZero() &&
			low[edge.Before].Add(time.Nanosecond).After(high[edge.After]) {
			constraints[i].Usable = false
		}
		if constraints[i].Usable {
			related[edge.Before] = true
			related[edge.After] = true
		}
	}
	// Extremes tell whether a record has a date-proven ordering anywhere in the
	// store, without constructing every pair. Strict inequality excludes itself.
	var firstEnd, lastStart time.Time
	for _, value := range latest {
		if firstEnd.IsZero() || value.Before(firstEnd) {
			firstEnd = value
		}
	}
	for _, value := range earliest {
		if lastStart.IsZero() || value.After(lastStart) {
			lastStart = value
		}
	}
	for _, id := range ids {
		if (!earliest[id].IsZero() && !firstEnd.IsZero() && firstEnd.Before(earliest[id])) ||
			(!latest[id].IsZero() && !lastStart.IsZero() && latest[id].Before(lastStart)) {
			related[id] = true
		}
	}
	var selected SearchResult
	if full {
		selected = SearchResult{Matches: rankMatches(c, q)}
	} else {
		selected = searchCatalog(c, q)
	}
	result := Timeline{Groups: map[string][]string{}, Records: []TemporalRecord{}, Constraints: []TemporalConstraint{}, Next: selected.Next, Truncated: selected.Truncated}
	displayed := map[string]bool{}
	for _, match := range selected.Matches {
		if match.ID == "" {
			continue
		}
		r := records[match.ID]
		episode := r.Time
		for _, field := range []**string{&episode.Date, &episode.Earliest, &episode.Latest} {
			if *field != nil && len(**field) > 96 {
				clipped := clip(**field, 96)
				*field = &clipped
			}
		}
		relations := append([]Relation{}, r.Relations[:min(16, len(r.Relations))]...)
		if full {
			episode = r.Time
			relations = append([]Relation{}, r.Relations...)
		}
		for i := range relations {
			relations[i].Type = clip(relations[i].Type, 128)
			relations[i].Target = clip(relations[i].Target, 128)
			if relations[i].Type == "part_of" && ValidID(relations[i].Target) {
				result.Groups[relations[i].Target] = append(result.Groups[relations[i].Target], r.ID)
			}
		}
		result.Records = append(result.Records, TemporalRecord{User: clip(r.User, 160), ID: r.ID, Title: clip(r.Title, 160), Time: episode, Relations: relations, Unordered: !related[r.ID], Unconfirmed: r.Unconfirmed, DetailsTruncated: len(r.Relations) > len(relations) || episode != r.Time || len(r.Title) > 160 || len(r.User) > 160})
		displayed[r.ID] = true
	}
	if !full {
		for _, a := range result.Records {
			for _, b := range result.Records {
				if a.ID != b.ID && !latest[a.ID].IsZero() && !earliest[b.ID].IsZero() && latest[a.ID].Before(earliest[b.ID]) {
					add(a.ID, b.ID, "non-overlapping episode bounds")
				}
			}
		}
	}
	for _, edge := range constraints {
		if displayed[edge.Before] && displayed[edge.After] {
			result.Constraints = append(result.Constraints, edge)
		}
	}
	sort.Slice(result.Constraints, func(i, j int) bool {
		a, b := result.Constraints[i], result.Constraints[j]
		if a.Before != b.Before {
			return a.Before < b.Before
		}
		return a.After < b.After
	})
	result.Diagnostics = boundedDiagnostics(diagnostics)
	if full {
		result.Diagnostics = diagnostics
	}
	return result, nil
}
