package songya

import (
	"bytes"
	"context"
	"encoding/gob"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gofrs/flock"
)

func TestBatchFingerprintIsStableForCompoundMetadataKeys(t *testing.T) {
	request := BatchRequest{User: "test", Key: "compound-keys", Changes: []Change{{Fields: map[string]any{"values": map[[1]int]string{{1}: "One", {2}: "Two", {3}: "Three"}}}}}
	first, err := requestRevision(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	for range 100 {
		next, err := requestRevision(context.Background(), request)
		if err != nil || next != first {
			t.Fatal("unchanged supported metadata acquired a different retry fingerprint")
		}
	}
}

func TestImportRetrySurvivesRemovalOfSourceDirectory(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "notes")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	original := []byte("Retain the original account.")
	if err := fixtureWriteFile(filepath.Join(source, "old.md"), original, 0600); err != nil {
		t.Fatal(err)
	}
	request := ImportRequest{User: "test", Path: "old.md", Revision: dataRevision(original)}
	first, err := store.Import(context.Background(), source, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(source); err != nil {
		t.Fatal(err)
	}
	retried, err := store.Import(context.Background(), source, request)
	if err != nil || len(retried.Parts) != 1 || retried.Parts[0].ID != first.Parts[0].ID {
		t.Fatalf("preserved import could not resume without the external root: %+v %v", retried, err)
	}
}

func TestDiscoveryIncludesReadablePendingSingleCreation(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(store.path)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	id := "m_000000000000"
	name, _ := recordName(id)
	if err := fixtureRootMkdir(root, name, 0700); err != nil {
		t.Fatal(err)
	}
	raw := []byte("---\ntitle: Unpublished episode\n---\nUnpublished readable body")
	if _, err := stage(root, name, "", raw); err != nil {
		t.Fatal(err)
	}
	found, err := store.Search(context.Background(), Query{Text: "Unpublished"})
	if err != nil || len(found.Matches) != 1 || found.Matches[0].ID != id || !found.Matches[0].Pending || found.Matches[0].Revision != "" {
		t.Fatalf("durably retained body vanished from discovery: %+v %v", found, err)
	}
	source, err := store.ReadSource(name)
	if err != nil || !bytes.Equal(source, raw) {
		t.Fatal("retained raw source became unavailable")
	}
}

func TestHistoricalWithdrawalDoesNotBlockAnIndependentExactEdit(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	doc := createRecord(t, store, Memories, nil, "Original")
	request := BatchRequest{User: "test", Key: "historical-withdrawal", Changes: []Change{{ID: doc.ID, Revision: doc.Revision, Body: textPointer("Later user account")}}}
	root, err := os.OpenRoot(store.path)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	digest, _ := requestRevision(context.Background(), request)
	journal, err := prepareBatch(context.Background(), root, request, digest)
	if err != nil {
		t.Fatal(err)
	}
	persistJournal(t, root, journal)
	if _, err := store.AbandonBatch(context.Background(), request.Key); err != nil {
		t.Fatal(err)
	}
	entry := journal.Entries[0]
	// Restoring exact bytes from a backup is a valid independent external edit.
	if err := fixtureRootWriteFile(root, entry.Source, entry.After, 0600); err != nil {
		t.Fatal(err)
	}
	lock := flock.New(filepath.Join(store.path, ".lock"))
	if err := lock.Lock(); err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	current, err := store.Read(doc.ID)
	if err != nil || current.Body != "Later user account" {
		t.Fatalf("an unrelated busy writer made a historical proposal block healthy content: %+v %v", current, err)
	}
}

func TestStoryDetectsRecursionThroughRetiredAlias(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	story := createRecord(t, store, Stories, nil, "")
	alias := createRecord(t, store, Stories, map[string]any{"redirects": []string{story.ID}}, "Old story")
	body := "Head Tail"
	if _, err := store.Update(story.ID, story.Revision, map[string]any{"relations": []Relation{{"related_to", alias.ID}}}, body); err != nil {
		t.Fatal(err)
	}
	page, err := store.Compose(context.Background(), story.ID, 0, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(page.Text, "Head") != 1 || len(page.Diagnostics) == 0 {
		t.Fatalf("alias recursion duplicated canonical narrative: %q", page.Text)
	}
}

func TestMergeRetainsRichSubmittedAliases(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := createRecord(t, store, Entities, map[string]any{"name": "John"}, "First")
	b := createRecord(t, store, Entities, map[string]any{"name": "Johnny"}, "Second")
	receipt, err := store.Merge(context.Background(), MergeRequest{Key: "rich-aliases", Sources: []Version{{a.ID, a.Revision}, {b.ID, b.Revision}}, Fields: map[string]any{"aliases": map[string]any{"original": "Do not discard this unresolved name"}}})
	if err != nil {
		t.Fatal(err)
	}
	merged, err := store.Read(receipt.Refs["merged"])
	if err != nil {
		t.Fatal(err)
	}
	raw, err := merged.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "Do not discard this unresolved name") {
		t.Fatal("organization discarded submitted alias data")
	}
}

func TestExplicitRebuildDiscardsCachedInterpretations(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	createRecord(t, store, Memories, map[string]any{"title": "Authoritative title"}, "Account")
	catalogue, err := store.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	catalogue.Records[0].Title = "Old derived interpretation"
	var encoded bytes.Buffer
	if err := gob.NewEncoder(&encoded).Encode(catalogue.Records); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(struct {
		Format   int    `json:"format"`
		Checksum string `json:"checksum"`
		Records  []byte `json:"records"`
	}{indexFormat, dataRevision(encoded.Bytes()), encoded.Bytes()})
	if err != nil {
		t.Fatal(err)
	}
	if err := fixtureWriteFile(filepath.Join(store.path, ".index", "catalog.json"), payload, 0600); err != nil {
		t.Fatal(err)
	}
	cached, err := store.Scan(context.Background())
	if err != nil || cached.Records[0].Title != "Old derived interpretation" {
		t.Fatal("fixture did not install a valid prior cache")
	}
	rebuilt, err := store.Rebuild(context.Background())
	if err != nil || rebuilt.Records[0].Title != "Authoritative title" {
		t.Fatal("explicit rebuild reused a cached interpretation")
	}
}

func TestDamagedBatchJournalLeavesAnExplicitlyUnconfirmedReadableSnapshot(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	healthy := createRecord(t, store, Memories, nil, "Healthy committed account")
	changed := createRecord(t, store, Memories, nil, "Original account")
	request := BatchRequest{User: "test", Key: "damaged-journal", Changes: []Change{{ID: changed.ID, Revision: changed.Revision, Body: textPointer("Retained proposal"), Fields: map[string]any{"redirects": []string{healthy.ID}, "time": map[string]any{"earliest": "2090-01-01", "latest": "2090-01-01"}, "relations": []Relation{{Type: "after", Target: healthy.ID}}}}}}
	root, err := os.OpenRoot(store.path)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	digest, _ := requestRevision(context.Background(), request)
	journal, err := prepareBatch(context.Background(), root, request, digest)
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
	payload, _ := json.Marshal(journal)
	if err := fixtureRootWriteFile(root, filepath.Join(batchDirectory(request.Key), dataRevision(payload)+".json"), []byte("Damaged journal"), 0600); err != nil {
		t.Fatal(err)
	}
	current, err := store.Read(changed.ID)
	if err != nil || current.Body != "Retained proposal" {
		t.Fatal("damaged coordination hid intact content")
	}
	// An unavailable batch boundary cannot honestly supply a committed revision.
	if current.Revision != "" || !current.Unconfirmed || !strings.Contains(strings.Join(current.Diagnostics(), " "), "unconfirmed") {
		t.Fatal("unconfirmed publication was represented as committed")
	}
	other, err := store.Read(healthy.ID)
	if err != nil || other.Body != healthy.Body || other.Revision != healthy.Revision {
		t.Fatal("one damaged journal disabled a healthy record")
	}
	catalogue, err := store.Scan(context.Background())
	if err != nil || len(catalogue.Records) != 2 {
		t.Fatal("one damaged journal disabled discovery")
	}
	for _, record := range catalogue.Records {
		if record.ID == changed.ID && (!record.Unconfirmed || record.Revision != "") {
			t.Fatal("index claimed a damaged batch was committed")
		}
	}
	resolution, err := store.Resolve(context.Background(), changed.ID)
	if err != nil || !resolution.Unconfirmed || len(resolution.Targets) != 1 || resolution.Targets[0] != changed.ID {
		t.Fatal("unconfirmed retirement was treated as canonical")
	}
	graph, err := store.Graph(context.Background(), GraphQuery{ID: changed.ID, Direction: "outgoing"})
	if err != nil || len(graph.Edges) != 1 || !graph.Edges[0].Unconfirmed {
		t.Fatal("unconfirmed relationship lost its qualification")
	}
	timeline, err := store.Timeline(context.Background(), Query{Limit: 50})
	if err != nil || len(timeline.Constraints) != 0 {
		t.Fatal("unconfirmed metadata established chronology")
	}
}

func TestCancelledBatchDoesNotStartPreparationOrPublish(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	receipt, err := store.ApplyBatch(ctx, BatchRequest{User: "test", Key: "cancelled-before-preparation", Changes: []Change{{Body: textPointer("Do not publish")}}})
	if err != context.Canceled || receipt.Status != "not_accepted" {
		t.Fatalf("cancelled request entered preparation: %+v %v", receipt, err)
	}
	if _, err := os.Stat(filepath.Join(store.path, ".batches")); !os.IsNotExist(err) {
		t.Fatal("cancelled request created retained intent")
	}
}
