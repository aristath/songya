package songya

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gofrs/flock"
	"go.yaml.in/yaml/v3"
)

func TestHealthyMetadataSurvivesDamagedLeavesAndReread(t *testing.T) {
	path := t.TempDir()
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	cycle := map[string]any{"fact": "Keep cyclic neighbour"}
	cycle["self"] = cycle
	doc, err := store.Create(Memories, testUserFields(Memories, map[string]any{
		"title":   3,
		"details": map[string]any{"fact": "Keep fact", "bad": func() {}, "list": []any{"Keep item", make(chan int)}},
		"cycle":   cycle,
		"broken_node": &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: "fact"},
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: "Keep damaged neighbour"},
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: "missing value"},
		}},
	}), "Original body")
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Diagnostics()) != 4 {
		t.Fatalf("creation issues: %v", doc.Diagnostics())
	}
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	read, err := store.Read(doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := read.Frontmatter.Decode(&fields); err != nil {
		t.Fatal(err)
	}
	details := fields["details"].(map[string]any)
	if details["fact"] != "Keep fact" || details["list"].([]any)[0] != "Keep item" || fields["cycle"].(map[string]any)["fact"] != "Keep cyclic neighbour" {
		t.Fatalf("healthy neighbours were discarded: %v", fields)
	}
	if fields["broken_node"].(map[string]any)["fact"] != "Keep damaged neighbour" {
		t.Fatal("an unfinished YAML entry discarded healthy pairs")
	}
	if read.Body != "Original body" || read.Metadata().Title != "3" || len(read.Diagnostics()) != 4 {
		t.Fatalf("content or persistent issues changed: %v", read.Diagnostics())
	}
	for _, location := range []string{"metadata.details.bad", "metadata.details.list[1]", "metadata.cycle.self", "metadata.broken_node"} {
		if !strings.Contains(strings.Join(read.Diagnostics(), "\n"), location) {
			t.Fatalf("issue lacks location %s: %v", location, read.Diagnostics())
		}
	}
}

func TestCreateRetriesItsOwnUnfinishedInitialization(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	parent := filepath.Join(t.TempDir(), "parent")
	if err := os.Mkdir(parent, 0300); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(parent, 0700)
	store, err := Open(filepath.Join(parent, "store"))
	if err != nil || len(store.Diagnostics()) == 0 {
		t.Fatalf("initialization was not deferred: %v", err)
	}
	if err := os.Chmod(parent, 0700); err != nil {
		t.Fatal(err)
	}
	doc, err := store.Create(Memories, testUserFields(Memories, nil), "Recovered without reopening")
	if err != nil || doc.Pending {
		t.Fatalf("same-handle retry failed: %v, %v", err, store.Diagnostics())
	}
	if read, err := store.Read(doc.ID); err != nil || read.Body != doc.Body {
		t.Fatalf("recovered write unavailable: %v", err)
	}
	// The save repaired initialization, but it did not scan outstanding work.
	if err := store.Recover(); err != nil || len(store.Diagnostics()) != 0 {
		t.Fatalf("recovery did not finish initialization: %v, %v", err, store.Diagnostics())
	}
}

func TestUnrelatedSaveDoesNotClearDeferredRecovery(t *testing.T) {
	path := t.TempDir()
	if _, err := Open(path); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	id := "m_000000000000"
	name := filepath.Join("memories", id+"/record.md")
	if _, err := stage(root, name, "", []byte(sample)); err != nil {
		t.Fatal(err)
	}
	lock := flock.New(filepath.Join(path, ".lock"))
	if err := lock.Lock(); err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	store, err := Open(path)
	if err != nil || len(store.Diagnostics()) == 0 {
		t.Fatalf("recovery was not deferred: %v", err)
	}
	if err := lock.Unlock(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(Entities, testUserFields(Entities, nil), "Unrelated save"); err != nil {
		t.Fatal(err)
	}
	if _, err := root.Lstat(name); !errors.Is(err, fs.ErrNotExist) || len(store.Diagnostics()) == 0 {
		t.Fatal("unrelated save hid unfinished recovery")
	}
	if err := store.Recover(); err != nil || len(store.Diagnostics()) != 0 {
		t.Fatalf("actual recovery did not clear deferred status: %v, %v", err, store.Diagnostics())
	}
	if doc, err := store.Read(id); err != nil || doc.Pending || doc.Body != Parse([]byte(sample)).Body {
		t.Fatalf("retained work did not recover: %v", err)
	}
}

func TestRecoveryCleansIdenticalIndependentCopies(t *testing.T) {
	path := t.TempDir()
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	id := "m_000000000000"
	name := filepath.Join("memories", id+"/record.md")
	if _, err := stage(root, name, "", []byte(sample)); err != nil {
		t.Fatal(err)
	}
	// A restore or an editor's atomic save can replace the canonical inode while
	// retaining the exact same content as staging.
	if err := fixtureRootWriteFile(root, name, []byte(sample), 0600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := store.Recover(); err != nil || len(store.Diagnostics()) != 0 {
			t.Fatalf("matching restored data remained stuck: %v, %v", err, store.Diagnostics())
		}
	}
	if _, err := root.Lstat(pendingName(name)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("redundant staging was not removed")
	}
	if data, err := root.ReadFile(name); err != nil || string(data) != sample {
		t.Fatal("cleanup changed the committed record")
	}
	if doc, err := store.Read(id); err != nil || doc.Pending || len(doc.Diagnostics()) != 0 {
		t.Fatalf("restored record unavailable or still awaiting cleanup: %v", err)
	}
}

func TestStagingReservationAndAmbiguityStayIsolated(t *testing.T) {
	path := t.TempDir()
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	name := "memories/m_000000000000/record.md"
	if err := fixtureRootMkdir(root, pendingName(name), 0700); err != nil {
		t.Fatal(err)
	}
	if retained, err := stage(root, name, "", []byte(sample)); retained || !errors.Is(err, fs.ErrExist) {
		t.Fatalf("reused claimed ID: %v", err)
	}
	if err := store.Recover(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read("m_000000000000"); err == nil || errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("empty reservation mistaken for an absent record: %v", err)
	}
	for _, data := range [][]byte{[]byte(sample), []byte("Different retained body")} {
		if err := fixtureRootWriteFile(root, temporaryName(name, "", data), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Recover(); err != nil {
		t.Fatal(err)
	}
	if _, err := root.Lstat(name); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("arbitrarily selected one of two staged records")
	}
	for _, data := range [][]byte{[]byte(sample), []byte("Different retained body")} {
		if got, err := root.ReadFile(temporaryName(name, "", data)); err != nil || !bytes.Equal(got, data) {
			t.Fatal("ambiguous data was discarded")
		}
	}
	if _, err := store.Create(Stories, testUserFields(Stories, nil), "Unrelated work continues"); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryRetainsForeignPendingData(t *testing.T) {
	path := t.TempDir()
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := store.Create(Memories, testUserFields(Memories, nil), "Committed")
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Join("memories", doc.ID+"/record.md")
	foreign := filepath.Join(path, pendingName(name))
	if err := fixtureWriteFile(foreign, []byte("Unrecognized data"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.Recover(); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(foreign); err != nil || string(data) != "Unrecognized data" {
		t.Fatal("deleted unknown pending data")
	}
	if read, err := store.Read(doc.ID); err != nil || read.Body != "Committed" || read.Pending {
		t.Fatalf("foreign data disabled a committed record: %v", err)
	}
}

func TestRecoveryFinishesInterruptedStagingCleanup(t *testing.T) {
	path := t.TempDir()
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	name := "memories/m_000000000000/record.md"
	data := []byte(sample)
	if _, err := stage(root, name, "", data); err != nil {
		t.Fatal(err)
	}
	if err := root.Link(temporaryName(name, "", data), name); err != nil {
		t.Fatal(err)
	}
	if err := syncPath(root, "memories"); err != nil {
		t.Fatal(err)
	}
	// Publication finished; cleanup removed the payload but not its directory.
	if err := root.Remove(temporaryName(name, "", data)); err != nil {
		t.Fatal(err)
	}
	if err := store.Recover(); err != nil || len(store.Diagnostics()) != 0 {
		t.Fatalf("interrupted cleanup remained blocked: %v, %v", err, store.Diagnostics())
	}
	if _, err := root.Lstat(pendingName(name)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("empty reservation was not cleaned up")
	}
	if read, err := store.Read("m_000000000000"); err != nil || read.Body != Parse(data).Body || read.Pending {
		t.Fatalf("cleanup harmed the committed record: %v", err)
	}
}

func TestPublicationKeepsStagingWhenDirectorySyncFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	path := t.TempDir()
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	name := "memories/m_000000000000/record.md"
	data := []byte(sample)
	if _, err := stage(root, name, "", data); err != nil {
		t.Fatal(err)
	}
	publicationRoot, err := os.OpenRoot(filepath.Join(path, filepath.Dir(name)))
	if err != nil {
		t.Fatal(err)
	}
	defer publicationRoot.Close()
	// Linking is permitted, but opening the directory to sync publication fails.
	if err := root.Chmod(filepath.Dir(name), 0300); err != nil {
		t.Fatal(err)
	}
	defer root.Chmod(filepath.Dir(name), 0700)
	if published, err := store.publish(publicationRoot, filepath.Base(name), data); published || err == nil {
		t.Fatal("claimed publication without syncing the final name")
	}
	if err := root.Chmod(filepath.Dir(name), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := root.Lstat(name); err != nil {
		t.Fatalf("test did not reach publication: %v", err)
	}
	if retained, err := root.ReadFile(temporaryName(name, "", data)); err != nil || !bytes.Equal(retained, data) {
		t.Fatal("failed publication discarded durable staging")
	}
	if err := store.Recover(); err != nil || len(store.Diagnostics()) != 0 {
		t.Fatalf("publication did not resume after repair: %v, %v", err, store.Diagnostics())
	}
	if doc, err := store.Read("m_000000000000"); err != nil || doc.Pending || doc.Body != Parse(data).Body {
		t.Fatalf("recovered record unavailable: %v", err)
	}
}
