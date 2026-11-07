package songya

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestTypeDefaultsAndIndependentUserDefinitions(t *testing.T) {
	s, _ := Open(t.TempDir())
	ctx := context.Background()
	ari, err := s.TypeConfiguration(ctx, "ari")
	if err != nil || len(ari.Kinds[Entities]) != 12 || len(ari.Kinds[Memories]) != 0 {
		t.Fatal(ari, err)
	}
	bob, err := s.TypeConfiguration(ctx, "bob")
	if err != nil {
		t.Fatal(err)
	}
	ari.Kinds[Entities] = append(ari.Kinds[Entities], TypeDefinition{"ingredient", "Ingredient", "An ingredient used for cooking."})
	ari.Kinds[Memories] = []TypeDefinition{{"recipe", "Recipe", "Ingredients and instructions for making a dish."}}
	receipt, err := s.SaveTypeConfiguration(ctx, ari, ari.Version, nil, "save-ari")
	if err != nil || receipt.Status != "committed" {
		t.Fatal(receipt, err)
	}
	got, _ := s.TypeConfiguration(ctx, "bob")
	if !reflect.DeepEqual(got.Kinds, bob.Kinds) {
		t.Fatal("another user's choices changed")
	}
	reopened, _ := Open(s.Directory())
	got, err = reopened.TypeConfiguration(ctx, "ari")
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
func TestSharedEntityTypesAreScopedAndValidated(t *testing.T) {
	s, _ := Open(t.TempDir())
	ctx := context.Background()
	doc, err := s.Create(Entities, map[string]any{"name": "Apple", "types": map[string]string{"ari": "plant", "bob": "organization"}}, "A shared name with separate classifications.")
	if err != nil {
		t.Fatal(err)
	}
	if doc.Metadata().User != "" || doc.TypeFor("ari") != "plant" || doc.TypeFor("bob") != "organization" {
		t.Fatal(doc)
	}
	cfg, _ := s.TypeConfiguration(ctx, "ari")
	cfg.Kinds[Entities] = append(cfg.Kinds[Entities], TypeDefinition{"ingredient", "Ingredient", "An ingredient used for cooking."})
	if _, err = s.SaveTypeConfiguration(ctx, cfg, cfg.Version, nil, "ingredient"); err != nil {
		t.Fatal(err)
	}
	receipt, err := s.SaveNamed(WithTypeUser(ctx, "ari"), Entities, NamedInput{ID: doc.ID, Type: textValue("Ingredient")})
	if err != nil || receipt.Status != "committed" {
		t.Fatal(receipt, err)
	}
	after, _ := s.Read(doc.ID)
	if after.Body != doc.Body || after.TypeFor("ari") != "ingredient" || after.TypeFor("bob") != "organization" {
		t.Fatal("classification overwrote identity or another user's assignment", after)
	}
	if _, err = s.SaveNamed(WithTypeUser(ctx, "bob"), Entities, NamedInput{ID: doc.ID, Type: textValue("ingredient")}); err == nil {
		t.Fatal("another user's type list was used")
	}
	if _, err = s.ApplyBatch(WithTypeUser(ctx, "ari"), BatchRequest{Key: "cross-user", Changes: []Change{{ID: doc.ID, Fields: map[string]any{"types": map[string]string{"ari": "ingredient", "bob": "object"}}}}}); err == nil {
		t.Fatal("selected user overwrote someone else's type")
	}
	records, err := s.Search(ctx, Query{User: "ari", Text: "Apple"})
	if err != nil || records.Matches[0].Type != "ingredient" {
		t.Fatal(records, err)
	}
	if _, err = s.SaveNamed(WithTypeUser(ctx, "ari"), Entities, NamedInput{ID: doc.ID, Type: textValue("")}); err != nil {
		t.Fatal(err)
	}
	after, _ = s.Read(doc.ID)
	if after.TypeFor("ari") != "" || after.TypeFor("bob") != "organization" {
		t.Fatal("clearing one user's type erased another", after)
	}
}
func TestUnknownTypesCannotBypassEngineWrites(t *testing.T) {
	s, _ := Open(t.TempDir())
	ctx := context.Background()
	for _, fields := range []map[string]any{{"name": "Bad", "type": "person"}, {"name": "Bad", "types": map[string]any{"ari": "made_up"}}, {"name": "Bad", "types": map[string]any{"all": "person"}}} {
		if _, err := s.Create(Entities, fields, "Intact words"); err == nil {
			t.Fatal("invalid type accepted", fields)
		}
	}
	doc, err := s.Create(Entities, map[string]any{"name": "Clara", "types": map[string]string{"ari": "person"}}, "An intact description.")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Update(doc.ID, doc.Revision, map[string]any{"types": map[string]string{"ari": "bad"}}, doc.Body); err == nil {
		t.Fatal("Update bypassed validation")
	}
	if _, err = s.SaveSource(ctx, "source-bypass", doc.ID, Entities, doc.Revision, "---\nname: Clara\ntypes: {ari: bad}\n---\nIntact description."); err == nil {
		t.Fatal("Source bypassed validation")
	}
	if _, err = s.SaveMemory(ctx, MemoryInput{User: "ari", Text: textValue("A recipe."), Type: textValue("recipe")}); err == nil {
		t.Fatal("unconfigured memory type accepted")
	}
	after, _ := s.Read(doc.ID)
	if after.Revision != doc.Revision {
		t.Fatal("rejected writes changed saved data")
	}
}
func TestRemovingUsedTypesRequiresExplicitReassignmentAndPreservesOtherUsers(t *testing.T) {
	s, _ := Open(t.TempDir())
	ctx := context.Background()
	doc, err := s.Create(Entities, map[string]any{"name": "Clara", "types": map[string]string{"ari": "person", "bob": "software"}}, "Friend and collaborator.")
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := s.TypeConfiguration(ctx, "ari")
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
	if after.Body != doc.Body || after.TypeFor("ari") != "organization" || after.TypeFor("bob") != "software" {
		t.Fatal(after)
	}
	// A completed retry must not undo later explicit work.
	_, err = s.SaveNamed(WithTypeUser(ctx, "ari"), Entities, NamedInput{ID: doc.ID, Type: textValue("object")})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err = s.SaveTypeConfiguration(ctx, cfg, cfg.Version, map[Destination]map[string]string{Entities: {"person": "organization"}}, "reassign")
	if err != nil {
		t.Fatal(receipt, err)
	}
	after, _ = s.Read(doc.ID)
	if after.TypeFor("ari") != "object" {
		t.Fatal("terminal retry republished old assignments")
	}
}
func TestDamagedUserTypesDoNotBlockHealthyRecordsOrAnotherUser(t *testing.T) {
	s, _ := Open(t.TempDir())
	ctx := context.Background()
	doc, err := s.Create(Entities, map[string]any{"name": "A person", "types": map[string]string{"ari": "person"}}, "Original words.")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(s.Directory(), typeConfigurationPath("ari")), []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = s.TypeConfiguration(ctx, "ari"); err == nil {
		t.Fatal("damaged settings were silently replaced")
	}
	if _, err = s.TypeConfiguration(ctx, "bob"); err != nil {
		t.Fatal("another user's settings were blocked", err)
	}
	updated, err := s.Update(doc.ID, doc.Revision, map[string]any{"name": "Corrected name"}, doc.Body)
	if err != nil || updated.Body != doc.Body {
		t.Fatal("damaged types blocked an unrelated edit", err)
	}
	if _, err = s.SaveNamed(WithTypeUser(ctx, "bob"), Entities, NamedInput{ID: doc.ID, Type: textValue("person")}); err != nil {
		t.Fatal("damaged ari settings blocked bob", err)
	}
}

func TestTypeSettingsAndReassignmentRecoverTogetherAfterInterruption(t *testing.T) {
	s, _ := Open(t.TempDir())
	ctx := context.Background()
	doc, err := s.Create(Entities, map[string]any{"name": "Clara", "types": map[string]string{"ari": "person", "bob": "software"}}, "Unchanged description.")
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := s.TypeConfiguration(ctx, "ari")
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
		if err := root.Mkdir(pendingName(entry.Source), 0700); err != nil {
			t.Fatal(err)
		}
		if err := syncNew(root, filepath.Join(pendingName(entry.Source), "batch"), []byte(filepath.Base(batchDirectory(request.Key)))); err != nil {
			t.Fatal(err)
		}
		if entry.ID == typeConfigurationID("ari") {
			if err := root.WriteFile(entry.Source, entry.After, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	reopened, err := Open(s.Directory())
	if err != nil {
		t.Fatal(err)
	}
	after, err := reopened.Read(doc.ID)
	if err != nil || after.Body != doc.Body || after.TypeFor("ari") != "organization" || after.TypeFor("bob") != "software" {
		t.Fatal(after, err)
	}
	actual, err := reopened.TypeConfiguration(ctx, "ari")
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
	cfg, _ := s.TypeConfiguration(ctx, "cook")
	cfg.Kinds[Entities] = []TypeDefinition{{"food", "Dish", "A prepared dish."}, {"ingredient", "Food", "An ingredient."}}
	if _, err := s.SaveTypeConfiguration(ctx, cfg, cfg.Version, nil, "name-overlap"); err != nil {
		t.Fatal(err)
	}
	doc, err := s.Create(Entities, map[string]any{"name": "Soup", "types": map[string]string{"cook": "food"}}, "A dish.")
	if err != nil || doc.TypeFor("cook") != "food" {
		t.Fatal(doc, err)
	}
}

func TestSettingsEditsPreserveUnrecognizedAndDamagedAssignments(t *testing.T) {
	s, _ := Open(t.TempDir())
	ctx := context.Background()
	doc, err := s.Create(Entities, map[string]any{"name": "Clara", "types": map[string]string{"ari": "person"}}, "Intact words.")
	if err != nil {
		t.Fatal(err)
	}
	name, _ := recordName(doc.ID)
	raw := []byte("---\nname: Clara\ntypes:\n  ari: person\n  bob: {unparsed: keep this}\n---\nIntact words.\n")
	if err := os.WriteFile(filepath.Join(s.Directory(), name), raw, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, _ := s.TypeConfiguration(ctx, "ari")
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
	assignments := fields["types"].(map[string]any)
	if after.TypeFor("ari") != "organization" || !reflect.DeepEqual(assignments["bob"], map[string]any{"unparsed": "keep this"}) || after.Body != "Intact words.\n" {
		t.Fatal(fields, after.Body)
	}
	// An already unrecognized assignment must not block a definition-only edit.
	if err := os.WriteFile(filepath.Join(s.Directory(), name), []byte("---\nname: Clara\ntypes: {ari: old_unknown}\n---\nIntact words.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, _ = s.TypeConfiguration(ctx, "ari")
	cfg.Kinds[Entities][0].Name = "Renamed choice"
	if _, err := s.SaveTypeConfiguration(ctx, cfg, cfg.Version, nil, "rename-with-unknown"); err != nil {
		t.Fatal(err)
	}
	after, _ = s.Read(doc.ID)
	if after.TypeFor("ari") != "old_unknown" {
		t.Fatal("unrecognized metadata discarded")
	}
}

func TestTypeSettingsLockedReadsKeepInterruptedBatchPriorView(t *testing.T) {
	s, _ := Open(t.TempDir())
	ctx := context.Background()
	doc, err := s.Create(Entities, map[string]any{"name": "Clara", "types": map[string]string{"ari": "person"}}, "Earlier words.")
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
	if err := root.Mkdir(pendingName(entry.Source), 0700); err != nil {
		t.Fatal(err)
	}
	if err := syncNew(root, filepath.Join(pendingName(entry.Source), "batch"), []byte(filepath.Base(batchDirectory(request.Key)))); err != nil {
		t.Fatal(err)
	}
	if err := root.WriteFile(entry.Source, entry.After, 0600); err != nil {
		t.Fatal(err)
	}
	err = s.lockedContext(ctx, func(locked *os.Root) error {
		read, err := readLockedCopy(locked, doc.ID, entry.Source)
		if err != nil || read.Body != doc.Body || read.TypeFor("ari") != "person" {
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
