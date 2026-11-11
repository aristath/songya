package web

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/aristath/songya"
)

const graphWeightFile = "graph-weights.json"
const graphWeightVersion = 4
const graphWeightTTL = 24 * time.Hour

type graphWeightEntry struct {
	Weight       float64   `json:"weight"`
	CalculatedAt time.Time `json:"calculated_at"`
	Revision     string    `json:"revision"`
	Neighbors    []string  `json:"neighbors"`
	Pending      bool      `json:"pending,omitempty"`
}

type graphWeightCache struct {
	Version int                         `json:"version"`
	Entries map[string]graphWeightEntry `json:"records"`
}

type graphWeights struct {
	mu                 sync.Mutex
	cache              graphWeightCache
	wake               chan struct{}
	initialized, dirty bool
	issue              string
}

func newGraphWeights(a *chatApp) *graphWeights {
	w := &graphWeights{cache: graphWeightCache{Version: graphWeightVersion, Entries: map[string]graphWeightEntry{}}, wake: make(chan struct{}, 1)}
	// This file is a rebuildable projection. An unreadable entry does not discard healthy entries.
	if data, err := a.readFile(graphWeightFile); err == nil {
		var saved struct {
			Version int
			Records map[string]json.RawMessage
		}
		if json.Unmarshal(data, &saved) == nil && saved.Version == graphWeightVersion {
			for id, raw := range saved.Records {
				var entry graphWeightEntry
				if songya.ValidID(id) && json.Unmarshal(raw, &entry) == nil && entry.Weight >= 0 && !math.IsNaN(entry.Weight) && !math.IsInf(entry.Weight, 0) {
					w.cache.Entries[id] = entry
				}
			}
		}
	}
	return w
}

func (a *chatApp) recordsChanged() {
	a.recordsRevision.Add(1)
	if a.weights != nil {
		select {
		case a.weights.wake <- struct{}{}:
		default:
		}
	}
}

func (a *chatApp) runGraphWeights() {
	defer a.wg.Done()
	for {
		catalog, err := a.store.Scan(a.ctx)
		if err == nil {
			now := time.Now()
			changed := a.weights.update(a.ctx, newWeightGraph(&catalog), now, len(catalog.Diagnostics) == 0)
			a.weights.mu.Lock()
			previousIssue := a.weights.issue
			if a.weights.dirty {
				data, marshalErr := json.MarshalIndent(a.weights.cache, "", "  ")
				err = marshalErr
				if err == nil {
					err = a.publishOnce(graphWeightFile, append(data, '\n'))
				}
				if err == nil {
					a.weights.dirty = false
					a.weights.issue = ""
				} else {
					a.weights.issue = "Graph weights could not be saved; retrying."
				}
			}
			changed = changed || previousIssue != a.weights.issue
			a.weights.mu.Unlock()
			if changed {
				a.recordsRevision.Add(1)
			}
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-a.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		case <-a.weights.wake:
		}
		timer.Stop()
	}
}

// One worker drains due work, prioritising edits. Scoring runs outside the cache
// lock so readers can keep using the previous values during recalculation.
func (w *graphWeights) update(ctx context.Context, g *weightGraph, now time.Time, complete bool) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	roots, immediate := map[string]bool{}, map[string]bool{}
	oldEdges := map[string][]string{}
	for id, entry := range w.cache.Entries {
		oldEdges[id] = entry.Neighbors
	}
	for id, r := range g.records {
		entry, exists := w.cache.Entries[id]
		neighbors := g.neighbors(id)
		if (exists && entry.Revision != r.Revision) || (!exists && (w.initialized || len(oldEdges) > 0)) {
			roots[id] = true
			if exists {
				immediate[id] = true
			}
			// Changing an edge changes both endpoints' degrees, including removed edges.
			for _, target := range append(slices.Clone(entry.Neighbors), neighbors...) {
				roots[target] = true
			}
		}
		if !exists || entry.Revision != r.Revision || !slices.Equal(entry.Neighbors, neighbors) {
			entry.Revision, entry.Neighbors = r.Revision, neighbors
			w.cache.Entries[id] = entry
			w.dirty = true
		}
	}
	if complete {
		for id, entry := range w.cache.Entries {
			if _, exists := g.records[id]; !exists {
				roots[id] = true
				for _, target := range entry.Neighbors {
					roots[target] = true
				}
				delete(w.cache.Entries, id)
				w.dirty = true
			}
		}
	}
	frontier := roots
	seen := map[string]bool{}
	for depth := 0; depth <= 5; depth++ {
		next := map[string]bool{}
		for id := range frontier {
			if seen[id] {
				continue
			}
			seen[id] = true
			if entry, exists := w.cache.Entries[id]; exists && !entry.Pending {
				entry.Pending = true
				w.cache.Entries[id] = entry
				w.dirty = true
			}
			for _, target := range oldEdges[id] {
				if !seen[target] {
					next[target] = true
				}
			}
			for target := range g.edges[id] {
				if !seen[target] {
					next[target] = true
				}
			}
		}
		frontier = next
	}
	changed := false
	ids := []string{}
	for id := range g.records {
		entry := w.cache.Entries[id]
		if entry.Pending || entry.CalculatedAt.IsZero() || now.Sub(entry.CalculatedAt) >= graphWeightTTL {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool {
		id, other := ids[i], ids[j]
		if immediate[id] != immediate[other] {
			return immediate[id]
		}
		a, b := w.cache.Entries[id], w.cache.Entries[other]
		if a.Pending != b.Pending {
			return a.Pending
		}
		if !a.CalculatedAt.Equal(b.CalculatedAt) {
			return a.CalculatedAt.Before(b.CalculatedAt)
		}
		// Never-calculated ties favour well-connected records so the graph becomes useful quickly.
		if a.CalculatedAt.IsZero() && len(g.edges[id]) != len(g.edges[other]) {
			return len(g.edges[id]) > len(g.edges[other])
		}
		return id < other
	})
	for _, id := range ids {
		if ctx.Err() != nil {
			break
		}
		w.mu.Unlock()
		score := g.score(id)
		w.mu.Lock()
		entry := w.cache.Entries[id]
		entry.Weight = score
		entry.CalculatedAt = now
		entry.Pending = false
		w.cache.Entries[id] = entry
		w.dirty = true
		changed = true
	}
	w.initialized = true
	return changed
}

func (w *graphWeights) snapshot() (map[string]graphWeightEntry, map[string]any) {
	w.mu.Lock()
	defer w.mu.Unlock()
	entries := map[string]graphWeightEntry{}
	ready, pending := 0, 0
	for id, entry := range w.cache.Entries {
		entries[id] = entry
		if !entry.CalculatedAt.IsZero() {
			ready++
		}
		if entry.Pending || entry.CalculatedAt.IsZero() || time.Since(entry.CalculatedAt) >= graphWeightTTL {
			pending++
		}
	}
	status := ""
	if pending > 0 {
		status = fmt.Sprintf("Updating sizes · %d calculated · %d queued", ready, pending)
	}
	if w.issue != "" {
		status = w.issue
	}
	return entries, map[string]any{"calculated": ready, "pending": pending, "status": status}
}
