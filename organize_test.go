package songya

import (
	"context"
	"strings"
	"testing"
)

func TestMergeAndSplitPreserveOriginalsAndRetryIdentity(t *testing.T) {
	ctx := context.Background()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := createRecord(t, store, Memories, map[string]any{"title": "First"}, "First exact narrative")
	b := createRecord(t, store, Memories, map[string]any{"title": "Second"}, "Second exact narrative")
	referencing := createRecord(t, store, Memories, map[string]any{"relations": []Relation{{Type: "motivated_by", Target: a.ID}}}, "Reference")
	request := MergeRequest{Key: "merge", Sources: []Version{{a.ID, a.Revision}, {b.ID, b.Revision}}}
	merged, err := store.Merge(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := store.Read(merged.Refs["merged"])
	if err != nil || !strings.Contains(canonical.Body, a.Body) || !strings.Contains(canonical.Body, b.Body) {
		t.Fatal("merge lost a narrative")
	}
	old, err := store.Read(a.ID)
	if err != nil || old.Body != a.Body {
		t.Fatal("merge rewrote its original")
	}
	retry, err := store.Merge(ctx, request)
	if err != nil || retry.Refs["merged"] != canonical.ID {
		t.Fatalf("merge retry duplicated or failed: %+v, %v", retry, err)
	}
	incoming, err := store.Graph(ctx, GraphQuery{ID: canonical.ID, Direction: "incoming"})
	if err != nil || len(incoming.Edges) != 1 || incoming.Edges[0].Source != referencing.ID {
		t.Fatal("merge lost incoming references")
	}
	splitRequest := SplitRequest{Key: "split", Source: Version{canonical.ID, canonical.Revision}, Parts: []Part{{Fields: map[string]any{"title": "Part one"}, Body: a.Body}, {Fields: map[string]any{"title": "Part two"}, Body: b.Body}}}
	split, err := store.Split(ctx, splitRequest)
	if err != nil {
		t.Fatal(err)
	}
	resolution, err := store.Resolve(ctx, a.ID)
	if err != nil || !resolution.Ambiguous || len(resolution.Targets) != 2 {
		t.Fatal("split lost incoming ambiguity")
	}
	retried, err := store.Split(ctx, splitRequest)
	if err != nil || retried.Refs["part_1"] != split.Refs["part_1"] {
		t.Fatalf("split retry changed identity: %v", err)
	}
	original, err := store.Read(canonical.ID)
	if err != nil || original.Body != canonical.Body {
		t.Fatal("split discarded its original")
	}
}
func TestPreparationReturnsAmbiguityWithoutChoosingAnIdentity(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"John Brown", "John Smith"} {
		createRecord(t, store, Entities, map[string]any{"name": name, "aliases": []string{"John"}}, "A different person")
	}
	prepared, err := store.Prepare(context.Background(), "I had coffee with John", 10)
	if err != nil || prepared.Key == "" || len(prepared.Matches) != 2 || len(prepared.Ambiguities) != 1 || len(prepared.Ambiguities[0].Candidates) != 2 {
		t.Fatalf("ambiguity was hidden: %+v, %v", prepared, err)
	}
}

func TestMergeKeepsRelationshipQualifiers(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := createRecord(t, store, Entities, nil, "Identity")
	a := createRecord(t, store, Memories, map[string]any{"relations": []any{map[string]any{"type": "possibly_with", "target": target.ID, "certainty": "uncertain", "detail": "Do not lose this qualifier"}}}, "First")
	b := createRecord(t, store, Memories, nil, "Second")
	receipt, err := store.Merge(context.Background(), MergeRequest{Key: "qualified-merge", Sources: []Version{{a.ID, a.Revision}, {b.ID, b.Revision}}})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := store.Read(receipt.Refs["merged"])
	if err != nil {
		t.Fatal(err)
	}
	data, err := doc.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "certainty: uncertain") || !strings.Contains(string(data), "Do not lose this qualifier") {
		t.Fatal("merge flattened relationship qualifiers")
	}
}

func TestPreparationKeepsDuplicateSuggestionsRelevant(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Adam", "John"} {
		for range 2 {
			createRecord(t, store, Entities, map[string]any{"name": name}, "Distinct account")
		}
	}
	prepared, err := store.Prepare(context.Background(), "Coffee with John", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.PossibleDuplicates) != 1 || len(prepared.Ambiguities) != 1 || prepared.Ambiguities[0].Name != "John" {
		t.Fatalf("unrelated duplicates crowded preparation: %+v", prepared)
	}
}
