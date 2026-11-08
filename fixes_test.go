package songya

import (
	"bufio"
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"go.yaml.in/yaml/v3"
)

func TestFrontmatterTrailingContentIsRetained(t *testing.T) {
	for _, tail := range []string{"custom: DO_NOT_LOSE\n", "broken: [DO_NOT_LOSE\n"} {
		header := "title: Example\ncreated_at: 2026-11-03T10:00:00Z\nupdated_at: 2026-11-03T10:00:00Z\n...\n" + tail
		d := Parse([]byte("---\n" + header + "---\nOriginal body"))
		if metadata := d.Metadata(); metadata.Title != "Example" || metadata.CreatedAt.IsZero() || metadata.UpdatedAt.IsZero() {
			t.Fatal("trailing content hid healthy metadata")
		}
		encoded, err := d.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(encoded, []byte("DO_NOT_LOSE")) || len(d.Diagnostics()) == 0 {
			t.Fatal("trailing header content was silently discarded")
		}
		var retained string
		raw := field(&Parse(encoded).Frontmatter, "unparsed_frontmatter")
		if raw == nil {
			t.Fatal("original frontmatter was not retained")
		}
		if err := raw.Decode(&retained); err != nil || retained != header {
			t.Fatalf("header changed: %q, %v", retained, err)
		}
		if Parse(encoded).Body != "Original body" {
			t.Fatal("body changed")
		}
	}
}

func TestCorruptedReadyWriteIsNotPublished(t *testing.T) {
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
	name := filepath.Join("memories", "m_7k3m9x2q6v4c/record.md")
	if ready, err := stage(root, name, "", []byte(sample)); !ready || err != nil {
		t.Fatal(err)
	}
	changed := []byte("---\ntitle: Truncated\n---\nOnly part of the body")
	if err := fixtureRootWriteFile(root, temporaryName(name, "", []byte(sample)), changed, 0600); err != nil {
		t.Fatal(err)
	}
	if published, err := store.complete(root, name); published || !errors.Is(err, errUnverifiedPending) {
		t.Fatalf("changed data was acknowledged as saved: %v", err)
	}
	if err := store.Recover(); err != nil {
		t.Fatal(err)
	}
	if _, err := root.Lstat(name); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("corrupted pending data was published")
	}
	if data, err := root.ReadFile(temporaryName(name, "", []byte(sample))); err != nil || !bytes.Equal(data, changed) {
		t.Fatal("damaged pending data was discarded")
	}
	doc, err := store.Read("m_7k3m9x2q6v4c")
	if err != nil || !doc.Pending || len(doc.Diagnostics()) == 0 {
		t.Fatalf("damaged data was hidden or reported as complete: %v", err)
	}
}

func TestCommittedContentWinsOverPendingWhenLockBreaks(t *testing.T) {
	path := t.TempDir()
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	committed, err := store.Create(Memories, testUserFields(Memories, nil), "Authoritative body")
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	name := filepath.Join("memories", committed.ID+"/record.md")
	if ready, err := stage(root, name, "", []byte(sample)); !ready || err != nil {
		t.Fatal(err)
	}
	if err := root.Remove(".lock"); err != nil {
		t.Fatal(err)
	}
	if err := fixtureRootMkdir(root, ".lock", 0700); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := reopened.Read(committed.ID)
	if err != nil || doc.Body != "Authoritative body" || doc.Pending {
		t.Fatalf("pending content replaced the committed memory: %+v %v", doc, err)
	}
	if len(doc.Diagnostics()) == 0 {
		t.Fatal("pending recovery was not reported")
	}
	if data, err := root.ReadFile(temporaryName(name, "", []byte(sample))); err != nil || string(data) != sample {
		t.Fatal("conflicting pending work was lost")
	}
}

func TestOpenDoesNotWaitForUnrelatedWriter(t *testing.T) {
	path := t.TempDir()
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := store.Create(Memories, testUserFields(Memories, nil), "Readable body")
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
	defer func() { _ = command.Process.Kill(); _ = command.Wait() }()
	if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "locked\n" {
		t.Fatalf("child did not lock: %q %v", line, err)
	}
	opened := make(chan *Store, 1)
	failures := make(chan error, 1)
	go func() {
		s, err := Open(path)
		if err != nil {
			failures <- err
		} else {
			opened <- s
		}
	}()
	select {
	case s := <-opened:
		read, err := s.Read(doc.ID)
		if err != nil || read.Body != "Readable body" {
			t.Fatalf("fresh reader could not read: %v", err)
		}
	case err := <-failures:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("opening a reader waited for an unrelated writer")
	}
}

func TestReplacingNestedAnchorKeepsExternalAliases(t *testing.T) {
	input := strings.Replace(sample, "aliases: &names [\"α\", \"000123\", \"yes\"]", "aliases: &names [\"α\", &nested \"000123\", \"yes\"]", 1)
	input = strings.Replace(input, "custom:\n", "nested_reference: *nested # Keep reference comment\ncustom:\n", 1)
	doc := Parse([]byte(input))
	if err := doc.Set("aliases", []string{"Replacement"}); err != nil {
		t.Fatal(err)
	}
	encoded, err := doc.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var root yaml.Node
	if err := yaml.Unmarshal(encoded, &root); err != nil {
		t.Fatalf("edit broke YAML: %v", err)
	}
	read := Parse(encoded)
	var value string
	if err := field(&read.Frontmatter, "nested_reference").Decode(&value); err != nil || value != "000123" {
		t.Fatalf("referenced value lost: %q %v", value, err)
	}
	if !bytes.Contains(encoded, []byte("Keep reference comment")) {
		t.Fatal("alias comment lost")
	}
	if read.Body != doc.Body || read.Metadata().Title != "Example" {
		t.Fatal("unrelated content changed")
	}
}

func TestReplacingNestedAnchorsKeepsRecursiveReferences(t *testing.T) {
	doc := Parse([]byte("---\naliases:\n  outer: &outer\n    inner: &inner [*outer]\ninner_reference: *inner\nouter_reference: *outer\nagain: *inner\n---\nBody"))
	if err := doc.Set("aliases", "Replacement"); err != nil {
		t.Fatal(err)
	}
	encoded, err := doc.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var node yaml.Node
	if err := yaml.Unmarshal(encoded, &node); err != nil {
		t.Fatalf("recursive references broke YAML: %v", err)
	}
	read := Parse(encoded)
	inner := field(&read.Frontmatter, "inner_reference")
	outer := field(&read.Frontmatter, "outer_reference")
	again := field(&read.Frontmatter, "again")
	if inner.Anchor != "inner" || outer.Alias != inner.Content[0] || again.Alias != inner {
		t.Fatal("references no longer share their original values")
	}
	if field(inner.Content[0], "inner").Alias != inner {
		t.Fatal("recursive reference was lost")
	}
}

func TestInitializationRetriesUnsyncedParents(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	for _, destination := range []string{"store", "unfinished-parent/store"} {
		t.Run(destination, func(t *testing.T) {
			parent := filepath.Join(t.TempDir(), "parent")
			if err := os.Mkdir(parent, 0300); err != nil {
				t.Fatal(err)
			}
			defer os.Chmod(parent, 0700)
			path := filepath.Join(parent, destination)
			var unfinished *Store
			for range 2 {
				store, err := Open(path)
				if err == nil {
					unfinished = store
					if _, err := store.Create(Memories, testUserFields(Memories, nil), "Must remain unacknowledged"); err == nil {
						t.Fatal("save succeeded before the new store's parents could be synced")
					}
				}
			}
			// Even an interrupted initializer must not hide existing content.
			if err := os.MkdirAll(filepath.Join(path, "memories"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := fixtureWriteFile(filepath.Join(path, "memories", "m_7k3m9x2q6v4c/record.md"), []byte(sample), 0600); err != nil {
				t.Fatal(err)
			}
			if unfinished == nil {
				t.Fatal("initialization failure prevented opening a reader")
			}
			if doc, err := unfinished.Read("m_7k3m9x2q6v4c"); err != nil || doc.Body != Parse([]byte(sample)).Body {
				t.Fatalf("initialization failure hid readable data: %v", err)
			}
			if err := os.Chmod(parent, 0700); err != nil {
				t.Fatal(err)
			}
			store := unfinished
			if err := unfinished.Recover(); err != nil || len(unfinished.Diagnostics()) != 0 {
				t.Fatalf("repaired initialization left stale diagnostics: %v, %v", err, unfinished.Diagnostics())
			}
			saved, err := store.Create(Memories, testUserFields(Memories, nil), "Recovered initialization")
			if err != nil {
				t.Fatal(err)
			}
			// An initialized store still works through a traverse-only parent.
			if err := os.Chmod(parent, 0111); err != nil {
				t.Fatal(err)
			}
			store, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			if read, err := store.Read(saved.ID); err != nil || read.Body != saved.Body {
				t.Fatalf("recovered memory unavailable: %v", err)
			}
			if _, err := store.Create(Topics, testUserFields(Topics, nil), "Still writable"); err != nil {
				t.Fatalf("initialized store unnecessarily needed its parent reopened: %v", err)
			}
		})
	}
}

func TestPendingReadDoesNotWaitAndRecoveryClearsDeferredDiagnostic(t *testing.T) {
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
	id := "m_7k3m9x2q6v4c"
	name := filepath.Join("memories", id+"/record.md")
	if ready, err := stage(root, name, "", []byte(sample)); !ready || err != nil {
		t.Fatal(err)
	}
	lock := flock.New(filepath.Join(path, ".lock"))
	if err := lock.Lock(); err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	store, err = Open(path)
	if err != nil || len(store.Diagnostics()) == 0 {
		t.Fatalf("deferred recovery was not reported: %v", err)
	}
	readDone := make(chan *Document, 1)
	readErrors := make(chan error, 1)
	go func() {
		doc, err := store.Read(id)
		if err != nil {
			readErrors <- err
		} else {
			readDone <- doc
		}
	}()
	select {
	case doc := <-readDone:
		if !doc.Pending || doc.Body != Parse([]byte(sample)).Body || len(doc.Diagnostics()) == 0 {
			t.Fatal("pending data or deferred recovery status lost")
		}
	case err := <-readErrors:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("complete pending data waited behind an unrelated writer")
	}
	if err := lock.Unlock(); err != nil {
		t.Fatal(err)
	}
	if err := store.Recover(); err != nil {
		t.Fatal(err)
	}
	if len(store.Diagnostics()) != 0 {
		t.Fatalf("successful recovery left stale diagnostics: %v", store.Diagnostics())
	}
	if doc, err := store.Read(id); err != nil || doc.Pending || doc.Body != Parse([]byte(sample)).Body {
		t.Fatalf("pending record did not recover: %v", err)
	}
}

func TestRecoveryClearsDeferredDiagnostic(t *testing.T) {
	path := t.TempDir()
	if _, err := Open(path); err != nil {
		t.Fatal(err)
	}
	lock := flock.New(filepath.Join(path, ".lock"))
	if err := lock.Lock(); err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	store, err := Open(path)
	if err != nil || len(store.Diagnostics()) == 0 {
		t.Fatalf("deferred recovery was not reported: %v", err)
	}
	if err := lock.Unlock(); err != nil {
		t.Fatal(err)
	}
	if err := store.Recover(); err != nil || len(store.Diagnostics()) != 0 {
		t.Fatalf("recovery left stale diagnostics: %v, %v", err, store.Diagnostics())
	}
}

func TestRelocatedAnchorsDoNotShadowUnrelatedValues(t *testing.T) {
	for _, reserved := range []string{"", "reserved: &a_1 Reserved\n"} {
		input := "---\nreplace:\n  original: &a OLD\n  nested: &b [*a]\nshadow: &a NEW\n" + reserved + "out: *b\nafter: *a\n---\nBody"
		if reserved != "" {
			input = strings.Replace(input, "after: *a\n", "after: *a\nreserved_after: *a_1\n", 1)
		}
		doc := Parse([]byte(input))
		if err := doc.Set("replace", "Replacement"); err != nil {
			t.Fatal(err)
		}
		encoded, err := doc.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		var values map[string]any
		if err := Parse(encoded).Frontmatter.Decode(&values); err != nil {
			t.Fatal(err)
		}
		if values["after"] != "NEW" || values["shadow"] != "NEW" || values["out"].([]any)[0] != "OLD" {
			t.Fatalf("editing one field changed unrelated values: %v", values)
		}
		if reserved != "" && (values["reserved"] != "Reserved" || values["reserved_after"] != "Reserved") {
			t.Fatal("renamed anchor collided with an existing name")
		}
	}
}
