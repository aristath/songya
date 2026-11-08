package songya

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"go.yaml.in/yaml/v3"
)

func TestStoreInitializesAndPreservesExistingFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store")
	if err := os.MkdirAll(filepath.Join(path, "memories"), 0700); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(path, "memories", "m_7k3m9x2q6v4c/record.md")
	if err := fixtureWriteFile(name, []byte(sample), 0600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		store, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Read("m_7k3m9x2q6v4c"); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(name)
	if err != nil || string(data) != sample {
		t.Fatalf("existing file was changed: %v", err)
	}
	for _, directory := range []string{"memories", "entities", "topics", "stories", ".index"} {
		info, err := os.Stat(filepath.Join(path, directory))
		if err != nil || !info.IsDir() {
			t.Fatalf("missing directory %s: %v", directory, err)
		}
	}
}

func TestStoreCreatesAndReadsAllDestinations(t *testing.T) {
	path := t.TempDir()
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	before := time.Now()
	fields := map[string]any{
		"id":         "caller-owned-id",
		"title":      "α Title",
		"created_at": "invalid caller timestamp",
		"updated_at": 0,
		"relations":  []Relation{{Type: "a_custom_relation", Target: "e_7k3m9x2q6v4c"}},
		"custom":     map[string]any{"enabled": false, "number": 0, "values": []string{"α", "yes", "0001"}},
	}
	body := "\r\n# Original body\r\n\r\nLiteral --- text."
	for _, destination := range []Destination{Memories, Entities, Topics, Stories} {
		doc, err := store.Create(destination, testUserFields(destination, fields), body)
		if err != nil {
			t.Fatal(err)
		}
		metadata := doc.Metadata()
		prefix, _ := destination.prefix()
		if !strings.HasPrefix(doc.ID, prefix) || !ValidID(doc.ID) {
			t.Fatalf("wrong ID for %s: %q", destination, doc.ID)
		}
		if metadata.CreatedAt.Before(before) || metadata.CreatedAt.After(time.Now()) || !metadata.CreatedAt.Equal(metadata.UpdatedAt) || metadata.CreatedAt.Location() != time.UTC {
			t.Fatalf("timestamps were not engine-assigned in UTC: %+v", metadata)
		}
		stored, err := os.ReadFile(filepath.Join(path, string(destination), doc.ID+"/record.md"))
		if err != nil {
			t.Fatal(err)
		}
		read, err := store.Read(doc.ID)
		if err != nil || read.Body != body {
			t.Fatalf("read failed or body changed: %v", err)
		}
		if read.ID != doc.ID {
			t.Fatalf("filename ID changed on read: got %q, want %q", read.ID, doc.ID)
		}
		readBytes, err := read.Marshal()
		if err != nil || !bytes.Equal(readBytes, stored) {
			t.Fatalf("metadata changed after reading: %v", err)
		}
		var actual, expected yaml.Node
		if err := expected.Encode(fields["custom"]); err != nil {
			t.Fatal(err)
		}
		var values map[string]yaml.Node
		if err := read.Frontmatter.Decode(&values); err != nil {
			t.Fatal(err)
		}
		if _, exists := values["id"]; exists {
			t.Fatal("creation wrote an ID into YAML metadata")
		}
		actual = values["custom"]
		var got, want any
		if err := actual.Decode(&got); err != nil {
			t.Fatal(err)
		}
		if err := expected.Decode(&want); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("custom metadata changed: %v", got)
		}
	}
	if fields["id"] != "caller-owned-id" || fields["created_at"] != "invalid caller timestamp" {
		t.Fatal("Create mutated the caller's input")
	}
}

func TestStoreReadsSameSuffixInDifferentDestinations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "store")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, destination := range []Destination{Memories, Entities, Topics, Stories} {
		prefix, _ := destination.prefix()
		id := prefix + "7k3m9x2q6v4c"
		if err := fixtureWriteFile(filepath.Join(path, string(destination), id+"/record.md"), []byte(sample), 0600); err != nil {
			t.Fatal(err)
		}
		read, err := store.Read(id)
		if err != nil {
			t.Fatal(err)
		}
		if read.ID != id {
			t.Fatalf("wrong record for %s: %s", destination, read.ID)
		}
	}
}

func TestStoreWorksWithoutDerivedIndex(t *testing.T) {
	path := t.TempDir()
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	lockBefore, err := os.Stat(filepath.Join(path, ".lock"))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := store.Create(Memories, testUserFields(Memories, nil), "Body")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(path, ".index")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(doc.ID); err != nil {
		t.Fatalf("read depended on index: %v", err)
	}
	if _, err := store.Create(Entities, testUserFields(Entities, nil), "Entity"); err != nil {
		t.Fatalf("creation depended on index: %v", err)
	}
	if _, err := Open(path); err != nil {
		t.Fatal(err)
	}
	lockAfter, err := os.Stat(filepath.Join(path, ".lock"))
	if err != nil || !os.SameFile(lockBefore, lockAfter) {
		t.Fatalf("initialization replaced the coordination lock: %v", err)
	}
}

func TestStoreRejectsInvalidInputAndIsolatesDamagedRecords(t *testing.T) {
	path := t.TempDir()
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, destination := range []Destination{"", "../outside", "unknown"} {
		if _, err := store.Create(destination, testUserFields(destination, nil), "Body"); err == nil {
			t.Fatalf("accepted destination %q", destination)
		}
	}
	damaged, err := store.Create(Memories, testUserFields(Memories, map[string]any{"relations": []Relation{{Type: "after", Target: "bad-id"}}}), "Body")
	if err != nil || damaged.Body != "Body" || len(damaged.Diagnostics()) == 0 {
		t.Fatalf("bad link blocked creation: %v", err)
	}
	for _, id := range []string{"../outside", "7k3m9x2q6v4c", "x_7k3m9x2q6v4c"} {
		if _, err := store.Read(id); err == nil {
			t.Fatalf("accepted invalid ID %q", id)
		}
	}
	if _, err := store.Read("m_000000000000"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing record did not retain ErrNotExist: %v", err)
	}
	for _, content := range []string{"---\ntitle: [broken\n---\nOriginal text", strings.Replace(sample, "created_at: 2026-11-03T10:00:00Z", "created_at: invalid", 1)} {
		name := filepath.Join(path, "memories", "m_7k3m9x2q6v4c/record.md")
		if err := fixtureWriteFile(name, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if doc, err := store.Read("m_7k3m9x2q6v4c"); err != nil || len(doc.Diagnostics()) == 0 {
			t.Fatalf("damaged record was not readable with diagnostics: %v", err)
		}
		data, _ := os.ReadFile(name)
		if string(data) != content {
			t.Fatal("damaged record was rewritten")
		}
		healthy, err := store.Create(Memories, testUserFields(Memories, nil), "Healthy record")
		if err != nil {
			t.Fatalf("damaged record blocked healthy creation: %v", err)
		}
		if _, err := store.Read(healthy.ID); err != nil {
			t.Fatalf("damaged record blocked healthy reading: %v", err)
		}
	}
}

func TestPublicationNeverOverwritesExistingFiles(t *testing.T) {
	path := t.TempDir()
	if _, err := Open(path); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	name := filepath.Join("memories", "m_7k3m9x2q6v4c/record.md")
	if err := fixtureRootWriteFile(root, name, []byte("Existing contents"), 0600); err != nil {
		t.Fatal(err)
	}
	if ready, err := stage(root, name, "", []byte(sample)); !ready || err != nil {
		t.Fatal(err)
	}
	store := &Store{path: path}
	if published, err := store.complete(root, name); published || !errors.Is(err, fs.ErrExist) {
		t.Fatalf("overwrote existing file: %v", err)
	}
	data, _ := root.ReadFile(name)
	if string(data) != "Existing contents" {
		t.Fatal("existing contents changed")
	}
	if data, err := root.ReadFile(temporaryName(name, "", []byte(sample))); err != nil || string(data) != sample {
		t.Fatal("recoverable pending data was discarded")
	}

}

func TestStoreRejectsSymlinksOutsideRoot(t *testing.T) {
	path, outside := t.TempDir(), t.TempDir()
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(outside, "record.md")
	if err := fixtureWriteFile(external, []byte(sample), 0600); err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(filepath.Join(path, "memories", "m_7k3m9x2q6v4c"), 0700)
	if err := os.Symlink(external, filepath.Join(path, "memories", "m_7k3m9x2q6v4c/record.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read("m_7k3m9x2q6v4c"); err == nil {
		t.Fatal("read followed an external record symlink")
	}
	if err := os.Symlink(outside, filepath.Join(path, "linked")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create("linked", testUserFields("linked", nil), "Body"); err == nil {
		t.Fatal("accepted a non-record directory")
	}
	entries, _ := os.ReadDir(outside)
	if len(entries) != 1 {
		t.Fatal("store wrote outside its root")
	}
}

func TestStoreInitializationRejectsNonDirectoryAndExternalLinks(t *testing.T) {
	for _, name := range []string{"memories", ".index", ".lock"} {
		t.Run(name, func(t *testing.T) {
			path, outside := t.TempDir(), t.TempDir()
			if err := os.Symlink(outside, filepath.Join(path, name)); err != nil {
				t.Fatal(err)
			}
			store, err := Open(path)
			if name == ".lock" {
				if err != nil || len(store.Diagnostics()) == 0 {
					t.Fatalf("coordination problem was not isolated: %v", err)
				}
				if _, err := store.Create(Topics, testUserFields(Topics, nil), "Blocked write"); err == nil {
					t.Fatal("wrote through an unsafe lock")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.Create(Topics, testUserFields(Topics, nil), "Healthy"); err != nil {
					t.Fatal(err)
				}
			}
			entries, _ := os.ReadDir(outside)
			if len(entries) != 0 {
				t.Fatal("initialization wrote outside the store")
			}
		})
	}
	path := t.TempDir()
	name := filepath.Join(path, "memories")
	if err := fixtureWriteFile(name, []byte("Existing file"), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path)
	if err != nil || len(store.Diagnostics()) == 0 {
		t.Fatalf("one bad directory disabled the store: %v", err)
	}
	if _, err := store.Create(Topics, testUserFields(Topics, nil), "Healthy"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(name)
	if string(data) != "Existing file" {
		t.Fatal("initialization replaced an existing file")
	}
}

func helperProcess(t *testing.T, role, path string) *exec.Cmd {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestStoreProcess$")
	command.Env = append(os.Environ(), "SONGYA_TEST_ROLE="+role, "SONGYA_TEST_PATH="+path, "GORACE=atexit_sleep_ms=0")
	return command
}

func TestStoreConcurrentProcessesPublishCompleteRecords(t *testing.T) {
	path := t.TempDir()
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	const workers = 4
	results := make(chan error, workers)
	for range workers {
		command := helperProcess(t, "writer", path)
		go func() {
			data, err := command.CombinedOutput()
			if err != nil {
				err = fmt.Errorf("writer: %w: %s", err, data)
			}
			results <- err
		}()
	}
	// Inspect published names directly, without the engine's read lock, while
	// separate writer processes are running. Staging files are not record names.
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	finished := 0
	for finished < workers {
		select {
		case err := <-results:
			if err != nil {
				t.Error(err)
			}
			finished++
		case <-ticker.C:
			for _, destination := range destinations {
				entries, err := os.ReadDir(filepath.Join(path, string(destination)))
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range entries {
					if !entry.IsDir() || !ValidID(entry.Name()) {
						continue
					}
					data, err := os.ReadFile(filepath.Join(path, string(destination), entry.Name(), "record.md"))
					if errors.Is(err, fs.ErrNotExist) {
						continue
					}
					if err != nil {
						t.Fatal(err)
					}
					if doc := Parse(data); len(doc.Body) != 128*1024 {
						t.Fatal("observed partially published record")
					}
				}
			}
		}
	}
	count := 0
	for _, destination := range destinations {
		entries, _ := os.ReadDir(filepath.Join(path, string(destination)))
		for _, entry := range entries {
			if !entry.IsDir() || !ValidID(entry.Name()) {
				t.Fatalf("unexpected leftover staging file %q", entry.Name())
			}
			id := entry.Name()
			document, err := store.Read(id)
			if err != nil || len(document.Body) != 128*1024 {
				t.Fatalf("incomplete saved record %q: %v", id, err)
			}
			count++
		}
	}
	if count != workers*8 {
		t.Fatalf("expected %d records, found %d", workers*8, count)
	}
}

func TestStoreLockIsReleasedWhenWriterProcessDies(t *testing.T) {
	path := t.TempDir()
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	existing, err := store.Create(Topics, testUserFields(Topics, nil), "Already committed")
	if err != nil {
		t.Fatal(err)
	}
	command := helperProcess(t, "hold-lock", path)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer command.Process.Kill()
	if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "locked\n" {
		t.Fatalf("writer did not acquire the lock: %q, %v", line, err)
	}
	readDone := make(chan error, 1)
	go func() { _, err := store.Read(existing.ID); readDone <- err }()
	select {
	case err := <-readDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("a writer blocked an unrelated committed read")
	}
	done := make(chan error, 1)
	go func() {
		_, err := store.Create(Memories, testUserFields(Memories, nil), "After interruption")
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("creation bypassed another process's lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = command.Wait()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("creation remained blocked after lock owner died")
	}
}

// TestStoreProcess is a subprocess entry point, not an additional public API.
func TestStoreProcess(t *testing.T) {
	role, path := os.Getenv("SONGYA_TEST_ROLE"), os.Getenv("SONGYA_TEST_PATH")
	if role == "" {
		return
	}
	if strings.HasPrefix(role, "batch-") {
		testBatchProcess(t, role, path)
		return
	}
	if strings.HasPrefix(role, "update-") {
		testUpdateProcess(t, role, path)
		return
	}
	if role == "cyclic-metadata" {
		testCyclicMetadata(t, path)
		return
	}
	if role == "shared-merges" {
		testSharedMergeReferences(t)
		return
	}
	if role == "raw-yaml-recovery" {
		testRawYAMLRecovery(t, path)
		return
	}
	if role == "failed-collision" {
		testFailedCollision(t, path)
		return
	}
	if role == "orphan-collision" {
		testOrphanCollision(t, path)
		return
	}
	if role == "staged-crash" {
		store := &Store{path: path}
		err := store.locked(true, func(root *os.Root) error {
			if _, err := stage(root, filepath.Join("memories", "m_7k3m9x2q6v4c/record.md"), "", []byte(sample)); err != nil {
				return err
			}
			fmt.Println("staged")
			_, _ = io.Copy(io.Discard, os.Stdin)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	if role == "hold-lock" {
		lock := flock.New(filepath.Join(path, ".lock"))
		if err := lock.Lock(); err != nil {
			t.Fatal(err)
		}
		defer lock.Close()
		fmt.Println("locked")
		_, _ = io.Copy(io.Discard, os.Stdin)
		return
	}
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 8 {
		if _, err := store.Create(destinations[i%len(destinations)], testUserFields(destinations[i%len(destinations)], map[string]any{"title": "Concurrent"}), strings.Repeat("x", 128*1024)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOpenExistingDoesNotReplaceMissingSourcesOrPublishRecovery(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent")
	if _, err := OpenExisting(missing); err == nil {
		t.Fatal("missing source opened")
	}
	if _, err := os.Stat(missing); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("query created missing store: %v", err)
	}
	path := t.TempDir()
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	entity, err := store.Create(Entities, map[string]any{"name": "Alex"}, "A musician")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(path, "memories")); err != nil {
		t.Fatal(err)
	}
	reader, err := OpenExisting(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := reader.Scan(context.Background())
	if err != nil || len(catalog.Records) != 1 || catalog.Records[0].ID != entity.ID || len(catalog.Diagnostics) == 0 {
		t.Fatalf("healthy record or missing-source diagnostic lost: %+v, %v", catalog, err)
	}
	if _, err := os.Stat(filepath.Join(path, "memories")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("query recreated missing record directory: %v", err)
	}
	if err := os.Mkdir(filepath.Join(path, "memories"), 0700); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join("memories", "m_7k3m9x2q6v4c/record.md")
	if err := os.MkdirAll(filepath.Join(path, pendingName(name)), 0700); err != nil {
		t.Fatal(err)
	}
	if err := fixtureWriteFile(filepath.Join(path, temporaryName(name, "", []byte(sample))), []byte(sample), 0600); err != nil {
		t.Fatal(err)
	}
	reader, err = OpenExisting(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(path, name)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("query published recovery work: %v", err)
	}
	writer, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Read("m_7k3m9x2q6v4c"); err != nil {
		t.Fatalf("normal writer did not recover: %v", err)
	}
}
