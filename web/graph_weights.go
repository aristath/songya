package web

import (
	"sort"
	"strings"

	"github.com/aristath/songya"
)

var graphHopWeights = [...]float64{1, .5, .4, .3, .2, .1}

type weightGraph struct {
	records map[string]songya.Record
	edges   map[string]map[string]bool
	words   map[string]int
}

func newWeightGraph(catalog *songya.Catalog) *weightGraph {
	g := &weightGraph{map[string]songya.Record{}, map[string]map[string]bool{}, map[string]int{}}
	for _, record := range catalog.Records {
		if !songya.ValidID(record.ID) {
			continue
		}
		g.records[record.ID] = record
		g.edges[record.ID] = map[string]bool{}
		g.words[record.ID] = len(strings.Fields(record.Body))
	}
	for _, record := range g.records {
		for _, edge := range record.Relations {
			switch edge.Type {
			case "related_to", "caused_by", "part_of":
				if record.ID != edge.Target && g.edges[edge.Target] != nil {
					g.edges[record.ID][edge.Target] = true
					g.edges[edge.Target][record.ID] = true
				}
			}
		}
	}
	return g
}

func (g *weightGraph) neighbors(id string) []string {
	result := make([]string, 0, len(g.edges[id]))
	for target := range g.edges[id] {
		result = append(result, target)
	}
	sort.Strings(result)
	return result
}

// Breadth-first traversal counts each body once at its shortest distance.
func (g *weightGraph) score(id string) float64 {
	frontier := []string{id}
	seen := map[string]bool{id: true}
	score := 0.0
	for _, multiplier := range graphHopWeights {
		words := 0
		next := []string{}
		for _, from := range frontier {
			words += g.words[from]
			for target := range g.edges[from] {
				if !seen[target] {
					seen[target] = true
					next = append(next, target)
				}
			}
		}
		// Sum integer word counts first: map iteration cannot change rounding.
		score += float64(words) * multiplier
		frontier = next
		if len(frontier) == 0 {
			break
		}
	}
	return score
}
