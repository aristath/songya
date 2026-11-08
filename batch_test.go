package songya

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func textPointer(value string) *string { return &value }
func TestBatchCaptureRetriesAndResolvesLocalReferences(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	request := BatchRequest{User: "test", Key: "capture-one", Account: "I met John for coffee.", Changes: []Change{
		{Ref: "john", Kind: Entities, Fields: map[string]any{"name": "John", "aliases": []string{"Johnny"}}},
		{Ref: "music", Kind: Topics, Fields: map[string]any{"title": "Music"}},
		{Ref: "episode", Kind: Memories, Fields: map[string]any{"title": "Coffee", "relations": []any{map[string]any{"type": "related_to", "target": "$john"}, map[string]any{"type": "related_to", "target": "$music"}}}, Body: textPointer("An episode")},
		{Ref: "story", Kind: Stories, Fields: map[string]any{"title": "Our meetings", "relations": []Relation{{Type: "related_to", Target: "$episode"}}}},
	}}
	first, err := store.ApplyBatch(ctx, request)
	if err != nil || first.Status != "committed" || len(first.Refs) != 4 {
		t.Fatalf("capture failed: %+v, %v", first, err)
	}
	episode, err := store.Read(first.Refs["episode"])
	if err != nil {
		t.Fatal(err)
	}
	if episode.Metadata().Relations[0].Target != first.Refs["john"] || nodeStrings("original_accounts", episode)[0] != request.Account {
		t.Fatal("references or original account were lost")
	}
	story, err := store.Compose(ctx, first.Refs["story"], 0, 4096)
	if err != nil || story.Text != "An episode" {
		t.Fatalf("story batch membership failed: %+v, %v", story, err)
	}
	reopened, err := Open(store.path)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := reopened.ApplyBatch(ctx, request)
	if err != nil || retry.Refs["episode"] != episode.ID || !reflect.DeepEqual(retry.Records[0], first.Records[0]) {
		t.Fatal("restart/retry did not reuse durable receipt")
	}
	catalog, err := reopened.Scan(ctx)
	if err != nil || len(catalog.Records) != 4 {
		t.Fatal("retry duplicated records")
	}
	request.Account = "Different request"
	if _, err := reopened.ApplyBatch(ctx, request); err == nil {
		t.Fatal("same key accepted different work")
	}
}

func persistJournal(t *testing.T, root *os.Root, journal batchJournal) {
	t.Helper()
	if err := ensureDirectory(root, ".batches"); err != nil {
		t.Fatal(err)
	}
	directory := batchDirectory(journal.Key)
	if err := fixtureRootMkdir(root, directory, 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := syncNew(root, filepath.Join(directory, dataRevision(data)+".json"), data); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{directory, ".batches", "."} {
		if err := syncPath(root, path); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBatchPartialPublicationHasCoherentViewAndPreservesConflict(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	a := createRecord(t, store, Memories, map[string]any{"title": "A"}, "Old A")
	b := createRecord(t, store, Memories, map[string]any{"title": "B"}, "Old B")
	request := BatchRequest{User: "test", Key: "partial", Changes: []Change{{ID: a.ID, Revision: a.Revision, Body: textPointer("New A")}, {ID: b.ID, Revision: b.Revision, Body: textPointer("New B")}, {Ref: "new", Kind: Topics, Fields: map[string]any{"title": "New topic"}}}}
	digest, err := requestRevision(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(store.path)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	journal, err := prepareBatch(context.Background(), root, request, digest)
	if err != nil {
		t.Fatal(err)
	}
	persistJournal(t, root, journal)
	owner := []byte(filepath.Base(batchDirectory(request.Key)))
	for _, entry := range journal.Entries {
		if err := fixtureRootMkdir(root, pendingName(entry.Source), 0700); err != nil {
			t.Fatal(err)
		}
		if err := syncNew(root, filepath.Join(pendingName(entry.Source), "batch"), owner); err != nil {
			t.Fatal(err)
		}
		if entry.ID == a.ID || entry.Base == "" {
			if err := fixtureRootWriteFile(root, entry.Source, entry.After, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	read, err := store.Read(a.ID)
	if err != nil || read.Body != "Old A" || read.Revision != a.Revision {
		t.Fatalf("half a batch became visible: %+v, %v", read, err)
	}
	if _, err := store.Read(journal.Refs["new"]); err == nil {
		t.Fatal("new record was visible before batch commit")
	}
	catalog, err := store.Scan(ctx)
	if err != nil || len(catalog.Records) != 2 {
		t.Fatalf("discovery exposed a partial batch: %+v, %v", catalog, err)
	}
	name, _ := recordName(b.ID)
	user := []byte("User changed B")
	if err := fixtureRootWriteFile(root, name, user, 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.Recover(); err != nil {
		t.Fatal(err)
	}
	if current, err := root.ReadFile(name); err != nil || string(current) != string(user) {
		t.Fatal("recovery overwrote the user")
	}
	if marker(root, filepath.Join(batchDirectory(request.Key), "committed")) {
		t.Fatal("conflicted batch was labelled committed")
	}
	if len(store.Diagnostics()) == 0 {
		t.Fatal("blocked batch was not reported")
	}
	// Deliberately restore the base in this fixture, then let recovery finish.
	for _, entry := range journal.Entries {
		if entry.ID == b.ID {
			if err := fixtureRootWriteFile(root, entry.Source, entry.Before, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := store.Recover(); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{a.ID: "New A", b.ID: "New B"} {
		read, err := store.Read(id)
		if err != nil || read.Body != want {
			t.Fatal("batch did not recover")
		}
	}
	if !marker(root, filepath.Join(batchDirectory(request.Key), "committed")) {
		t.Fatal("recovered batch has no durable commit receipt")
	}
}

func TestBatchMetadataFaultsStayLocalAndIDsStayEngineOwned(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	cyclic := map[string]any{"fact": "Keep"}
	cyclic["self"] = cyclic
	receipt, err := store.ApplyBatch(ctx, BatchRequest{User: "test", Key: "imperfect", Changes: []Change{{Ref: "memory", Fields: map[string]any{"title": 3, "bad": func() {}, "cycle": cyclic}, Body: textPointer("Healthy body")}}})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := store.Read(receipt.Refs["memory"])
	if err != nil || doc.Body != "Healthy body" || doc.Metadata().Title != "3" || len(doc.Diagnostics()) == 0 {
		t.Fatal("metadata fault disabled useful capture")
	}
	if _, err := store.ApplyBatch(ctx, BatchRequest{User: "test", Key: "invented", Changes: []Change{{ID: "m_000000000000", Body: textPointer("Not an allocated ID")}}}); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("batch accepted a caller-assigned creation ID: %v", err)
	}
}

func TestStoryMembershipRespectsReplacementAppendAndExistingRevision(t *testing.T) {
	for _, mode := range []string{"append", "replace", "both", "membership-only"} {
		t.Run(mode, func(t *testing.T) {
			store, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			story := createRecord(t, store, Stories, nil, "Old narrative")
			member := createRecord(t, store, Memories, nil, "Member")
			change := Change{ID: story.ID, Revision: story.Revision}
			want := "Old narrative"
			if mode == "replace" || mode == "both" {
				change.Body = textPointer("Reviewed narrative")
				want = "Reviewed narrative"
			}
			if mode == "append" || mode == "both" {
				change.Append = "New detail"
				want += "\n\nNew detail"
			}
			change.Fields = map[string]any{"relations": []Relation{{Type: "related_to", Target: member.ID}}}
			request := BatchRequest{User: "test", Key: mode, Changes: []Change{change}}
			if _, err := store.ApplyBatch(context.Background(), request); err != nil {
				t.Fatal(err)
			}
			got, err := store.Read(story.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Body != want || len(storyMembers(got.Record(Stories))) != 1 {
				t.Fatalf("membership changed reviewed narrative: got %q want %q", got.Body, want)
			}
		})
	}
}

func TestCaptureRetainsRichOriginalAccounts(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	old := createRecord(t, store, Memories, map[string]any{"original_accounts": map[string]any{"raw": "First original", "note": "Keep this metadata"}}, "Body")
	if _, err := store.ApplyBatch(context.Background(), BatchRequest{User: "test", Key: "enrich-account", Account: "Second original", Changes: []Change{{ID: old.ID, Revision: old.Revision, Append: "New detail"}}}); err != nil {
		t.Fatal(err)
	}
	updated, err := store.Read(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := updated.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "First original") || !strings.Contains(string(data), "Keep this metadata") || !strings.Contains(string(data), "Second original") {
		t.Fatal("capture discarded an earlier rich account")
	}
}

func TestOpenRecoversAcceptedCreationAfterItsDirectoryDisappears(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	request := BatchRequest{User: "test", Key: "missing-directory", Changes: []Change{{Ref: "topic", Kind: Topics, Body: textPointer("Retained topic")}}}
	digest, _ := requestRevision(context.Background(), request)
	root, err := os.OpenRoot(store.path)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	journal, err := prepareBatch(context.Background(), root, request, digest)
	if err != nil {
		t.Fatal(err)
	}
	persistJournal(t, root, journal)
	if err := root.Remove("topics"); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(store.path)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := reopened.Read(journal.Refs["topic"])
	if err != nil || doc.Body != "Retained topic" {
		t.Fatalf("one open did not repair and recover the missing directory: %+v %v", doc, err)
	}
}
