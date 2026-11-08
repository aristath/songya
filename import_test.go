package songya

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestImportPreservesOriginalCoverageAndRetryIdentity(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sourceRoot := t.TempDir()
	source := []byte("First episode.\n\nSecond episode.\n")
	if err := fixtureWriteFile(filepath.Join(sourceRoot, "old.md"), source, 0600); err != nil {
		t.Fatal(err)
	}
	preview, err := store.PreviewImports(context.Background(), sourceRoot, 0, 10)
	if err != nil || len(preview.Files) != 1 {
		t.Fatal(err)
	}
	request := ImportRequest{User: "test", Path: "old.md", Revision: preview.Files[0].Revision, Parts: []ImportPart{{Start: 0, End: 14}, {Start: 16, End: len(source)}}}
	imported, err := store.Import(context.Background(), sourceRoot, request)
	if err != nil || !imported.CoverageComplete || len(imported.Parts) != 2 {
		t.Fatalf("import failed: %+v, %v", imported, err)
	}
	actual, err := os.ReadFile(filepath.Join(sourceRoot, "old.md"))
	if err != nil || string(actual) != string(source) {
		t.Fatal("import rewrote its source")
	}
	archive, err := os.ReadFile(filepath.Join(store.path, ".imports", imported.Archive+".md"))
	if err != nil || string(archive) != string(source) {
		t.Fatal("verbatim original was not retained")
	}
	if err := os.Remove(filepath.Join(sourceRoot, "old.md")); err != nil {
		t.Fatal(err)
	}
	retry, err := store.Import(context.Background(), sourceRoot, request)
	if err != nil || retry.Parts[0].ID != imported.Parts[0].ID {
		t.Fatalf("retry lost its mapping: %+v, %v", retry, err)
	}
	catalog, err := store.Scan(context.Background())
	if err != nil || len(catalog.Records) != 2 {
		t.Fatal("import retry duplicated active records")
	}
}
func TestPartialImportReportsUncoveredSource(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sourceRoot := t.TempDir()
	source := []byte("Keep one. Keep two.")
	if err := fixtureWriteFile(filepath.Join(sourceRoot, "old.md"), source, 0600); err != nil {
		t.Fatal(err)
	}
	mapping, err := store.Import(context.Background(), sourceRoot, ImportRequest{User: "test", Path: "old.md", Revision: dataRevision(source), Parts: []ImportPart{{Start: 0, End: 9}}})
	if err != nil || mapping.CoverageComplete || len(mapping.Uncovered) == 0 || len(mapping.Diagnostics) == 0 {
		t.Fatal("coverage loss was hidden")
	}
}

func TestImportCannotReadActiveStoreThroughDirectoryAlias(t *testing.T) {
	parent := t.TempDir()
	path := filepath.Join(parent, "store")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	doc := createRecord(t, store, Memories, nil, "Already active")
	if err := os.Symlink(path, filepath.Join(parent, "alias")); err != nil {
		t.Fatal(err)
	}
	_, _, err = store.ImportSource(parent, filepath.Join("alias", "memories", doc.ID+"/record.md"))
	if err == nil {
		t.Fatal("active source was importable through a directory alias")
	}
}
