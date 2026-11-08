package songya

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestFolderConversionPreservesBytesMetadataHistoryAndResumes(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(t.TempDir())
	root, _ := os.OpenRoot(s.path)
	defer root.Close()
	raw := []byte("---\r\nuser: john\r\ntitle: keep exact\r\ncustom: {x: 3}\r\n---\r\nOriginal \xff bytes\r\n")
	for _, kind := range destinations {
		prefix, _ := kind.prefix()
		id := prefix + "000000000000"
		old := historicalRecordName(id)
		if err := root.WriteFile(old, raw, 0640); err != nil {
			t.Fatal(err)
		}
		when := time.Unix(1234567890, 123456789)
		if err := root.Chtimes(old, when, when); err != nil {
			t.Fatal(err)
		}
	}
	id := "m_000000000001"
	old := historicalRecordName(id)
	pending := pendingName(old)
	if err := root.Mkdir(pending, 0700); err != nil {
		t.Fatal(err)
	}
	if err := root.WriteFile(temporaryName(old, "", raw), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := root.Mkdir(filepath.Join(pending, "retained-empty"), 0700); err != nil {
		t.Fatal(err)
	}
	// Preserve old accepted receipt JSON exactly, including checksum-protected Source.
	request := BatchRequest{Key: "old-accepted", User: "john", Changes: []Change{{Kind: Memories, Body: pointer("A saved account")}}}
	revision, _ := requestRevision(ctx, request)
	journal, err := prepareBatch(ctx, root, request, revision)
	if err != nil {
		t.Fatal(err)
	}
	for i := range journal.Entries {
		journal.Entries[i].Source = historicalRecordName(journal.Entries[i].ID)
	}
	persistJournal(t, root, journal)
	entry := journal.Entries[0]
	if err := root.WriteFile(entry.Source, entry.After, 0600); err != nil {
		t.Fatal(err)
	}
	if err := mark(root, filepath.Join(batchDirectory(request.Key), "committed")); err != nil {
		t.Fatal(err)
	}
	if err := ensureDirectory(root, ".imports"); err != nil {
		t.Fatal(err)
	}
	if err := root.WriteFile(".imports/original.md", raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := ensureDirectory(root, ".layout-folders"); err != nil {
		t.Fatal(err)
	}
	inventory, err := inventoryLayout(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(inventory)
	if err := syncNew(root, layoutInventory, data); err != nil {
		t.Fatal(err)
	}
	// Crash state: the first canonical rename happened, but progress was not marked.
	if err := convertLayoutFile(root, inventory.Files[len(inventory.Files)-1]); err != nil {
		t.Fatal(err)
	}
	report, err := s.ConvertFolders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if report.Records != 5 || report.Pending != 1 || report.History < 3 {
		t.Fatalf("incomplete inventory %+v", report)
	}
	for _, file := range report.Files {
		if err := verifyLayoutFile(root, file.Target, file); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.ConvertFolders(ctx); err != nil {
		t.Fatal("second offline run", err)
	}
	receipt, err := s.ApplyBatch(ctx, request)
	if err != nil || receipt.Status != "committed" {
		t.Fatal(receipt, err)
	}
	retained, err := loadJournal(root, batchDirectory(request.Key))
	if err != nil || retained.Entries[0].Source != entry.Source {
		t.Fatal("receipt provenance changed", err)
	}
	if err := s.Recover(); err != nil {
		t.Fatal(err)
	}
	// The unknown empty pending subfolder remains at the new location, not deleted.
	canonical, _ := recordName(id)
	if _, err := root.Lstat(filepath.Join(pendingName(canonical), "retained-empty")); err != nil {
		t.Fatal(err)
	}
	for _, file := range report.Files {
		if file.Source == file.Target {
			if err := verifyLayoutFile(root, file.Target, file); err != nil {
				t.Fatal("history changed", err)
			}
		}
	}
}
func pointer(s string) *string { return &s }

func TestFileChangesAllKindsPreserveProseRetriesAndDeletion(t *testing.T) {
	ctx := context.Background()
	for _, kind := range destinations {
		t.Run(string(kind), func(t *testing.T) {
			s, _ := Open(t.TempDir())
			doc := createRecord(t, s, kind, map[string]any{"title": "John's files", "custom": map[string]any{"keep": 3}}, "Unchanged words.\n\nA second paragraph.")
			original, _ := s.Read(doc.ID)
			binary := bytes.Repeat([]byte{0, 255, 4, 7}, 1024*100)
			upload, err := s.Upload(ctx, "upload-one", "test", "資料.bin", bytes.NewReader(binary))
			if err != nil {
				t.Fatal(err)
			}
			same, err := s.Upload(ctx, "upload-one", "test", "資料.bin", bytes.NewReader(binary))
			if err != nil || same != upload {
				t.Fatal("lost upload ack", same, err)
			}
			request := FileChange{Key: "attach-one", ID: doc.ID, Revision: doc.Revision, Action: "add", Upload: upload.ID}
			receipt, err := s.ChangeFile(ctx, request)
			if err != nil || receipt.Status != "committed" || receipt.Filename != "資料.bin" {
				t.Fatal(receipt, err)
			}
			read, _ := s.Read(doc.ID)
			if read.Body != original.Body || read.Metadata().CreatedAt != original.Metadata().CreatedAt || read.Revision == original.Revision {
				t.Fatal("attachment changed prose/creation time or kept stale version")
			}
			if !bytes.Contains(read.OriginalSource(), []byte("keep: 3")) {
				t.Fatal("custom fields lost")
			}
			file, _, err := s.OpenAttachment(ctx, doc.ID, receipt.Filename)
			if err != nil {
				t.Fatal(err)
			}
			downloaded, _ := io.ReadAll(file)
			file.Close()
			if !bytes.Equal(downloaded, binary) {
				t.Fatal("bytes changed")
			}
			if pending, _ := s.Uploads(ctx, "test"); len(pending) != 0 {
				t.Fatal("attached bytes still listed as pending")
			}
			after, err := s.Update(doc.ID, read.Revision, nil, "Later prose.")
			if err != nil {
				t.Fatal(err)
			}
			retry, err := s.ChangeFile(ctx, request)
			if err != nil || retry != receipt {
				t.Fatal("lost file ack", retry, err)
			}
			latest, _ := s.Read(doc.ID)
			if latest.Body != after.Body {
				t.Fatal("retry overwrote later edits")
			}
			if _, err := s.ChangeFile(ctx, FileChange{Key: "stale", ID: doc.ID, Revision: doc.Revision, Action: "remove", Filename: receipt.Filename}); !errors.Is(err, ErrConflict) {
				t.Fatal("stale file mutation", err)
			}
			if err := s.Delete(ctx, doc.ID, latest.Revision); err != nil {
				t.Fatal(err)
			}
			if err := s.Recover(); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Read(doc.ID); !errors.Is(err, fs.ErrNotExist) {
				t.Fatal("record resurrected", err)
			}
			name, _ := recordName(doc.ID)
			if _, err := os.Stat(filepath.Join(s.path, filepath.Dir(name))); !errors.Is(err, fs.ErrNotExist) {
				t.Fatal("deleted folder or bytes remain live", err)
			}
		})
	}
}
func TestFileNamesReplacementRemovalAndUnsupportedReads(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(t.TempDir())
	doc := createRecord(t, s, Memories, nil, "John kept a note")
	for _, name := range []string{"../escape", "x/y", "x\\y", "\x00", ".."} {
		if _, err := s.Upload(ctx, name, "test", name, strings.NewReader("x")); err == nil {
			t.Fatal("accepted hostile filename", name)
		}
	}
	for i, name := range []string{"record.md", ".hidden", "日本語.txt", "日本語.txt"} {
		key := string(rune('a' + i))
		upload, err := s.Upload(ctx, key, "test", name, strings.NewReader("hello"))
		if err != nil {
			t.Fatal(err)
		}
		receipt, err := s.ChangeFile(ctx, FileChange{Key: "file-" + key, ID: doc.ID, Revision: doc.Revision, Action: "add", Upload: upload.ID})
		if err != nil {
			t.Fatal(err)
		}
		doc, _ = s.Read(doc.ID)
		if i == 0 && receipt.Filename != "record (file).md" {
			t.Fatal(receipt)
		}
		if i == 3 && receipt.Filename != "日本語 (2).txt" {
			t.Fatal(receipt)
		}
	}
	list, err := s.Attachments(ctx, doc.ID)
	if err != nil || len(list.Files) != 4 {
		t.Fatal(list, err)
	}
	text, next, err := s.ReadAttachmentText(ctx, doc.ID, "日本語.txt", 0, 3)
	if err != nil || text != "hel" || next != 3 {
		t.Fatal(text, next, err)
	}
	replacement, _ := s.Upload(ctx, "replacement", "test", "note.txt", strings.NewReader("Goodbye"))
	receipt, err := s.ChangeFile(ctx, FileChange{Key: "replace", ID: doc.ID, Revision: doc.Revision, Action: "replace", Filename: "日本語.txt", Upload: replacement.ID})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err = s.ChangeFile(ctx, FileChange{Key: "remove", ID: doc.ID, Revision: receipt.Revision, Action: "remove", Filename: "日本語.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.OpenAttachment(ctx, doc.ID, "日本語.txt"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	if _, _, err := s.OpenAttachment(ctx, doc.ID, "record.md"); err == nil {
		t.Fatal("canonical text exposed as attachment")
	}
	name, _ := recordName(doc.ID)
	if err := os.Symlink("/etc/passwd", filepath.Join(s.path, filepath.Dir(name), "unsafe.txt")); err != nil {
		t.Fatal(err)
	}
	listing, err := s.Attachments(ctx, doc.ID)
	if err != nil || len(listing.Files) != 3 || len(listing.Diagnostics) != 1 {
		t.Fatal("bad attachment hid healthy text/files", listing, err)
	}
	healthy, _ := s.Read(doc.ID)
	if healthy.Body != "John kept a note" {
		t.Fatal("prose changed")
	}
}

func TestInterruptedUploadRetryAndAcceptedFileChangeRecovery(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(t.TempDir())
	doc := createRecord(t, s, Memories, nil, "Mary's original words")
	// An interrupted request was never acknowledged as complete; retry keeps bytes.
	_, err := s.Upload(ctx, "interrupted", "test", "note.txt", io.MultiReader(strings.NewReader("partial"), errorReader{}))
	if err == nil {
		t.Fatal("interrupted upload acknowledged")
	}
	pending, _ := s.Uploads(ctx, "test")
	if len(pending) != 0 {
		t.Fatal("partial bytes exposed as completed upload")
	}
	upload, err := s.Upload(ctx, "interrupted", "test", "note.txt", strings.NewReader("A completed file"))
	if err != nil {
		t.Fatal(err)
	}
	root, _ := os.OpenRoot(s.path)
	defer root.Close()
	request := FileChange{Key: "interrupted-file", ID: doc.ID, Revision: doc.Revision, Action: "add", Upload: upload.ID}
	proposed, _ := s.Read(doc.ID)
	_ = proposed.Set("updated_at", time.Now().UTC())
	data, _ := proposed.Marshal()
	journal := fileJournal{Request: request, Filename: upload.Filename, AfterFile: upload.SHA256, Record: data}
	directory := fileOperation(request.Key)
	_ = ensureDirectory(root, ".file-ops")
	_ = ensureDirectory(root, directory)
	encoded, _ := json.Marshal(journal)
	if err := publishImmutable(root, filepath.Join(directory, dataRevision(encoded)+".json"), encoded); err != nil {
		t.Fatal(err)
	}
	if err := publishImmutable(root, filepath.Join(directory, "journal.json"), encoded); err != nil {
		t.Fatal(err)
	}
	name, _ := recordName(doc.ID)
	_ = ensureDirectory(root, pendingName(name))
	_ = syncNew(root, filepath.Join(pendingName(name), "files"), []byte(filepath.Base(directory)))
	// Crash after the attachment appeared, but before the timestamp or commit.
	uploadDir, _ := uploadDirectory(upload.ID)
	if err := root.Link(filepath.Join(uploadDir, "content"), filepath.Join(filepath.Dir(name), upload.Filename)); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, doc.ID, doc.Revision); err == nil {
		t.Fatal("deletion ignored accepted file change")
	}
	reopened, err := Open(s.path)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := reopened.Read(doc.ID)
	if err != nil || recovered.Body != doc.Body || recovered.Revision != dataRevision(data) {
		t.Fatal("accepted write did not recover", recovered, err)
	}
	receipt, err := reopened.ChangeFile(ctx, request)
	if err != nil || receipt.Status != "committed" {
		t.Fatal(receipt, err)
	}
	state, err := reopened.UploadState(ctx, upload.ID, "test")
	if err != nil || state.Status != "attached" || state.RecordID != doc.ID {
		t.Fatal(state, err)
	}
	list, err := reopened.Attachments(ctx, doc.ID)
	if err != nil || len(list.Files) != 1 || list.Pending {
		t.Fatal("duplicate or unfinished recovery", list, err)
	}
	// A long later edit and repeated recovery cannot replay the old timestamp/body.
	later, err := reopened.Update(doc.ID, recovered.Revision, nil, "Mary added another detail.")
	if err != nil {
		t.Fatal(err)
	}
	_ = reopened.Recover()
	actual, _ := reopened.Read(doc.ID)
	if actual.Revision != later.Revision {
		t.Fatal("recovery rolled back newer prose")
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("connection interrupted") }

func TestAttachmentMarkdownIsNotAnotherRecordAndStoryIncludesEmptyOrigins(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(t.TempDir())
	memory := createRecord(t, s, Memories, nil, "")
	nested := createRecord(t, s, Stories, map[string]any{"relations": []Relation{{"related_to", memory.ID}}}, "")
	outer := createRecord(t, s, Stories, map[string]any{"relations": []Relation{{"related_to", nested.ID}, {"related_to", nested.ID}}}, "")
	for _, doc := range []*Document{memory, nested, outer} {
		upload, err := s.Upload(ctx, doc.ID, "test", "notes.md", strings.NewReader("File owned by "+doc.ID))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.ChangeFile(ctx, FileChange{Key: "attach-" + doc.ID, ID: doc.ID, Revision: doc.Revision, Action: "add", Upload: upload.ID}); err != nil {
			t.Fatal(err)
		}
	}
	catalog, err := s.Scan(ctx)
	if err != nil || len(catalog.Records) != 3 {
		t.Fatal("attached Markdown was scanned as a record", catalog, err)
	}
	page, err := s.ComposeParts(ctx, outer.ID, 0, 8192)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, part := range page.Parts {
		counts[part.ID]++
	}
	if counts[outer.ID] != 1 || counts[nested.ID] != 2 || counts[memory.ID] != 2 {
		t.Fatal("empty/repeated origins disappeared", page.Parts)
	}
	for _, part := range page.Parts {
		files, err := s.Attachments(ctx, part.ID)
		if err != nil || len(files.Files) != 1 {
			t.Fatal(files, err)
		}
	}
}

func TestConversionKeepsAcceptedOldUncommittedBatchAndPendingUpdate(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(t.TempDir())
	root, _ := os.OpenRoot(s.path)
	defer root.Close()
	request := BatchRequest{Key: "old-interrupted-batch", User: "john", Changes: []Change{{Ref: "one", Kind: Memories, Body: pointer("John's accepted account")}, {Ref: "two", Kind: Memories, Body: pointer("Mary's accepted account")}}}
	digest, _ := requestRevision(ctx, request)
	journal, err := prepareBatch(ctx, root, request, digest)
	if err != nil {
		t.Fatal(err)
	}
	for i := range journal.Entries {
		journal.Entries[i].Source = historicalRecordName(journal.Entries[i].ID)
	}
	persistJournal(t, root, journal)
	for i, entry := range journal.Entries {
		if err := root.Mkdir(pendingName(entry.Source), 0700); err != nil {
			t.Fatal(err)
		}
		if err := syncNew(root, filepath.Join(pendingName(entry.Source), "batch"), []byte(filepath.Base(batchDirectory(request.Key)))); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			if err := root.WriteFile(entry.Source, entry.After, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	id := "m_000000000001"
	old := historicalRecordName(id)
	before := []byte("---\nuser: john\n---\nEarlier words")
	after := []byte("---\nuser: john\n---\nA correction with the original detail")
	if err := root.WriteFile(old, before, 0600); err != nil {
		t.Fatal(err)
	}
	if err := root.Mkdir(pendingName(old), 0700); err != nil {
		t.Fatal(err)
	}
	if err := syncNew(root, temporaryName(old, dataRevision(before), after), after); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConvertFolders(ctx); err != nil {
		t.Fatal(err)
	}
	opened, err := Open(s.path)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := opened.ApplyBatch(ctx, request)
	if err != nil || receipt.Status != "committed" || receipt.Refs["one"] != journal.Refs["one"] {
		t.Fatal(receipt, err)
	}
	updated, err := opened.Read(id)
	if err != nil || !bytes.Equal(updated.OriginalSource(), after) {
		t.Fatal("old update did not recover", updated, err)
	}
	for _, entry := range journal.Entries {
		read, err := opened.Read(entry.ID)
		if err != nil || !bytes.Equal(read.OriginalSource(), entry.After) {
			t.Fatal("accepted proposal changed", err)
		}
	}
}

func TestAcceptedFileJournalBeforeClaimWinsOverLaterStaleTextWrite(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(t.TempDir())
	doc := createRecord(t, s, Memories, nil, "Original account")
	upload, err := s.Upload(ctx, "preclaim-upload", "test", "note.txt", strings.NewReader("Original file"))
	if err != nil {
		t.Fatal(err)
	}
	root, _ := os.OpenRoot(s.path)
	defer root.Close()
	proposal, _ := s.Read(doc.ID)
	_ = proposal.Set("updated_at", time.Now().UTC())
	after, _ := proposal.Marshal()
	request := FileChange{Key: "preclaim", ID: doc.ID, Revision: doc.Revision, Action: "add", Upload: upload.ID}
	journal := fileJournal{Request: request, Filename: upload.Filename, AfterFile: upload.SHA256, Record: after}
	directory := fileOperation(request.Key)
	_ = ensureDirectory(root, ".file-ops")
	_ = ensureDirectory(root, directory)
	encoded, _ := json.Marshal(journal)
	if err := publishImmutable(root, filepath.Join(directory, dataRevision(encoded)+".json"), encoded); err != nil {
		t.Fatal(err)
	}
	if err := publishImmutable(root, filepath.Join(directory, "journal.json"), encoded); err != nil {
		t.Fatal(err)
	}
	// A stopped process had not created .record.md.pending yet. A later writer
	// still resolves accepted work before comparing the stale revision.
	if _, err := s.Update(doc.ID, doc.Revision, nil, "A concurrent edit"); !errors.Is(err, ErrConflict) {
		t.Fatal("accepted file change was bypassed", err)
	}
	latest, err := s.Read(doc.ID)
	if err != nil || latest.Body != "Original account" {
		t.Fatal(latest, err)
	}
	files, err := s.Attachments(ctx, doc.ID)
	if err != nil || len(files.Files) != 1 {
		t.Fatal(files, err)
	}
	if _, err := s.Update(doc.ID, latest.Revision, nil, "A concurrent edit"); err != nil {
		t.Fatal("current edit could not proceed", err)
	}
}

func TestDuplicateMaximumLengthFilenameGetsAUsableDistinctName(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(t.TempDir())
	doc := createRecord(t, s, Memories, nil, "John's files")
	filename := strings.Repeat("a", 251) + ".txt"
	for i := range 2 {
		key := fmt.Sprintf("long-name-%d", i)
		upload, err := s.Upload(ctx, key, "test", filename, strings.NewReader("Original bytes"))
		if err != nil {
			t.Fatal(err)
		}
		receipt, err := s.ChangeFile(ctx, FileChange{Key: key, ID: doc.ID, Revision: doc.Revision, Action: "add", Upload: upload.ID})
		if err != nil {
			t.Fatal(err)
		}
		if len(receipt.Filename) > 255 || !strings.HasSuffix(receipt.Filename, ".txt") {
			t.Fatal("unusable reported filename", receipt.Filename)
		}
		doc, _ = s.Read(doc.ID)
	}
	files, err := s.Attachments(ctx, doc.ID)
	if err != nil || len(files.Files) != 2 {
		t.Fatal(files, err)
	}
}

func TestLongUnicodeUploadNameKeepsValidRunesAndExtension(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(t.TempDir())
	doc := createRecord(t, s, Memories, nil, "Mary kept a note")
	name := strings.Repeat("界", 100) + ".txt"
	upload, err := s.Upload(ctx, "long-unicode", "test", name, strings.NewReader("Exact bytes"))
	if err != nil {
		t.Fatal(err)
	}
	if len(upload.Filename) > 255 || !utf8.ValidString(upload.Filename) || !strings.HasSuffix(upload.Filename, ".txt") {
		t.Fatal(upload)
	}
	receipt, err := s.ChangeFile(ctx, FileChange{Key: "long-unicode", ID: doc.ID, Revision: doc.Revision, Action: "add", Upload: upload.ID})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Filename != upload.Filename {
		t.Fatal("actual filename was not reported", receipt)
	}
	for _, hostile := range []string{strings.Repeat("a", 260) + "/secret.txt", strings.Repeat("a", 260) + "\x00.txt"} {
		if _, err := s.Upload(ctx, hostile, "test", hostile, strings.NewReader("Keep out")); err == nil {
			t.Fatal("truncation hid a hostile name")
		}
	}
}

func TestConsumedUploadCanBeReusedFromVerifiedRecordBytes(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(t.TempDir())
	first := createRecord(t, s, Memories, map[string]any{"title": "John's letter"}, "Original words.")
	second := createRecord(t, s, Memories, map[string]any{"title": "Mary's copy"}, "Other words.")
	upload, err := s.Upload(ctx, "original-upload", "test", "letter.txt", strings.NewReader("Exact original bytes"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.ChangeFile(ctx, FileChange{Key: "first", ID: first.ID, Revision: first.Revision, Action: "add", Upload: upload.ID})
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := uploadDirectory(upload.ID)
	if _, err := os.Stat(filepath.Join(s.path, dir, "content")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("staging not consumed", err)
	}
	receipt, err := s.ChangeFile(ctx, FileChange{Key: "copy", ID: second.ID, Revision: second.Revision, Action: "add", Upload: upload.ID, Filename: "copy.txt"})
	if err != nil || receipt.Status != "committed" {
		t.Fatal(receipt, err)
	}
	text, _, err := s.ReadAttachmentText(ctx, second.ID, "copy.txt", 0, 100)
	if err != nil || text != "Exact original bytes" {
		t.Fatal(text, err)
	}
	if _, err := os.Stat(filepath.Join(s.path, dir, "content")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("recovered staging not cleaned", err)
	}
	// A changed source must never be treated as the old uploaded bytes.
	name, _ := recordName(first.ID)
	if err := os.WriteFile(filepath.Join(s.path, filepath.Dir(name), "letter.txt"), []byte("Different bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	text, _, err = s.ReadAttachmentText(ctx, second.ID, "copy.txt", 0, 100)
	if err != nil || text != "Exact original bytes" {
		t.Fatal("copy shares the source inode", text, err)
	}
	current, _ := s.Read(second.ID)
	_, err = s.ChangeFile(ctx, FileChange{Key: "wrong-source", ID: second.ID, Revision: current.Revision, Action: "add", Upload: upload.ID})
	if err == nil || !strings.Contains(err.Error(), "original upload bytes") {
		t.Fatal("changed source reused", err)
	}
}
