package songya

import (
	"context"
	"strings"
	"testing"
)

func TestStoryOrderComesOnlyFromRelations(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first := createRecord(t, store, Memories, nil, "First event.")
	second := createRecord(t, store, Memories, nil, "Second event.")
	entity := createRecord(t, store, Entities, map[string]any{"name": "Alex"}, "An identifying description.")
	story := createRecord(t, store, Stories, map[string]any{"relations": []Relation{
		{Type: "related_to", Target: second.ID},
		{Type: "related_to", Target: entity.ID},
		{Type: "related_to", Target: first.ID},
	}}, "An ordered story.")
	page, err := store.Compose(context.Background(), story.ID, 0, 4096)
	if err != nil || page.Text != "An ordered story.\n\nSecond event.\n\nFirst event." {
		t.Fatalf("membership or narrative order lost: %+v, %v", page, err)
	}
	if strings.Contains(page.Text, entity.Body) {
		t.Fatal("a participant became a narrative member")
	}
	// Ordinary authored prose does not become another authoritative link source.
	text := "A literal marker [[" + second.ID + "]]"
	plain := createRecord(t, store, Stories, nil, text)
	composed, err := store.Compose(context.Background(), plain.ID, 0, 4096)
	if err != nil || composed.Text != text {
		t.Fatalf("prose was interpreted as membership: %+v, %v", composed, err)
	}
}

func TestCausalOrderAndDerivedContainment(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cause := createRecord(t, store, Memories, nil, "The job ended.")
	whole := createRecord(t, store, Memories, nil, "Moving house.")
	effect := createRecord(t, store, Memories, map[string]any{"relations": []Relation{
		{Type: "caused_by", Target: cause.ID},
		{Type: "part_of", Target: whole.ID},
	}}, "Signing the moving contract.")
	timeline, err := store.Timeline(context.Background(), Query{Kind: Memories})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, edge := range timeline.Constraints {
		found = found || edge.Before == cause.ID && edge.After == effect.ID && edge.Usable
	}
	if !found {
		t.Fatalf("causation did not supply chronology: %+v", timeline)
	}
	if len(timeline.Groups[whole.ID]) != 1 || timeline.Groups[whole.ID][0] != effect.ID {
		t.Fatal("event group missing", timeline.Groups)
	}
	graph, err := store.Graph(context.Background(), GraphQuery{ID: whole.ID, Direction: "incoming", Type: "contains"})
	if err != nil || len(graph.Edges) != 1 || graph.Edges[0].Type != "part_of" || graph.Edges[0].ReverseType != "contains" {
		t.Fatalf("containment backlink missing: %+v, %v", graph, err)
	}
	saved, err := store.Read(effect.ID)
	if err != nil || len(saved.Metadata().Relations) != 2 {
		t.Fatal("derived relationships were written back into the record", err)
	}
	for _, relation := range saved.Metadata().Relations {
		if relation.Type == "after" {
			t.Fatal("causation required a redundant persisted after edge")
		}
	}
}

func TestInlineIdentityEnrichmentKeepsExistingConnections(t *testing.T) {
	store, _ := Open(t.TempDir())
	first := createRecord(t, store, Topics, map[string]any{"name": "First"}, "")
	second := createRecord(t, store, Topics, map[string]any{"name": "Second"}, "")
	entity := createRecord(t, store, Entities, map[string]any{"name": "Alex", "relations": []Relation{{"related_to", first.ID}}}, "A musician")
	receipt, err := store.SaveMemory(context.Background(), MemoryInput{Text: textValue("An afternoon with Alex"), Relations: []RelationInput{{Type: "related_to", Target: map[string]any{"id": entity.ID, "name": "Alex", "relations": []any{map[string]any{"type": "related_to", "target": second.ID}}}}}})
	if err != nil || receipt.Status != "committed" {
		t.Fatal(receipt, err)
	}
	doc, _ := store.Read(entity.ID)
	if len(doc.Metadata().Relations) != 2 || doc.Body != "A musician" {
		t.Fatal("inline connection enrichment lost existing data", doc.Metadata())
	}
}

func TestConnectionsCanRepairMalformedLinksAndNormalizeSpelling(t *testing.T) {
	doc := Parse([]byte("---\nrelations:\n - {type: Related-To, target: m_000000000000}\n - {type: old_custom_label, target: m_missing}\ncustom: Keep this\n---\nIntact prose"))
	if err := doc.EditConnection(Relation{Type: "old_custom_label", Target: "m_missing"}, true); err != nil {
		t.Fatal(err)
	}
	if err := doc.EditConnection(Relation{Type: "related_to", Target: "m_000000000000"}, true); err != nil {
		t.Fatal(err)
	}
	if len(doc.Metadata().Relations) != 0 || doc.Body != "Intact prose" || metadataField(&doc.Frontmatter, "custom") == nil {
		t.Fatal("repair lost unrelated data")
	}
}
