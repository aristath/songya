package web

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/aristath/songya"
)

func weightRecord(kind songya.Destination, n int, links ...songya.Relation) songya.Record {
	prefix := map[songya.Destination]string{songya.Entities: "e", songya.Memories: "m", songya.Topics: "t", songya.Stories: "s"}[kind]
	body := ""
	if kind == songya.Memories {
		body = "memory"
	}
	return songya.Record{Body: body, ID: fmt.Sprintf("%s_%012d", prefix, n), Kind: kind, Revision: fmt.Sprint(n), Relations: links}
}
func near(t *testing.T, actual, expected float64) {
	t.Helper()
	if math.Abs(actual-expected) > 1e-9 {
		t.Fatalf("got %.12f, want %.12f", actual, expected)
	}
}

func TestGraphWeightContentAssociationsExcludeOrderingAndIdentityGuesses(t *testing.T) {
	for _, test := range []struct {
		kind    string
		reverse bool
		want    float64
	}{
		{"related_to", false, .5}, {"related_to", true, .5},
		{"caused_by", false, .5}, {"caused_by", true, .5},
		{"part_of", false, .5}, {"part_of", true, .5},
		{"after", false, 0}, {"after", true, 0}, {"possibly_same_as", false, 0},
	} {
		t.Run(fmt.Sprint(test.kind, test.reverse), func(t *testing.T) {
			entity := weightRecord(songya.Entities, 1)
			memory := weightRecord(songya.Memories, 1)
			if test.reverse {
				memory.Relations = []songya.Relation{{Type: test.kind, Target: entity.ID}}
			} else {
				entity.Relations = []songya.Relation{{Type: test.kind, Target: memory.ID}}
			}
			g := newWeightGraph(&songya.Catalog{Records: []songya.Record{entity, memory}})
			near(t, g.score(entity.ID), test.want)
		})
	}
}

func TestGraphWeightHopLimitsDeduplicationAndSharedContent(t *testing.T) {
	for hops := 1; hops <= 6; hops++ {
		records := []songya.Record{weightRecord(songya.Memories, 1)}
		target := records[0].ID
		for i := hops; i >= 1; i-- {
			r := weightRecord(songya.Entities, i, songya.Relation{Type: "related_to", Target: target})
			records = append(records, r)
			target = r.ID
		}
		g := newWeightGraph(&songya.Catalog{Records: records})
		expected := 0.0
		if hops <= 5 {
			expected = graphHopWeights[hops] / math.Pow(2, float64(hops-1))
		}
		near(t, g.score(target), expected)
	}
	root := weightRecord(songya.Entities, 1)
	bridge := weightRecord(songya.Entities, 2)
	memory := weightRecord(songya.Memories, 1)
	root.Relations = []songya.Relation{{Type: "related_to", Target: bridge.ID}}
	bridge.Relations = []songya.Relation{{Type: "related_to", Target: memory.ID}}
	records := []songya.Record{root, bridge, memory}
	small := newWeightGraph(&songya.Catalog{Records: records}).score(root.ID)
	for i := 3; i <= 102; i++ {
		records = append(records, weightRecord(songya.Entities, i, songya.Relation{Type: "related_to", Target: bridge.ID}))
	}
	large := newWeightGraph(&songya.Catalog{Records: records}).score(root.ID)
	near(t, small, .4/2)
	near(t, large, .4/102)
	// Reciprocal/duplicate edges and loops must not count the same memory again.
	records[0].Relations = append(records[0].Relations, songya.Relation{Type: "related_to", Target: memory.ID}, songya.Relation{Type: "related_to", Target: memory.ID})
	records[2].Relations = []songya.Relation{{Type: "related_to", Target: root.ID}}
	g := newWeightGraph(&songya.Catalog{Records: records})
	near(t, g.score(root.ID), .5/2)
	for i := 0; i < 20; i++ {
		if g.score(root.ID) != .5/2 {
			t.Fatal("Non-deterministic score")
		}
	}
}

func TestGraphWeightIgnoresDates(t *testing.T) {
	str := func(s string) *string { return &s }
	for name, timing := range map[string]songya.RecordTime{
		"undated":    {},
		"old":        {Date: str("1900-01-01")},
		"future":     {Date: str("2200-01-01")},
		"range":      {Earliest: str("1980-01-01"), Latest: str("2000-12-31")},
		"open range": {Earliest: str("1980-01-01")},
		"existence":  {Start: str("1980-01-01"), End: str("2000-12-31")},
	} {
		t.Run(name, func(t *testing.T) {
			memory := weightRecord(songya.Memories, 1)
			memory.Time = timing
			entity := weightRecord(songya.Entities, 1, songya.Relation{Type: "related_to", Target: memory.ID})
			g := newWeightGraph(&songya.Catalog{Records: []songya.Record{entity, memory}})
			near(t, g.score(entity.ID), .5)
		})
	}
}

func TestGraphWeightCountsOwnBodyAndEveryConnectedKind(t *testing.T) {
	root := weightRecord(songya.Entities, 1)
	root.Body = "One two"
	records := []songya.Record{root}
	for i, kind := range []songya.Destination{songya.Entities, songya.Topics, songya.Stories, songya.Memories} {
		record := weightRecord(kind, i+2, songya.Relation{Type: "related_to", Target: root.ID})
		record.Body = "Three recorded words"
		records = append(records, record)
	}
	near(t, newWeightGraph(&songya.Catalog{Records: records}).score(root.ID), 2+.5*12)
	// Metadata and absent content add no words, even when the entry has links.
	records[0].Title = "A long title that is not the saved account"
	records[0].Body = " \n\t "
	near(t, newWeightGraph(&songya.Catalog{Records: records}).score(root.ID), .5*12)
	records[0].Body = "Μια\tσταθερή\nανάμνηση"
	near(t, newWeightGraph(&songya.Catalog{Records: records}).score(root.ID), 3+.5*12)
}

func TestGraphWeightUsesShortestPathAndRecordOrderingDoesNotChangeIt(t *testing.T) {
	root := weightRecord(songya.Entities, 1)
	bridge := weightRecord(songya.Topics, 1, songya.Relation{Type: "related_to", Target: root.ID})
	memory := weightRecord(songya.Memories, 1, songya.Relation{Type: "related_to", Target: root.ID}, songya.Relation{Type: "related_to", Target: bridge.ID})
	memory.Body = "Four words counted once"
	records := []songya.Record{root, bridge, memory}
	near(t, newWeightGraph(&songya.Catalog{Records: records}).score(root.ID), 1)
	records[0], records[2] = records[2], records[0]
	near(t, newWeightGraph(&songya.Catalog{Records: records}).score(root.ID), 1)
}

func TestGraphWeightSharesConnectedContentInsteadOfCopyingIt(t *testing.T) {
	source := weightRecord(songya.Memories, 1)
	source.Body = strings.Repeat("word ", 100)
	john := weightRecord(songya.Entities, 1, songya.Relation{Type: "related_to", Target: source.ID})
	mary := weightRecord(songya.Entities, 2, songya.Relation{Type: "related_to", Target: source.ID})
	g := newWeightGraph(&songya.Catalog{Records: []songya.Record{source, john, mary}})
	near(t, g.score(source.ID), 100)
	near(t, g.score(john.ID), 25)
	near(t, g.score(mary.ID), 25)
}

func TestGraphWeightAddsIndependentShortestPathShares(t *testing.T) {
	root := weightRecord(songya.Entities, 1)
	first := weightRecord(songya.Topics, 1, songya.Relation{Type: "related_to", Target: root.ID})
	second := weightRecord(songya.Topics, 2, songya.Relation{Type: "related_to", Target: root.ID})
	source := weightRecord(songya.Memories, 1, songya.Relation{Type: "related_to", Target: first.ID}, songya.Relation{Type: "related_to", Target: second.ID})
	source.Body = strings.Repeat("word ", 100)
	g := newWeightGraph(&songya.Catalog{Records: []songya.Record{root, first, second, source}})
	near(t, g.score(root.ID), 20)
}
