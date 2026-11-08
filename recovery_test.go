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
)

func TestIndependentMetadataAndEquivalentValues(t *testing.T) {
	for _, title := range []string{"3", `"3"`} {
		source := strings.Replace(sample, "title: Example", "title: "+title, 1)
		source = strings.Replace(source, "created_at: 2026-11-03T10:00:00Z", "created_at: unknown", 1)
		source = strings.Replace(source, "m_b8r5n2w9d4hx", "unresolved", 1)
		d := Parse([]byte(source))
		m := d.Metadata()
		if m.Title != "3" || m.UpdatedAt.IsZero() || !m.CreatedAt.IsZero() || len(m.Relations) != 2 {
			t.Fatalf("one bad field hid healthy data: %+v", m)
		}
		if len(d.Diagnostics()) == 0 {
			t.Fatal("problems were not reported")
		}
		serialized, err := d.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(serialized, []byte("unresolved")) || !bytes.Contains(serialized, []byte("unknown")) {
			t.Fatal("damaged values were discarded")
		}
	}
	d := Parse([]byte(strings.Replace(sample, "title: Example", "title: [not, scalar]", 1)))
	if d.Metadata().UpdatedAt.IsZero() || len(d.Metadata().Relations) != 2 || d.Body == "" {
		t.Fatal("bad title hid the rest of the record")
	}
}

func TestMalformedHeaderRecoveryRetainsOriginal(t *testing.T) {
	raw := "title: [unfinished\ncustom: keep this\n"
	body := "\r\nOriginal body without final newline"
	d := Parse([]byte("---\n" + raw + "---\n" + body))
	if d.Body != body {
		t.Fatal("original body changed")
	}
	if err := d.Set("title", 3); err != nil {
		t.Fatal(err)
	}
	encoded, err := d.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	read := Parse(encoded)
	var preserved string
	if err := field(&read.Frontmatter, "unparsed_frontmatter").Decode(&preserved); err != nil {
		t.Fatal(err)
	}
	if preserved != raw || read.Body != body || read.Metadata().Title != "3" {
		t.Fatal("unparsed header or readable content lost")
	}
	// A delimiter inside a YAML block is data, not the end of frontmatter.
	read = Parse([]byte("---\ncustom: |\n  ---\n  original\n---\nBody"))
	if read.Body != "Body" {
		t.Fatal("indented YAML data was treated as a delimiter")
	}
}

func TestUnsupportedMetadataDoesNotCrashOrLoseBody(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	doc, err := store.Create(Memories, testUserFields(Memories, map[string]any{"title": 3, "unencodable": func() {}}), "Original body")
	if err != nil {
		t.Fatal(err)
	}
	read, err := store.Read(doc.ID)
	if err != nil || read.Body != "Original body" || read.Metadata().Title != "3" {
		t.Fatalf("body lost: %v", err)
	}
	if len(doc.Diagnostics()) == 0 {
		t.Fatal("normalization was not reported")
	}
}

func TestBrokenOptionalDirectoriesDoNotDisableStore(t *testing.T) {
	path := t.TempDir()
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := store.Create(Memories, testUserFields(Memories, nil), "Healthy")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(path, ".index")); err != nil {
		t.Fatal(err)
	}
	if err := fixtureWriteFile(filepath.Join(path, ".index"), []byte("Broken disposable cache"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(path, "entities")); err != nil {
		t.Fatal(err)
	}
	if err := fixtureWriteFile(filepath.Join(path, "entities"), []byte("Preserve this"), 0600); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if read, err := store.Read(doc.ID); err != nil || read.Body != "Healthy" {
		t.Fatalf("healthy memory unavailable: %v", err)
	}
	if _, err := store.Create(Topics, testUserFields(Topics, nil), "Healthy topic"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(Entities, testUserFields(Entities, nil), "Blocked destination"); err == nil {
		t.Fatal("could not store entity but claimed success")
	}
	data, _ := os.ReadFile(filepath.Join(path, "entities"))
	if string(data) != "Preserve this" {
		t.Fatal("damaged directory contents discarded")
	}
	if info, err := os.Stat(filepath.Join(path, ".index")); err != nil || !info.IsDir() {
		t.Fatal("cache was not recreated")
	}
}

func TestExistingStoreNeedsOnlyTraversalOfAncestors(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	parent := filepath.Join(t.TempDir(), "parent")
	path := filepath.Join(parent, "store")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := store.Create(Memories, testUserFields(Memories, nil), "Body")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0111); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(parent, 0700)
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(doc.ID); err != nil {
		t.Fatal(err)
	}
}

func TestPendingWriteRecoversAfterPublicationFailure(t *testing.T) {
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
	// A destination obstruction prevents publication, but not access to staged data.
	if err := fixtureRootMkdir(root, name, 0700); err != nil {
		t.Fatal(err)
	}
	if published, err := store.complete(root, name); published || err == nil {
		t.Fatal("expected blocked publication")
	}
	read, err := store.Read(id)
	if err != nil || !read.Pending || read.Body != Parse([]byte(sample)).Body {
		t.Fatalf("pending record unavailable: %v", err)
	}
	if err := root.Remove(name); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	read, err = store.Read(id)
	if err != nil || read.Pending || read.Body != Parse([]byte(sample)).Body {
		t.Fatalf("recovery failed: %v", err)
	}
	data, err := root.ReadFile(name)
	if err != nil || string(data) != sample {
		t.Fatal("recovered data changed")
	}
	for _, suffix := range []string{".pending", ".tmp"} {
		_, err := root.Lstat(filepath.Join("memories", "."+id+"/record.md"+suffix))
		if !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("staging file not cleaned: %v", err)
		}
	}
	if err := store.Recover(); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Join(path, "memories"))
	if len(entries) != 1 {
		t.Fatal("recovery duplicated a record")
	}
}

func TestIncompleteAndConflictingWritesStayIsolated(t *testing.T) {
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
	incomplete := temporaryName(filepath.Join("memories", "m_000000000000/record.md"), "", []byte(sample))
	if err := root.MkdirAll(filepath.Dir(incomplete), 0700); err != nil {
		t.Fatal(err)
	}
	if err := fixtureRootWriteFile(root, incomplete, []byte("Incomplete original"), 0600); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join("memories", "m_7k3m9x2q6v4c/record.md")
	if ready, err := stage(root, name, "", []byte(sample)); !ready || err != nil {
		t.Fatal(err)
	}
	if err := fixtureRootWriteFile(root, name, []byte("Different original record"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.Recover(); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{incomplete: "Incomplete original", name: "Different original record", temporaryName(name, "", []byte(sample)): sample} {
		data, err := root.ReadFile(name)
		if err != nil || string(data) != want {
			t.Fatalf("discarded unresolved work %s: %v", name, err)
		}
	}
	if doc, err := store.Read("m_000000000000"); err != nil || !doc.Pending || doc.Body != "Incomplete original" || len(doc.Diagnostics()) == 0 {
		t.Fatalf("incomplete bytes were hidden or claimed complete: %v", err)
	}
	if _, err := store.Create(Topics, testUserFields(Topics, nil), "Healthy"); err != nil {
		t.Fatal(err)
	}
	if len(store.Diagnostics()) < 2 {
		t.Fatal("unresolved work was not reported")
	}
}

func TestPublicationPreservesUnrelatedStagingData(t *testing.T) {
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
	// Files unrelated to the staged record must never be deleted by cleanup.
	temporary := filepath.Join(pendingName(name), "unrelated.keep")
	if err := fixtureRootWriteFile(root, temporary, []byte("Unrelated retained data"), 0600); err != nil {
		t.Fatal(err)
	}
	if published, err := store.complete(root, name); !published || err != nil {
		t.Fatalf("durable publication failed: %v", err)
	}
	if data, _ := root.ReadFile(temporary); string(data) != "Unrelated retained data" {
		t.Fatal("unrelated staging data deleted")
	}
}

func TestStagedFileRecoversBeforePublication(t *testing.T) {
	for _, trigger := range []string{"read", "open"} {
		t.Run(trigger, func(t *testing.T) {
			path := t.TempDir()
			store, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			name := filepath.Join("memories", "m_7k3m9x2q6v4c/record.md")
			// The ID was claimed and writing finished before the process could sync or publish.
			if err := os.MkdirAll(filepath.Join(path, pendingName(name)), 0700); err != nil {
				t.Fatal(err)
			}
			if err := fixtureWriteFile(filepath.Join(path, temporaryName(name, "", []byte(sample))), []byte(sample), 0600); err != nil {
				t.Fatal(err)
			}
			if trigger == "open" {
				store, err = Open(path)
				if err != nil {
					t.Fatal(err)
				}
			}
			doc, err := store.Read("m_7k3m9x2q6v4c")
			if err != nil || doc.Pending || doc.Body != Parse([]byte(sample)).Body {
				t.Fatalf("complete temporary write was not recovered: %v", err)
			}
			entries, _ := os.ReadDir(filepath.Join(path, "memories"))
			if len(entries) != 1 {
				t.Fatal("recovery left duplicates or staging files")
			}
		})
	}
}

func TestCommonDateRepresentationsAndSingleRelation(t *testing.T) {
	doc := Parse([]byte("---\ntitle: 3\ncreated_at: '2026-11-03 10:00:00'\nupdated_at: '2026-11-03'\nrelations:\n  type: Related-To\n  target: m_7k3m9x2q6v4c\n---\nBody"))
	m := doc.Metadata()
	if m.Title != "3" || m.CreatedAt.IsZero() || m.UpdatedAt.IsZero() || len(m.Relations) != 1 || m.Relations[0].Type != "related_to" {
		t.Fatalf("equivalent representations rejected: %+v", m)
	}
	if len(doc.Diagnostics()) != 0 {
		t.Fatalf("valid equivalents reported as damage: %v", doc.Diagnostics())
	}
}

func TestProcessCrashRecoversSameRecord(t *testing.T) {
	path := t.TempDir()
	if _, err := Open(path); err != nil {
		t.Fatal(err)
	}
	command := helperProcess(t, "staged-crash", path)
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer command.Process.Kill()
	if line, err := bufio.NewReader(output).ReadString('\n'); err != nil || line != "staged\n" {
		t.Fatalf("child did not stage data: %q %v", line, err)
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = command.Wait()
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := store.Read("m_7k3m9x2q6v4c")
	if err != nil || doc.Pending || doc.Body != Parse([]byte(sample)).Body {
		t.Fatalf("interrupted write was not recovered: %v", err)
	}
	if err := store.Recover(); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Join(path, "memories"))
	if len(entries) != 1 {
		t.Fatal("interrupted write was lost or duplicated")
	}
}

func TestBrokenCoordinationLeavesRecordsReadable(t *testing.T) {
	path := t.TempDir()
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := store.Create(Memories, testUserFields(Memories, nil), "Committed body")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(path, ".lock")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(path, ".lock"), 0700); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path)
	if err != nil || len(store.Diagnostics()) == 0 {
		t.Fatalf("store unavailable: %v", err)
	}
	if read, err := store.Read(doc.ID); err != nil || read.Body != "Committed body" {
		t.Fatalf("coordination failure hid a committed memory: %v", err)
	}
	if _, err := store.Create(Memories, testUserFields(Memories, nil), "Blocked write"); err == nil {
		t.Fatal("unsafe write accepted")
	}
}
