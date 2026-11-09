package web

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/aristath/songya"
)

func weightRecord(kind songya.Destination, n int, links ...songya.Relation) songya.Record {
	prefix := map[songya.Destination]string{songya.Entities: "e", songya.Memories: "m", songya.Topics: "t", songya.Stories: "s"}[kind]
	return songya.Record{ID: fmt.Sprintf("%s_%012d", prefix, n), Kind: kind, Revision: fmt.Sprint(n), Relations: links}
}
func datedWeightMemory(n int, date string) songya.Record {
	r := weightRecord(songya.Memories, n)
	r.Time.Date = &date
	return r
}
func near(t *testing.T, actual, expected float64) {
	t.Helper()
	if math.Abs(actual-expected) > 1e-9 {
		t.Fatalf("got %.12f, want %.12f", actual, expected)
	}
}

func TestGraphWeightRelationsHaveMeaningfulDirections(t *testing.T) {
	now := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		kind    string
		reverse bool
		want    float64
	}{
		{"related_to", false, .5}, {"related_to", true, .5},
		{"caused_by", false, .5}, {"caused_by", true, .375},
		{"part_of", false, .4}, {"part_of", true, .25},
		{"after", false, 0}, {"after", true, 0}, {"possibly_same_as", false, 0},
	} {
		t.Run(fmt.Sprint(test.kind, test.reverse), func(t *testing.T) {
			entity := weightRecord(songya.Entities, 1)
			memory := datedWeightMemory(1, "2026-11-09")
			if test.reverse {
				memory.Relations = []songya.Relation{{Type: test.kind, Target: entity.ID}}
			} else {
				entity.Relations = []songya.Relation{{Type: test.kind, Target: memory.ID}}
			}
			g := newWeightGraph(&songya.Catalog{Records: []songya.Record{entity, memory}}, now)
			near(t, g.score(entity.ID), test.want)
		})
	}
}

func TestGraphWeightHopLimitsDeduplicationAndHubDilution(t *testing.T) {
	now := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	for hops := 1; hops <= 6; hops++ {
		records := []songya.Record{datedWeightMemory(1, "2026-11-09")}
		target := records[0].ID
		for i := hops; i >= 1; i-- {
			r := weightRecord(songya.Entities, i, songya.Relation{Type: "related_to", Target: target})
			records = append(records, r)
			target = r.ID
		}
		g := newWeightGraph(&songya.Catalog{Records: records}, now)
		expected := 0.0
		if hops <= 5 {
			expected = graphHopWeights[hops] / math.Pow(math.Sqrt(2), float64(hops-1))
		}
		near(t, g.score(target), expected)
	}
	root := weightRecord(songya.Entities, 1)
	bridge := weightRecord(songya.Entities, 2)
	memory := datedWeightMemory(1, "2026-11-09")
	root.Relations = []songya.Relation{{Type: "related_to", Target: bridge.ID}}
	bridge.Relations = []songya.Relation{{Type: "related_to", Target: memory.ID}}
	records := []songya.Record{root, bridge, memory}
	small := newWeightGraph(&songya.Catalog{Records: records}, now).score(root.ID)
	for i := 3; i <= 102; i++ {
		records = append(records, weightRecord(songya.Entities, i, songya.Relation{Type: "related_to", Target: bridge.ID}))
	}
	large := newWeightGraph(&songya.Catalog{Records: records}, now).score(root.ID)
	near(t, small, .4/math.Sqrt(2))
	near(t, large, .4/math.Sqrt(102))
	// Reciprocal/duplicate edges and loops must not count the same memory again.
	records[0].Relations = append(records[0].Relations, songya.Relation{Type: "related_to", Target: memory.ID}, songya.Relation{Type: "related_to", Target: memory.ID})
	records[2].Relations = []songya.Relation{{Type: "related_to", Target: root.ID}}
	g := newWeightGraph(&songya.Catalog{Records: records}, now)
	near(t, g.score(root.ID), .5)
	for i := 0; i < 20; i++ {
		if g.score(root.ID) != .5 {
			t.Fatal("Non-deterministic score")
		}
	}
}

func TestGraphWeightRecencyUsesEventDatesAndAveragesRanges(t *testing.T) {
	now := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	str := func(s string) *string { return &s }
	near(t, memoryRecency(songya.RecordTime{}, now), .5)
	near(t, memoryRecency(songya.RecordTime{Earliest: str("2020-01-01")}, now), .5)
	near(t, memoryRecency(songya.RecordTime{Start: str("1980-01-01")}, now), .5)
	near(t, memoryRecency(songya.RecordTime{Date: str("2026-11-09")}, now), 1)
	near(t, memoryRecency(songya.RecordTime{Date: str("2030-01-01")}, now), 1)
	t0 := songya.RecordTime{Date: str("2000-01-01")}
	v := memoryRecency(t0, now)
	if v <= .25 || v >= .3 {
		t.Fatal("Old memories should keep a historical contribution", v)
	}
	lo, hi := "2020-01-01", "2021-01-01"
	average := memoryRecency(songya.RecordTime{Earliest: &lo, Latest: &hi}, now)
	if average <= memoryRecency(songya.RecordTime{Date: &lo}, now) || average >= memoryRecency(songya.RecordTime{Date: &hi}, now) {
		t.Fatal("Range not averaged", average)
	}
	near(t, memoryRecency(songya.RecordTime{Earliest: str("2030-01-01"), Latest: str("2031-01-01")}, now), 1)
	near(t, memoryRecency(songya.RecordTime{Earliest: &hi, Latest: &lo}, now), .5)
}

func TestGraphWeightWideDateRangesRemainBounded(t *testing.T) {
	lo, hi := "0001-01-01", "4001-01-01"
	v := memoryRecency(songya.RecordTime{Earliest: &lo, Latest: &hi}, time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC))
	if v < .6 || v > .65 {
		t.Fatal("Wide date range overflowed", v)
	}
}
