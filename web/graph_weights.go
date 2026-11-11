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

// Each record keeps its own words. Connected content is shared between the
// records referencing it, rather than broadcasting its full body to each one.
func (g *weightGraph) score(id string) float64 {
	frontier := map[string]float64{id: 1}
	seen := map[string]bool{id: true}
	score := 0.0
	for _, multiplier := range graphHopWeights {
		next := map[string]float64{}
		ids := make([]string, 0, len(frontier))
		for from := range frontier {
			ids = append(ids, from)
		}
		sort.Strings(ids)
		for _, from := range ids {
			share := frontier[from]
			score += float64(g.words[from]) * share * multiplier
			for _, target := range g.neighbors(from) {
				if !seen[target] {
					next[target] += share / float64(len(g.edges[target]))
				}
			}
		}
		for target := range next {
			seen[target] = true
		}
		frontier = next
		if len(frontier) == 0 {
			break
		}
	}
	return score
}
