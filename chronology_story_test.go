package songya

import (
	"context"
	"encoding/base64"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestTimelineValidatesChainsWithoutMarkingCycleDescendants(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	start := createRecord(t, store, Memories, map[string]any{"time": map[string]any{"earliest": "2001-01-01"}}, "start")
	middle := createRecord(t, store, Memories, map[string]any{"relations": []Relation{{"after", start.ID}}}, "middle")
	a := createRecord(t, store, Memories, nil, "cycle A")
	b := createRecord(t, store, Memories, map[string]any{"relations": []Relation{{"after", a.ID}}}, "cycle B")
	descendant := createRecord(t, store, Memories, map[string]any{"relations": []Relation{{"after", a.ID}}}, "descendant")
	finish := createRecord(t, store, Memories, map[string]any{"time": map[string]any{"latest": "2000-01-01"}, "relations": []Relation{{"after", middle.ID}, {"after", descendant.ID}}}, "finish")
	if _, err := store.Update(a.ID, a.Revision, map[string]any{"relations": []Relation{{"after", b.ID}}}, a.Body); err != nil {
		t.Fatal(err)
	}
	result, err := store.Timeline(ctx, Query{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	good := false
	for _, edge := range result.Constraints {
		switch {
		case edge.Before == start.ID && edge.After == middle.ID, edge.Before == middle.ID && edge.After == finish.ID:
			if edge.Usable {
				t.Fatal("transitive bound conflict accepted")
			}
		case edge.Before == a.ID && edge.After == b.ID, edge.Before == b.ID && edge.After == a.ID:
			if edge.Usable {
				t.Fatal("cycle edge accepted")
			}
		case edge.Before == descendant.ID && edge.After == finish.ID:
			good = edge.Usable
		}
	}
	if !good {
		t.Fatal("healthy ordering downstream of a cycle disabled")
	}
}

func TestTimelineManyDatedRecordsKeepsWorkAndResponseBounded(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Direct fixture files avoid hundreds of individual fsyncs. A small page must
	// not require constructing every pair of dates in the entire catalogue.
	for i := 0; i < 2000; i++ {
		id := fmt.Sprintf("m_%012d", i)
		date := time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i).Format(time.DateOnly)
		data := fmt.Sprintf("---\ntime:\n  earliest: %s\n  latest: %s\n---\nEpisode", date, date)
		if err := fixtureWriteFile(filepath.Join(store.path, "memories", id+"/record.md"), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := store.Timeline(ctx, Query{Limit: 8})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Records) != 8 || len(result.Constraints) != 28 || !result.Truncated {
		t.Fatalf("unexpected page: %+v", result)
	}
	for _, record := range result.Records {
		if record.Unordered {
			t.Fatal("dated episode treated as unordered")
		}
	}
}

func TestStoryPagesPreserveUnicodeAndLargeOffsets(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	episode := createRecord(t, store, Memories, nil, "Καφέ ☕🙂 and music")
	story := createRecord(t, store, Stories, map[string]any{"relations": []Relation{{"related_to", episode.ID}}}, "始 終")
	want := "始 終\n\n" + episode.Body
	for size := 1; size <= 9; size++ {
		var got strings.Builder
		offset := 0
		for range 200 {
			page, err := store.Compose(ctx, story.ID, offset, size)
			if err != nil {
				t.Fatal(err)
			}
			if !utf8.ValidString(page.Text) {
				t.Fatalf("split UTF-8: %q", page.Text)
			}
			got.WriteString(page.Text)
			if !page.Truncated {
				break
			}
			if page.Next <= offset {
				t.Fatal("page did not advance")
			}
			offset = page.Next
		}
		if got.String() != want {
			t.Fatalf("size %d changed text: %q", size, got.String())
		}
	}
	page, err := store.Compose(ctx, story.ID, int(^uint(0)>>1), 100)
	if err != nil || page.Text != "" || page.Truncated || page.Offset != len(want) {
		t.Fatalf("large offset: %+v %v", page, err)
	}
}

func TestStoryBinaryFragmentsRemainRecoverable(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	body := string([]byte{128, 255, 0, 65})
	story := createRecord(t, store, Stories, nil, body)
	page, err := store.Compose(context.Background(), story.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(page.Base64)
	if err != nil || string(raw) != body || page.Text != "" {
		t.Fatal("binary story bytes lost")
	}
}

func TestInvalidCalendarBoundsDoNotDisableIndependentRelativeOrder(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := createRecord(t, store, Memories, nil, "Known later")
	source := createRecord(t, store, Memories, map[string]any{"time": map[string]any{"earliest": "2001-01-01", "latest": "2000-01-01"}}, "Dates need correction; relative order is known")
	_, err = store.Update(target.ID, target.Revision, map[string]any{"relations": []Relation{{"after", source.ID}}}, target.Body)
	if err != nil {
		t.Fatal(err)
	}
	timeline, err := store.Timeline(context.Background(), Query{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(timeline.Diagnostics) == 0 {
		t.Fatal("damaged dates were not reported")
	}
	usable := false
	for _, edge := range timeline.Constraints {
		if edge.Before == source.ID && edge.After == target.ID {
			usable = edge.Usable
		}
	}
	if !usable {
		t.Fatal("one damaged calendar field disabled independent, consistent relative ordering")
	}
}

func TestStoryCanPageLargeSharedCompositionsWithoutReplayingTheirPrefix(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	node := createRecord(t, store, Memories, nil, "X")
	for range 20 {
		node = createRecord(t, store, Stories, map[string]any{"relations": []Relation{{"related_to", node.ID}, {"related_to", node.ID}}}, "")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	page, err := store.Compose(ctx, node.ID, 700000, 64)
	if err != nil || page.Text != strings.Repeat("X\n\n", 700100/3+1)[700000:700064] || !page.Truncated || page.Next != 700064 {
		t.Fatalf("large canonical composition could not be paged: %+v %v", page, err)
	}
}

func TestVirtualStoryOffsetsDoNotOverflow(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	node := createRecord(t, store, Memories, nil, "X")
	for range 12 {
		node = createRecord(t, store, Stories, map[string]any{"relations": repeatedRelations(node.ID, 100)}, "")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err = store.Compose(ctx, node.ID, int(^uint(0)>>1)-10, 100)
	if err == nil || !strings.Contains(err.Error(), "byte range") {
		t.Fatalf("unaddressable virtual offset was not reported safely: %v", err)
	}
}

func TestStoryBudgetAlsoBoundsMissingReferences(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Missing targets still count toward the traversal budget. The structured YAML
	// fixture needs time to decode under the race detector before traversal starts.
	story := createRecord(t, store, Stories, map[string]any{"relations": repeatedRelations("m_000000000000", 100001)}, "")
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	_, err = store.Compose(ctx, story.ID, int(^uint(0)>>1), 100)
	if err == nil || !strings.Contains(err.Error(), "traversal budget") {
		t.Fatalf("missing references bypassed the work budget: %v", err)
	}
}

func repeatedRelations(id string, count int) []Relation {
	relations := make([]Relation, count)
	for i := range relations {
		relations[i] = Relation{"related_to", id}
	}
	return relations
}
