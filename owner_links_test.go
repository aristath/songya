package songya

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestMyEntityCleansBothDirectionsAndPreservesWords(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(t.TempDir())
	person := createRecord(t, s, Entities, map[string]any{"name": "John"}, "Identity words")
	place := createRecord(t, s, Entities, map[string]any{"name": "Home"}, "A place")
	memory := createRecord(t, s, Memories, map[string]any{"relations": []Relation{{"related_to", person.ID}, {"related_to", place.ID}}}, "Original event")
	person, err := s.Update(person.ID, person.Revision, map[string]any{"relations": []Relation{{"related_to", memory.ID}, {"related_to", place.ID}}}, person.Body)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := s.TypeConfiguration(ctx)
	cfg.MyEntity = person.ID
	if _, err := s.SaveTypeConfiguration(ctx, cfg, cfg.Version, nil, "identity"); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Read(memory.ID)
	identity, _ := s.Read(person.ID)
	if after.Body != memory.Body || identity.Body != person.Body || !reflect.DeepEqual(after.Metadata().Relations, []Relation{{"related_to", place.ID}}) || !reflect.DeepEqual(identity.Metadata().Relations, []Relation{{"related_to", place.ID}}) {
		t.Fatal(after, identity)
	}
	next, err := s.Create(Memories, map[string]any{"relations": []Relation{{"related_to", person.ID}, {"related_to", place.ID}}}, "Another event")
	if err != nil || !reflect.DeepEqual(next.Metadata().Relations, []Relation{{"related_to", place.ID}}) {
		t.Fatal(next, err)
	}
}

func TestMyEntitySelectionRecoversAsOneDurableBatch(t *testing.T) {
	s, _ := Open(t.TempDir())
	person := createRecord(t, s, Entities, map[string]any{"name": "Mary"}, "Identity")
	memory := createRecord(t, s, Memories, map[string]any{"user": "mary", "relations": []Relation{{"related_to", person.ID}}}, "Original event")
	cfg, _ := s.TypeConfiguration(context.Background())
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
	loaded, _ := reopened.TypeConfiguration(context.Background())
	if loaded.MyEntity != person.ID || len(after.Metadata().Relations) != 0 || after.Body != memory.Body {
		t.Fatal(loaded, after)
	}
	if _, err := reopened.SaveTypeConfiguration(context.Background(), cfg, cfg.Version, nil, "recover-identity"); err != nil {
		t.Fatal(err)
	}
	// Damaged identity settings leave relationships intact instead of guessing.
	if err := os.WriteFile(filepath.Join(s.path, typeConfigurationPath()), []byte(`{"my_entity": 12}`), 0600); err != nil {
		t.Fatal(err)
	}
	doc, err := reopened.Create(Memories, map[string]any{"user": "mary", "relations": []Relation{{"related_to", person.ID}}}, "Still usable")
	if err != nil || len(doc.Metadata().Relations) != 1 {
		t.Fatal(doc, err)
	}
}
