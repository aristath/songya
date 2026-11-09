package web

import (
	"math"
	"sort"
	"time"

	"github.com/aristath/songya"
)

var graphHopWeights = [...]float64{0, .5, .4, .3, .2, .1}

type weightGraph struct {
	records map[string]songya.Record
	edges   map[string]map[string]float64
	recency map[string]float64
}

func newWeightGraph(catalog *songya.Catalog, now time.Time) *weightGraph {
	g := &weightGraph{map[string]songya.Record{}, map[string]map[string]float64{}, map[string]float64{}}
	for _, r := range catalog.Records {
		if !songya.ValidID(r.ID) {
			continue
		}
		g.records[r.ID] = r
		g.edges[r.ID] = map[string]float64{}
		if r.Kind == songya.Memories {
			g.recency[r.ID] = memoryRecency(r.Time, now)
		}
	}
	connect := func(from, to string, weight float64) {
		if from != to && g.edges[from] != nil && g.edges[to] != nil {
			g.edges[from][to] = math.Max(g.edges[from][to], weight)
		}
	}
	for _, r := range g.records {
		for _, edge := range r.Relations {
			var forward, reverse float64
			switch edge.Type {
			case "related_to":
				forward, reverse = 1, 1
			case "caused_by":
				forward, reverse = 1, .75
			case "part_of":
				forward, reverse = .8, .5
			default:
				continue // Ordering, identity guesses and redirects do not establish influence.
			}
			connect(r.ID, edge.Target, forward)
			connect(edge.Target, r.ID, reverse)
		}
	}
	return g
}

func memoryRecency(t songya.RecordTime, now time.Time) float64 {
	now = now.UTC().Truncate(24 * time.Hour)
	parse := func(s *string) (time.Time, bool) {
		if s == nil {
			return time.Time{}, false
		}
		d, err := time.Parse(time.DateOnly, *s)
		return d, err == nil
	}
	decay := math.Ln2 / (5 * 365.2425)
	// Unix day differences also handle intervals wider than time.Duration's limit.
	days := func(a, b time.Time) float64 { return float64(a.Unix()-b.Unix()) / 86400 }
	value := func(d time.Time) float64 { return .25 + .75*math.Exp(-decay*math.Max(0, days(now, d))) }
	if d, ok := parse(t.Date); ok {
		return value(d)
	}
	lo, lok := parse(t.Earliest)
	hi, hik := parse(t.Latest)
	if !lok || !hik || hi.Before(lo) {
		return .5
	}
	if hi.Equal(lo) {
		return value(lo)
	}
	// Average across the interval; future dates never receive a bonus.
	end := hi
	if end.After(now) {
		end = now
	}
	integral := 0.0
	if end.After(lo) {
		span := days(end, lo)
		integral = .25*span + .75*(math.Exp(-decay*days(now, end))-math.Exp(-decay*days(now, lo)))/decay
	}
	if hi.After(now) {
		start := lo
		if start.Before(now) {
			start = now
		}
		integral += days(hi, start)
	}
	return integral / (days(hi, lo))
}

func (g *weightGraph) neighbors(id string) []string {
	result := make([]string, 0, len(g.edges[id]))
	for target := range g.edges[id] {
		result = append(result, target)
	}
	sort.Strings(result)
	return result
}

func (g *weightGraph) score(id string) float64 {
	frontier := map[string]float64{id: 1}
	best := map[string]float64{}
	for hops := 1; hops < len(graphHopWeights); hops++ {
		next := map[string]float64{}
		// Sorted iteration keeps floating-point results repeatable across runs.
		for _, from := range sortedWeightIDs(frontier) {
			strength := frontier[from]
			if from != id {
				strength /= math.Sqrt(float64(max(1, len(g.edges[from]))))
			}
			for to, edge := range g.edges[from] {
				if to != id {
					next[to] = math.Max(next[to], strength*edge)
				}
			}
		}
		frontier = next
		for target, strength := range frontier {
			if recency, ok := g.recency[target]; ok {
				best[target] = math.Max(best[target], strength*graphHopWeights[hops]*recency)
			}
		}
	}
	sum := 0.0
	for _, target := range sortedWeightIDs(best) {
		sum += best[target]
	}
	return sum
}

func sortedWeightIDs[T any](values map[string]T) []string {
	ids := make([]string, 0, len(values))
	for id := range values {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
