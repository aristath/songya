package songya

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func testUserFields(kind Destination, fields map[string]any) map[string]any {
	if !userOwned(kind) {
		return fields
	}
	fields = copyFields(fields)
	if _, supplied := fields["user"]; !supplied {
		fields["user"] = "test"
	}
	return fields
}

func createRecord(t *testing.T, s *Store, kind Destination, fields map[string]any, body string) *Document {
	t.Helper()
	d, err := s.Create(kind, testUserFields(kind, fields), body)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestNamedDescriptionsUseTheBody(t *testing.T) {
	for _, kind := range []Destination{Entities, Topics} {
		store, err := Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		doc := createRecord(t, store, kind, map[string]any{"name": "Alex", "description": "obsolete metadata"}, "Current description")
		if doc.Record(kind).Description != doc.Body {
			t.Fatal("description did not use the body")
		}
		if _, err := store.Scan(context.Background()); err != nil {
			t.Fatal(err)
		}
		description := "Updated identifying prose"
		if receipt, err := store.SaveNamed(context.Background(), kind, NamedInput{ID: doc.ID, Description: &description}); err != nil || receipt.Status != "committed" {
			t.Fatal(receipt, err)
		}
		found, err := store.Search(context.Background(), Query{Text: "identifying"})
		if err != nil || len(found.Matches) != 1 || found.Matches[0].Excerpt != description {
			t.Fatal("description change did not refresh search", found, err)
		}
		stale, err := store.Search(context.Background(), Query{Text: "obsolete"})
		if err != nil || len(stale.Matches) != 0 {
			t.Fatal("search used an obsolete metadata description", stale, err)
		}
	}
}
func TestCatalogRebuildsAndPreservesIndependentSources(t *testing.T) {
	ctx := context.Background()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	doc := createRecord(t, store, Entities, map[string]any{"name": "John Smith", "aliases": []string{"Johnny"}, "description": "A jazz friend"}, "Raw body \xff")
	matches, err := store.Search(ctx, Query{Text: "Johnny"})
	if err != nil || len(matches.Matches) != 1 || matches.Matches[0].ID != doc.ID {
		t.Fatalf("aliases unavailable: %+v, %v", matches, err)
	}
	for _, damage := range []string{"delete", "corrupt", "directory"} {
		cache := filepath.Join(store.path, ".index", "catalog.json")
		if err := os.RemoveAll(cache); err != nil {
			t.Fatal(err)
		}
		if damage == "corrupt" {
			if err := fixtureWriteFile(cache, []byte("Broken derived data"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if damage == "directory" {
			if err := os.Mkdir(cache, 0700); err != nil {
				t.Fatal(err)
			}
		}
		catalog, err := store.Scan(ctx)
		if err != nil || len(catalog.Records) != 1 || catalog.Records[0].Body != "Raw body \xff" {
			t.Fatalf("cache damage hid or changed source: %+v, %v", catalog, err)
		}
	}
	name, _ := recordName(doc.ID)
	data, err := os.ReadFile(filepath.Join(store.path, name))
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.Replace(data, []byte("John Smith"), []byte("John Brown"), 1)
	if err := fixtureWriteFile(filepath.Join(store.path, name), data, 0600); err != nil {
		t.Fatal(err)
	}
	matches, err = store.Search(ctx, Query{Text: "John Brown"})
	if err != nil || len(matches.Matches) != 1 || matches.Matches[0].Revision == doc.Revision {
		t.Fatal("external edit stayed stale")
	}
	if err := fixtureWriteFile(filepath.Join(store.path, "memories", "damaged/record.md"), []byte("---\nbroken: [\n---\nReadable orphan body"), 0600); err != nil {
		t.Fatal(err)
	}
	catalog, err := store.Scan(ctx)
	if err != nil || len(catalog.Records) != 2 || len(catalog.Diagnostics) == 0 {
		t.Fatal("damaged filename disabled discovery")
	}
	raw, err := store.ReadSource("memories/damaged/record.md")
	if err != nil || !bytes.Contains(raw, []byte("Readable orphan body")) {
		t.Fatal("orphan source became unavailable")
	}
	if _, err := store.ReadSource("../outside.md"); err == nil {
		t.Fatal("source access escaped the store")
	}
}

func TestGraphAndRedirectsPreserveAmbiguity(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	a := createRecord(t, store, Memories, map[string]any{"title": "A"}, "A body")
	b := createRecord(t, store, Memories, map[string]any{"title": "B"}, "B body")
	old := createRecord(t, store, Memories, map[string]any{"redirects": []string{a.ID, b.ID}}, "Original remains")
	from := createRecord(t, store, Memories, map[string]any{"relations": []Relation{{Type: "related_to", Target: old.ID}, {Type: "after", Target: a.ID}}}, "From")
	incoming, err := store.Graph(ctx, GraphQuery{ID: a.ID, Direction: "incoming"})
	if err != nil || len(incoming.Edges) != 1 || incoming.Edges[0].Source != from.ID {
		t.Fatalf("wrong backlinks: %+v, %v", incoming, err)
	}
	graph, err := store.Graph(ctx, GraphQuery{ID: from.ID, Direction: "outgoing"})
	if err != nil || len(graph.Edges) != 2 {
		t.Fatal(err)
	}
	for _, edge := range graph.Edges {
		if edge.Type == "related_to" && (edge.Target != old.ID || edge.Resolved) {
			t.Fatal("split was silently assigned to one replacement")
		}
	}
	resolved, err := store.Resolve(ctx, old.ID)
	if err != nil || !resolved.Ambiguous || len(resolved.Targets) != 2 {
		t.Fatalf("redirect lost a replacement: %+v", resolved)
	}
}

func TestTimelineKeepsUnknownOrderAndDetectsConstraints(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	unknown := createRecord(t, store, Memories, map[string]any{"title": "Unknown"}, "No known episode date")
	later := createRecord(t, store, Memories, map[string]any{"time": map[string]any{"earliest": "2006-12-01", "latest": "2006-12-31"}}, "Later")
	first := createRecord(t, store, Memories, map[string]any{"relations": []Relation{{Type: "after", Target: later.ID}}, "time": map[string]any{"earliest": "2007-01-01"}}, "Conflicting")
	if _, err := store.Update(later.ID, later.Revision, map[string]any{"relations": []Relation{{Type: "after", Target: first.ID}}}, later.Body); err != nil {
		t.Fatal(err)
	}
	timeline, err := store.Timeline(ctx, Query{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range timeline.Records {
		if r.ID == unknown.ID {
			found = true
			if !r.Unordered || r.Time.Earliest != nil {
				t.Fatal("recording time was invented as episode time")
			}
		}
	}
	if !found || len(timeline.Diagnostics) == 0 {
		t.Fatal("unknown record or chronology problems missing")
	}
	usableCycle := 0
	for _, edge := range timeline.Constraints {
		if edge.Usable && (edge.Before == first.ID && edge.After == later.ID || edge.Before == later.ID && edge.After == first.ID) {
			usableCycle++
		}
	}
	if usableCycle != 0 {
		t.Fatal("cycle was accepted as chronology")
	}
}

func TestStoriesComposeReferencesWithoutCopyingNarratives(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	memory := createRecord(t, store, Memories, nil, "Canonical episode")
	a := createRecord(t, store, Stories, map[string]any{"relations": []Relation{{"related_to", memory.ID}}}, "A\nEnd")
	b := createRecord(t, store, Stories, map[string]any{"relations": []Relation{{"related_to", memory.ID}}}, "B")
	for _, story := range []*Document{a, b} {
		page, err := store.Compose(ctx, story.ID, 0, 16384)
		if err != nil || !strings.Contains(page.Text, "Canonical episode") {
			t.Fatalf("composition unavailable: %+v, %v", page, err)
		}
	}
	assembled := ""
	offset := 0
	for range 20 {
		page, err := store.Compose(ctx, a.ID, offset, 5)
		if err != nil {
			t.Fatal(err)
		}
		assembled += page.Text
		if !page.Truncated {
			break
		}
		offset = page.Next
	}
	if assembled != "A\nEnd\n\nCanonical episode" {
		t.Fatalf("paging changed composition: %q", assembled)
	}
	if _, err := store.Update(a.ID, a.Revision, map[string]any{"relations": []Relation{{"related_to", a.ID}}}, "A"); err != nil {
		t.Fatal(err)
	}
	cycle, err := store.Compose(ctx, a.ID, 0, 100)
	if err != nil || len(cycle.Diagnostics) == 0 {
		t.Fatal("story recursion was not isolated")
	}
	entries, err := os.ReadDir(filepath.Join(store.path, "memories"))
	if err != nil || len(entries) != 1 {
		t.Fatal("story rendering duplicated a memory")
	}
}

func TestCanonicalStoryReferencesParticipateInBacklinks(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	episode := createRecord(t, store, Memories, nil, "Canonical account")
	first := createRecord(t, store, Stories, map[string]any{"relations": []Relation{{"related_to", episode.ID}}}, "")
	second := createRecord(t, store, Stories, map[string]any{"relations": []Relation{{"related_to", episode.ID}}}, "Another telling")
	graph, err := store.Graph(ctx, GraphQuery{ID: episode.ID, Direction: "incoming", Type: "related_to"})
	if err != nil || len(graph.Edges) != 2 {
		t.Fatalf("story ownership was absent from backlinks: %+v %v", graph, err)
	}
	got := map[string]bool{}
	for _, edge := range graph.Edges {
		got[edge.Source] = true
	}
	if !got[first.ID] || !got[second.ID] {
		t.Fatal("story references were not canonical backlinks")
	}
	linked, err := store.Search(ctx, Query{Kind: Stories, RelatedTo: episode.ID, RelationType: "related_to"})
	if err != nil || len(linked.Matches) != 2 {
		t.Fatal("stories could not be retrieved through their canonical memories")
	}
}

func TestIndexLossPreservesRetrievalGraphChronologyAndComposition(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	entity := createRecord(t, store, Entities, map[string]any{"name": "John", "aliases": []string{"Johnny"}}, "A jazz friend")
	first := createRecord(t, store, Memories, map[string]any{"title": "Cafe", "relations": []Relation{{"related_to", entity.ID}}, "time": map[string]any{"earliest": "2000-01-01", "latest": "2000-01-01"}}, "First account")
	second := createRecord(t, store, Memories, map[string]any{"relations": []Relation{{Type: "after", Target: first.ID}}, "time": map[string]any{"earliest": "2001-01-01", "latest": "2001-01-01"}}, "Second account")
	story := createRecord(t, store, Stories, map[string]any{"relations": []Relation{{"related_to", first.ID}}}, "")
	for _, check := range []struct {
		query Query
		id    string
	}{
		{Query{Text: "Johnny"}, entity.ID},
		{Query{Text: "jazz", Kind: Entities}, entity.ID},
		{Query{Kind: Memories, RelatedTo: entity.ID, RelationType: "related_to"}, first.ID},
		{Query{Text: "2000-01-01"}, first.ID},
	} {
		found, err := store.Search(ctx, check.query)
		if err != nil {
			t.Fatal(err)
		}
		matched := false
		for _, match := range found.Matches {
			matched = matched || match.ID == check.id
		}
		if !matched {
			t.Fatalf("retrieval path unavailable: %+v %+v", check.query, found)
		}
	}
	before, err := store.Graph(ctx, GraphQuery{ID: first.ID, Direction: "incoming"})
	if err != nil || len(before.Edges) != 2 {
		t.Fatal("fixture backlinks unavailable")
	}
	beforeTime, err := store.Timeline(ctx, Query{Limit: 50})
	if err != nil || len(beforeTime.Constraints) != 1 {
		t.Fatal("fixture chronology unavailable")
	}
	if err := os.RemoveAll(filepath.Join(store.path, ".index")); err != nil {
		t.Fatal(err)
	}
	after, err := store.Graph(ctx, GraphQuery{ID: first.ID, Direction: "incoming"})
	if err != nil || !reflect.DeepEqual(before.Edges, after.Edges) {
		t.Fatal("index loss changed source-owned backlinks")
	}
	afterTime, err := store.Timeline(ctx, Query{Limit: 50})
	if err != nil || !reflect.DeepEqual(beforeTime.Constraints, afterTime.Constraints) || afterTime.Constraints[0].Before != first.ID || afterTime.Constraints[0].After != second.ID {
		t.Fatal("index loss changed partial chronology")
	}
	page, err := store.Compose(ctx, story.ID, 0, 100)
	if err != nil || page.Text != first.Body {
		t.Fatal("index loss changed canonical composition")
	}
	name, _ := recordName(entity.ID)
	if err := os.Remove(filepath.Join(store.path, name)); err != nil {
		t.Fatal(err)
	}
	found, err := store.Search(ctx, Query{Text: "Johnny"})
	if err != nil || len(found.Matches) != 0 {
		t.Fatal("removed source remained in the disposable index")
	}
}
