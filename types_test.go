package songya

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestTypeDefaultsAndPersistWithConflictsAndRetries(t *testing.T) {
	s, _ := Open(t.TempDir())
	ctx := context.Background()
	ari, err := s.TypeConfiguration(ctx)
	if err != nil || len(ari.Kinds[Entities]) != 12 || len(ari.Kinds[Memories]) != 0 {
		t.Fatal(ari, err)
	}
	ari.Kinds[Entities] = append(ari.Kinds[Entities], TypeDefinition{ID: "ingredient", Name: "Ingredient", Description: "An ingredient used for cooking."})
	ari.Kinds[Memories] = []TypeDefinition{{ID: "recipe", Name: "Recipe", Description: "Ingredients and instructions for making a dish."}}
	receipt, err := s.SaveTypeConfiguration(ctx, ari, ari.Version, nil, "save-ari")
	if err != nil || receipt.Status != "committed" {
		t.Fatal(receipt, err)
	}
	reopened, _ := Open(s.Directory())
	got, err := reopened.TypeConfiguration(ctx)
	if err != nil || len(got.Kinds[Entities]) != 13 {
		t.Fatal(got, err)
	}
	_, err = s.SaveTypeConfiguration(ctx, ari, ari.Version, nil, "stale")
	if !errors.Is(err, ErrConflict) {
		t.Fatal("stale settings overwrote a change", err)
	}
	receipt, err = s.SaveTypeConfiguration(ctx, ari, ari.Version, nil, "save-ari")
	if err != nil || receipt.Status != "committed" {
		t.Fatal("accepted settings retry was lost", receipt, err)
	}
}
func TestUnknownTypesCannotBypassEngineWrites(t *testing.T) {
	s, _ := Open(t.TempDir())
	ctx := context.Background()
	for _, fields := range []map[string]any{{"name": "Bad", "type": "made_up"}} {
		if _, err := s.Create(Entities, fields, "Intact words"); err == nil {
			t.Fatal("invalid type accepted", fields)
		}
	}
	doc, err := s.Create(Entities, map[string]any{"name": "Clara", "type": "person"}, "An intact description.")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Update(doc.ID, doc.Revision, map[string]any{"type": "bad"}, doc.Body); err == nil {
		t.Fatal("Update bypassed validation")
	}
	if _, err = s.SaveSource(ctx, "source-bypass", doc.ID, Entities, doc.Revision, "---\nname: Clara\ntype: bad\n---\nIntact description."); err == nil {
		t.Fatal("Source bypassed validation")
	}
	if _, err = s.SaveMemory(ctx, MemoryInput{Text: textValue("A recipe."), Type: textValue("recipe")}); err == nil {
		t.Fatal("unconfigured memory type accepted")
	}
	after, _ := s.Read(doc.ID)
	if after.Revision != doc.Revision {
		t.Fatal("rejected writes changed saved data")
	}
}
func TestRemovingUsedTypesRequiresExplicitReassignmentAndPreservesWords(t *testing.T) {
	s, _ := Open(t.TempDir())
	ctx := context.Background()
	doc, err := s.Create(Entities, map[string]any{"name": "Clara", "type": "person"}, "Friend and collaborator.")
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := s.TypeConfiguration(ctx)
	kept := []TypeDefinition{}
	for _, def := range cfg.Kinds[Entities] {
		if def.ID != "person" {
			kept = append(kept, def)
		}
	}
	cfg.Kinds[Entities] = kept
	if _, err = s.SaveTypeConfiguration(ctx, cfg, cfg.Version, nil, "no-reassignment"); err == nil {
		t.Fatal("used type silently removed")
	}
	receipt, err := s.SaveTypeConfiguration(ctx, cfg, cfg.Version, map[Destination]map[string]string{Entities: {"person": "organization"}}, "reassign")
	if err != nil || receipt.Status != "committed" {
		t.Fatal(receipt, err)
	}
	after, _ := s.Read(doc.ID)
	if after.Body != doc.Body || documentType(after) != "organization" {
		t.Fatal(after)
	}
	// A completed retry must not undo later explicit work.
	_, err = s.SaveNamed(ctx, Entities, NamedInput{ID: doc.ID, Type: textValue("object")})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err = s.SaveTypeConfiguration(ctx, cfg, cfg.Version, map[Destination]map[string]string{Entities: {"person": "organization"}}, "reassign")
	if err != nil {
		t.Fatal(receipt, err)
	}
	after, _ = s.Read(doc.ID)
	if documentType(after) != "object" {
		t.Fatal("terminal retry republished old assignments")
	}
}
func TestDamagedTypesDoNotBlockUnrelatedEdits(t *testing.T) {
	s, _ := Open(t.TempDir())
	ctx := context.Background()
	doc, err := s.Create(Entities, map[string]any{"name": "Person", "type": "person"}, "Original words")
	if err != nil {
		t.Fatal(err)
	}
	if err := fixtureWriteFile(filepath.Join(s.Directory(), typeConfigurationPath()), []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TypeConfiguration(ctx); err == nil {
		t.Fatal("damaged settings silently replaced")
	}
	after, err := s.Update(doc.ID, doc.Revision, map[string]any{"name": "Corrected name"}, doc.Body)
	if err != nil || after.Body != doc.Body {
		t.Fatal(after, err)
	}
}

func TestTypeSettingsAndReassignmentRecoverTogetherAfterInterruption(t *testing.T) {
	s, _ := Open(t.TempDir())
	ctx := context.Background()
	doc, err := s.Create(Entities, map[string]any{"name": "Clara", "type": "person"}, "Unchanged description.")
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := s.TypeConfiguration(ctx)
	kept := []TypeDefinition{}
	for _, def := range cfg.Kinds[Entities] {
		if def.ID != "person" {
			kept = append(kept, def)
		}
	}
	cfg.Kinds[Entities] = kept
	request := typeConfigurationRequest(cfg, cfg.Version, map[Destination]map[string]string{Entities: {"person": "organization"}}, "interrupted-settings")
	digest, err := requestRevision(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(s.Directory())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	journal, err := prepareBatch(ctx, root, request, digest)
	if err != nil {
		t.Fatal(err)
	}
	persistJournal(t, root, journal)
	for _, entry := range journal.Entries {
		if err := fixtureRootMkdir(root, pendingName(entry.Source), 0700); err != nil {
			t.Fatal(err)
		}
		if err := syncNew(root, filepath.Join(pendingName(entry.Source), "batch"), []byte(filepath.Base(batchDirectory(request.Key)))); err != nil {
			t.Fatal(err)
		}
		if entry.ID == typeConfigurationID() {
			if err := fixtureRootWriteFile(root, entry.Source, entry.After, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	reopened, err := Open(s.Directory())
	if err != nil {
		t.Fatal(err)
	}
	after, err := reopened.Read(doc.ID)
	if err != nil || after.Body != doc.Body || documentType(after) != "organization" {
		t.Fatal(after, err)
	}
	actual, err := reopened.TypeConfiguration(ctx)
	if err != nil || len(actual.Kinds[Entities]) != 11 {
		t.Fatal(actual, err)
	}
	if !marker(root, filepath.Join(batchDirectory(request.Key), "committed")) {
		t.Fatal("recovery did not commit")
	}
}

func TestTypeIDWinsOverAnotherDisplayName(t *testing.T) {
	s, _ := Open(t.TempDir())
	ctx := context.Background()
	cfg, _ := s.TypeConfiguration(ctx)
	cfg.Kinds[Entities] = []TypeDefinition{{ID: "food", Name: "Dish", Description: "A prepared dish."}, {ID: "ingredient", Name: "Food", Description: "An ingredient."}}
	if _, err := s.SaveTypeConfiguration(ctx, cfg, cfg.Version, nil, "name-overlap"); err != nil {
		t.Fatal(err)
	}
	doc, err := s.Create(Entities, map[string]any{"name": "Soup", "type": "food"}, "A dish.")
	if err != nil || documentType(doc) != "food" {
		t.Fatal(doc, err)
	}
}

func TestSettingsEditsPreserveUnrecognizedAndDamagedAssignments(t *testing.T) {
	s, _ := Open(t.TempDir())
	ctx := context.Background()
	doc, err := s.Create(Entities, map[string]any{"name": "Clara", "type": "person"}, "Intact words.")
	if err != nil {
		t.Fatal(err)
	}
	name, _ := recordName(doc.ID)
	raw := []byte("---\nname: Clara\ntype: person\ncustom: {unparsed: keep this}\n---\nIntact words.\n")
	if err := fixtureWriteFile(filepath.Join(s.Directory(), name), raw, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, _ := s.TypeConfiguration(ctx)
	kept := []TypeDefinition{}
	for _, def := range cfg.Kinds[Entities] {
		if def.ID != "person" {
			kept = append(kept, def)
		}
	}
	cfg.Kinds[Entities] = kept
	if _, err := s.SaveTypeConfiguration(ctx, cfg, cfg.Version, map[Destination]map[string]string{Entities: {"person": "organization"}}, "preserve-damage"); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Read(doc.ID)
	var fields map[string]any
	if err := after.Frontmatter.Decode(&fields); err != nil {
		t.Fatal(err)
	}
	if documentType(after) != "organization" || !reflect.DeepEqual(fields["custom"], map[string]any{"unparsed": "keep this"}) || after.Body != "Intact words.\n" {
		t.Fatal(fields, after.Body)
	}
	// An already unrecognized assignment must not block a definition-only edit.
	if err := fixtureWriteFile(filepath.Join(s.Directory(), name), []byte("---\nname: Clara\ntype: old_unknown\n---\nIntact words.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, _ = s.TypeConfiguration(ctx)
	cfg.Kinds[Entities][0].Name = "Renamed choice"
	if _, err := s.SaveTypeConfiguration(ctx, cfg, cfg.Version, nil, "rename-with-unknown"); err != nil {
		t.Fatal(err)
	}
	after, _ = s.Read(doc.ID)
	if documentType(after) != "old_unknown" {
		t.Fatal("unrecognized metadata discarded")
	}
}

func TestTypeSettingsLockedReadsKeepInterruptedBatchPriorView(t *testing.T) {
	s, _ := Open(t.TempDir())
	ctx := context.Background()
	doc, err := s.Create(Entities, map[string]any{"name": "Clara", "type": "person"}, "Earlier words.")
	if err != nil {
		t.Fatal(err)
	}
	request := BatchRequest{Key: "settings-locked-read", Changes: []Change{{ID: doc.ID, Revision: doc.Revision, Body: textValue("Accepted new words.")}}}
	digest, err := requestRevision(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(s.Directory())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	journal, err := prepareBatch(ctx, root, request, digest)
	if err != nil {
		t.Fatal(err)
	}
	persistJournal(t, root, journal)
	entry := journal.Entries[0]
	if err := fixtureRootMkdir(root, pendingName(entry.Source), 0700); err != nil {
		t.Fatal(err)
	}
	if err := syncNew(root, filepath.Join(pendingName(entry.Source), "batch"), []byte(filepath.Base(batchDirectory(request.Key)))); err != nil {
		t.Fatal(err)
	}
	if err := fixtureRootWriteFile(root, entry.Source, entry.After, 0600); err != nil {
		t.Fatal(err)
	}
	err = s.lockedContext(ctx, func(locked *os.Root) error {
		read, err := readLockedCopy(locked, doc.ID, entry.Source)
		if err != nil || read.Body != doc.Body || documentType(read) != "person" {
			t.Fatalf("partial publication became visible: %+v %v", read, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Recover(); err != nil {
		t.Fatal(err)
	}
	after, err := s.Read(doc.ID)
	if err != nil || after.Body != "Accepted new words." {
		t.Fatal(after, err)
	}
}
