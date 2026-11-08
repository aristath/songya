package songya

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestAbandonBatchRestoresOwnBytesAndKeepsExternalEditsAndProposals(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	a := createRecord(t, store, Memories, nil, "Old A")
	b := createRecord(t, store, Memories, nil, "Old B")
	request := BatchRequest{User: "test", Key: "withdraw", Account: "Preserve this original account", Changes: []Change{
		{ID: a.ID, Revision: a.Revision, Body: textPointer("New A")},
		{ID: b.ID, Revision: b.Revision, Body: textPointer("New B")},
		{Ref: "new", Kind: Topics, Body: textPointer("New topic")},
	}}
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
	for _, entry := range journal.Entries {
		if err := fixtureRootMkdir(root, pendingName(entry.Source), 0700); err != nil {
			t.Fatal(err)
		}
		if err := syncNew(root, filepath.Join(pendingName(entry.Source), "batch"), []byte(filepath.Base(batchDirectory(request.Key)))); err != nil {
			t.Fatal(err)
		}
		if err := fixtureRootWriteFile(root, entry.Source, entry.After, 0600); err != nil {
			t.Fatal(err)
		}
	}
	name, _ := recordName(b.ID)
	if err := fixtureRootWriteFile(root, name, []byte("External B"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := store.AbandonBatch(ctx, request.Key)
	if err != nil || result.Status != "abandoned" {
		t.Fatalf("withdrawal failed: %+v %v", result, err)
	}
	old, err := store.Read(a.ID)
	if err != nil || old.Body != a.Body {
		t.Fatal("own publication was not restored")
	}
	external, err := root.ReadFile(name)
	if err != nil || string(external) != "External B" {
		t.Fatal("external edit was replaced")
	}
	if _, err := store.Read(journal.Refs["new"]); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("withdrawn creation remained active: %v", err)
	}
	proposed, err := store.BatchData(ctx, request.Key, a.ID, "proposed")
	if err != nil || Parse(proposed).Body != "New A" {
		t.Fatal("proposal was discarded")
	}
	account, err := store.BatchData(ctx, request.Key, "", "account")
	if err != nil || string(account) != request.Account {
		t.Fatal("original account was discarded")
	}
	// Reintroducing the exact former proposal later must not cause a terminal
	// withdrawal to roll it back again during retry or automatic recovery.
	if _, err := store.Update(a.ID, old.Revision, nil, "New A"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AbandonBatch(ctx, request.Key); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(store.path)
	if err != nil {
		t.Fatal(err)
	}
	retried, err := reopened.ApplyBatch(ctx, request)
	if err != nil || retried.Status != "abandoned" {
		t.Fatal("withdrawn key was not terminal")
	}
	current, err := reopened.Read(a.ID)
	if err != nil || current.Body != "New A" {
		t.Fatal("withdrawal replayed after reaching a terminal state")
	}
	status, err := reopened.BatchStatus(ctx, request.Key)
	if err != nil || status.Status != "abandoned" {
		t.Fatal("withdrawal receipt unavailable")
	}
}

func TestInterruptedWithdrawalRecoversWithoutPublishing(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	original := createRecord(t, store, Memories, nil, "Original")
	request := BatchRequest{User: "test", Key: "interrupted-withdrawal", Changes: []Change{{ID: original.ID, Revision: original.Revision, Body: textPointer("Proposal")}}}
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
	if err := mark(root, filepath.Join(batchDirectory(request.Key), "abandoning")); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(store.path)
	if err != nil {
		t.Fatal(err)
	}
	current, err := reopened.Read(original.ID)
	if err != nil || current.Body != original.Body {
		t.Fatal("withdrawal did not recover")
	}
	status, err := reopened.BatchStatus(context.Background(), request.Key)
	if err != nil || status.Status != "abandoned" {
		t.Fatal("withdrawal did not reach durable completion")
	}
}

func TestCommittedBatchCannotBeWithdrawn(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	receipt, err := store.ApplyBatch(ctx, BatchRequest{User: "test", Key: "committed", Changes: []Change{{Ref: "memory", Body: textPointer("Keep")}}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.AbandonBatch(ctx, receipt.Key)
	if err == nil || result.Status != "committed" {
		t.Fatal("committed batch was withdrawn")
	}
	doc, err := store.Read(receipt.Refs["memory"])
	if err != nil || doc.Body != "Keep" {
		t.Fatal("committed memory lost")
	}
}

func TestConcurrentReadCannotExposeWithdrawnPublication(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	original := createRecord(t, store, Memories, nil, "Original")
	request := BatchRequest{User: "test", Key: "withdrawn-read", Changes: []Change{{ID: original.ID, Revision: original.Revision, Body: textPointer("Never committed")}}}
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
	// The read obtained these bytes during publication, then withdrawal completed
	// before it inspected the now-removed claim. Such a snapshot was never committed.
	stale := journal.Entries[0].After
	if _, err := store.AbandonBatch(ctx, request.Key); err != nil {
		t.Fatal(err)
	}
	if data, err := busyBatchView(root, journal.Entries[0].Source, stale, nil); err == nil && Parse(data).Body == "Never committed" {
		t.Fatal("withdrawn proposal leaked through a concurrent read")
	}
	// Ordinary reads take a stable view and remain available after withdrawal.
	doc, err := store.Read(original.ID)
	if err != nil || doc.Body != original.Body {
		t.Fatal("stable record became unavailable")
	}
}
