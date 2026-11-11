package songya

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestMyEntityCleansBothDirectionsAndPreservesOtherRecords(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(t.TempDir())
	person := createRecord(t, s, Entities, map[string]any{"name": "John"}, "Identity words")
	place := createRecord(t, s, Entities, map[string]any{"name": "Home"}, "A place")
	fields := map[string]any{"user": "john", "relations": []Relation{{"related_to", person.ID}, {"related_to", place.ID}}, "original_accounts": []string{"My exact words."}, "time": map[string]any{"date": "2020-01-02"}}
	own := createRecord(t, s, Memories, fields, "One event, in full.")
	fields["user"] = "mary"
	other := createRecord(t, s, Memories, fields, "Mary's event.")
	story := createRecord(t, s, Stories, map[string]any{"user": "john", "relations": []Relation{{"related_to", person.ID}}}, "A story")
	person, _ = s.Update(person.ID, person.Revision, map[string]any{"relations": []Relation{{"related_to", own.ID}, {"related_to", other.ID}, {"related_to", story.ID}, {"related_to", place.ID}}}, person.Body)
	cfg, err := s.TypeConfiguration(ctx, "john")
	if err != nil {
		t.Fatal(err)
	}
	cfg.MyEntity = person.ID
	receipt, err := s.SaveTypeConfiguration(ctx, cfg, cfg.Version, nil, "select-my-entity")
	if err != nil || receipt.Status != "committed" {
		t.Fatal(receipt, err)
	}
	after, _ := s.Read(own.ID)
	if after.Body != own.Body || !reflect.DeepEqual(after.Metadata().Relations, []Relation{{"related_to", place.ID}}) {
		t.Fatal(after)
	}
	for _, field := range []string{"user", "time", "original_accounts", "created_at"} {
		if !sameFieldValue(metadataField(&own.Frontmatter, field), metadataField(&after.Frontmatter, field)) {
			t.Fatalf("changed %s", field)
		}
	}
	identity, _ := s.Read(person.ID)
	if identity.Body != person.Body || !reflect.DeepEqual(identity.Metadata().Relations, []Relation{{"related_to", other.ID}, {"related_to", story.ID}, {"related_to", place.ID}}) {
		t.Fatal(identity)
	}
	for _, before := range []*Document{other, story, place} {
		doc, _ := s.Read(before.ID)
		if doc.Revision != before.Revision {
			t.Fatalf("changed unrelated %s", before.ID)
		}
	}
	// The settings response was lost: the same request remains safe to retry.
	if _, err := s.SaveTypeConfiguration(ctx, cfg, cfg.Version, nil, "select-my-entity"); err != nil {
		t.Fatal(err)
	}
	loaded, _ := s.TypeConfiguration(ctx, "john")
	if loaded.MyEntity != person.ID {
		t.Fatal(loaded)
	}

	// New memories, direct updates, and source-editor saves share the rule.
	fields["user"] = "john"
	newMemory, err := s.Create(Memories, fields, "New event")
	if err != nil || len(newMemory.Metadata().Relations) != 1 {
		t.Fatal(newMemory, err)
	}
	identity, err = s.Update(identity.ID, identity.Revision, map[string]any{"relations": []Relation{{"related_to", own.ID}, {"related_to", other.ID}}}, identity.Body)
	if err != nil || !reflect.DeepEqual(identity.Metadata().Relations, []Relation{{"related_to", other.ID}}) {
		t.Fatal(identity, err)
	}
	source := fmt.Sprintf("---\nuser: john\nrelations:\n  - {type: related_to, target: %s}\n  - {type: related_to, target: %s}\n---\nCorrected words\n", person.ID, place.ID)
	if _, err := s.SaveSource(ctx, "source-owner-link", own.ID, Memories, after.Revision, source); err != nil {
		t.Fatal(err)
	}
	after, _ = s.Read(own.ID)
	if after.Body != "Corrected words\n" || len(after.Metadata().Relations) != 1 || after.Metadata().Relations[0].Target != place.ID {
		t.Fatal(after)
	}

	// Changing Mary to John also cleans an existing inverse identity link.
	if _, err := s.ApplyBatch(ctx, BatchRequest{Key: "change-owner", Changes: []Change{{ID: other.ID, Revision: other.Revision, Fields: map[string]any{"user": "john"}}}}); err != nil {
		t.Fatal(err)
	}
	identity, _ = s.Read(person.ID)
	if len(identity.Metadata().Relations) != 0 {
		t.Fatal("inverse owner link survived", identity.Metadata())
	}
	if _, err := s.ApplyBatch(ctx, BatchRequest{Key: "two-way-save", Changes: []Change{
		{ID: person.ID, AddRelations: []Relation{{"related_to", own.ID}}},
		{ID: own.ID, AddRelations: []Relation{{"related_to", person.ID}}, Body: textPointer("Updated event")},
	}}); err != nil {
		t.Fatal(err)
	}
	identity, _ = s.Read(person.ID)
	after, _ = s.Read(own.ID)
	if len(identity.Metadata().Relations) != 0 || len(after.Metadata().Relations) != 1 || after.Body != "Updated event" {
		t.Fatal("batch save restored redundant links", identity.Metadata(), after.Metadata())
	}
	// Creating a new, unrelated entity can still refer to an existing memory.
	newEntity, err := s.Create(Entities, map[string]any{"name": "Mary", "relations": []Relation{{"related_to", own.ID}}}, "A different person")
	if err != nil || len(newEntity.Metadata().Relations) != 1 {
		t.Fatal(newEntity, err)
	}
}

func TestMyEntitySelectionRecoversAsOneDurableBatch(t *testing.T) {
	s, _ := Open(t.TempDir())
	person := createRecord(t, s, Entities, map[string]any{"name": "Mary"}, "Identity")
	memory := createRecord(t, s, Memories, map[string]any{"user": "mary", "relations": []Relation{{"related_to", person.ID}}}, "Original event")
	cfg, _ := s.TypeConfiguration(context.Background(), "mary")
	cfg.MyEntity = person.ID
	request := typeConfigurationRequest(cfg, cfg.Version, nil, "recover-identity")
	digest, _ := requestRevision(context.Background(), request)
	root, _ := os.OpenRoot(s.path)
	journal, err := prepareBatch(context.Background(), root, request, digest)
	if err != nil {
		t.Fatal(err)
	}
	persistJournal(t, root, journal)
	root.Close()
	reopened, err := Open(s.path)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := reopened.Read(memory.ID)
	loaded, _ := reopened.TypeConfiguration(context.Background(), "mary")
	if loaded.MyEntity != person.ID || len(after.Metadata().Relations) != 0 || after.Body != memory.Body {
		t.Fatal(loaded, after)
	}
	if _, err := reopened.SaveTypeConfiguration(context.Background(), cfg, cfg.Version, nil, "recover-identity"); err != nil {
		t.Fatal(err)
	}
	// Damaged identity settings leave relationships intact instead of guessing.
	if err := os.WriteFile(filepath.Join(s.path, typeConfigurationPath("mary")), []byte(`{"my_entity": 12}`), 0600); err != nil {
		t.Fatal(err)
	}
	doc, err := reopened.Create(Memories, map[string]any{"user": "mary", "relations": []Relation{{"related_to", person.ID}}}, "Still usable")
	if err != nil || len(doc.Metadata().Relations) != 1 {
		t.Fatal(doc, err)
	}
}
