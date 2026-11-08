package songya

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSourceEditingDurableRetryFullMetadataAndClocks(t *testing.T) {
	s, _ := Open(t.TempDir())
	ctx := context.Background()
	for _, kind := range destinations {
		source := "---\nuser: 3\ntitle: First\ncustom:\n  values: [yes, 3, false]\nobsolete: remove\ncreated_at: 1900-01-01\nupdated_at: 1900-01-01\n---\n# Original\n\nLiteral [[m_000000000000]]\n"
		key := "create-" + string(kind)
		receipt, err := s.SaveSource(ctx, key, "", kind, "", source)
		if err != nil {
			t.Fatal(err)
		}
		retry, err := s.SaveSource(ctx, key, "", kind, "", source)
		if err != nil || retry.Refs["record"] != receipt.Refs["record"] {
			t.Fatal("creation retry", retry, err)
		}
		id := receipt.Refs["record"]
		doc, _ := s.Read(id)
		created := doc.Metadata().CreatedAt
		if doc.Metadata().Title != "First" {
			t.Fatal("source creation lost title")
		}
		if created.Year() == 1900 || created.IsZero() {
			t.Fatal("caller assigned clock")
		}
		updated := "---\nuser: 3\ntitle: Changed\ncustom:\n  values: [yes, 4, false]\ncreated_at: 1901-01-01\n---\n**Edited**\n"
		receipt, err = s.SaveSource(ctx, "edit-"+string(kind), id, kind, doc.Revision, updated)
		if err != nil {
			t.Fatal(err)
		}
		next, _ := s.Read(id)
		if next.Metadata().CreatedAt != created || next.Metadata().Title != "Changed" || next.Body != "**Edited**\n" {
			t.Fatal("snapshot", next)
		}
		var fields map[string]any
		_ = next.Frontmatter.Decode(&fields)
		if _, ok := fields["obsolete"]; ok {
			t.Fatal("full source retained deleted field")
		}
		if _, ok := fields["custom"]; !ok {
			t.Fatal("nested metadata lost")
		}
		if _, err := s.SaveSource(ctx, "stale-"+string(kind), id, kind, doc.Revision, source); !errors.Is(err, ErrConflict) {
			t.Fatal("stale source overwrote data", err)
		}
		if _, err := s.SaveSource(ctx, "edit-"+string(kind), id, kind, doc.Revision, updated); err != nil {
			t.Fatal("same committed receipt failed", err)
		}
	}
	reopened, _ := Open(s.path)
	c, _ := reopened.Scan(ctx)
	if len(c.Records) != 4 {
		t.Fatal("restart lost or duplicated records", c)
	}
}

func TestDeleteChecksAllIncomingFormsAndDoesNotRecoverDeletedRecord(t *testing.T) {
	s, _ := Open(t.TempDir())
	ctx := context.Background()
	for _, kind := range destinations {
		t.Run(string(kind), func(t *testing.T) {
			target, _ := s.Create(kind, testUserFields(kind, map[string]any{"title": "Target"}), "Target body")
			fields := map[string]any{"user": "ari", "relations": []Relation{{Type: "caused_by", Target: target.ID}}}
			incoming, _ := s.Create(Memories, fields, "A memory [["+target.ID+"]]")
			story, _ := s.Create(Stories, map[string]any{"user": "ari", "relations": []Relation{{"related_to", target.ID}}}, "A narrative")
			before, _ := s.Read(target.ID)
			var blocked *DeleteBlocked
			if err := s.Delete(ctx, target.ID, before.Revision); !errors.As(err, &blocked) || len(blocked.Records) != 2 {
				t.Fatal("missing incoming blockers", blocked, err)
			}
			if _, err := s.Update(incoming.ID, incoming.Revision, map[string]any{"relations": []Relation{}}, "Links removed"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Update(story.ID, story.Revision, map[string]any{"relations": []Relation{}}, "Membership removed"); err != nil {
				t.Fatal(err)
			}
			// A committed batch receipt must not recreate this record on normal recovery.
			receipt, err := s.SaveSource(ctx, "target-edit-"+string(kind), target.ID, kind, before.Revision, "---\nuser: ari\ntitle: Target\n---\nTarget")
			if err != nil {
				t.Fatal(err)
			}
			target, _ = s.Read(receipt.Refs["record"])
			if err := s.Delete(ctx, target.ID, before.Revision); !errors.Is(err, ErrConflict) {
				t.Fatal("stale deletion", err)
			}
			if err := s.Delete(ctx, target.ID, target.Revision); err != nil {
				t.Fatal(err)
			}
			reopened, _ := Open(s.path)
			if _, err := reopened.Read(target.ID); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("deleted record recovered", err)
			}
			if err := reopened.Delete(ctx, target.ID, target.Revision); err != nil {
				t.Fatal("delete retry", err)
			}
		})
	}
}

func TestDeleteKeepsHealthyOperationsWhenSourcesOrCacheFail(t *testing.T) {
	s, _ := Open(t.TempDir())
	ctx := context.Background()
	target, _ := s.Create(Entities, map[string]any{"name": "Alex"}, "Identity")
	// A broken derived cache cannot block deletion of healthy authoritative records.
	_ = os.RemoveAll(filepath.Join(s.path, ".index"))
	_ = fixtureWriteFile(filepath.Join(s.path, ".index"), []byte("blocked cache"), 0600)
	if err := s.Delete(ctx, target.ID, target.Revision); err != nil {
		t.Fatal("cache blocked deletion", err)
	}
	target, _ = s.Create(Entities, map[string]any{"name": "Sam"}, "Identity")
	_ = os.Rename(filepath.Join(s.path, "stories"), filepath.Join(s.path, "held-stories"))
	var blocked *DeleteBlocked
	if err := s.Delete(ctx, target.ID, target.Revision); !errors.As(err, &blocked) || len(blocked.Diagnostics) == 0 {
		t.Fatal("unreadable source became authoritative empty", err)
	}
	if _, err := s.Read(target.ID); err != nil {
		t.Fatal("deletion failure disabled read", err)
	}
	if _, err := s.Create(Topics, map[string]any{"name": "Music"}, "Subject"); err != nil {
		t.Fatal("deletion failure disabled write", err)
	}
}

func TestDeleteWaitsForConcurrentWriterAndSeesItsIncomingLink(t *testing.T) {
	s, _ := Open(t.TempDir())
	target, _ := s.Create(Entities, nil, "")
	ctx := context.Background()
	locked := make(chan struct{})
	release := make(chan struct{})
	writer := make(chan error, 1)
	go func() {
		writer <- s.lockedContext(ctx, func(root *os.Root) error {
			close(locked)
			<-release
			doc, _ := NewDocument(Metadata{User: "ari", CreatedAt: time.Now(), UpdatedAt: time.Now(), Relations: []Relation{{"related_to", target.ID}}}, "Concurrent [["+target.ID+"]]")
			data, _ := doc.Marshal()
			if err := fixtureRootWriteFile(root, "memories/m_000000000001/record.md", data, 0600); err != nil {
				return err
			}
			return syncPath(root, "memories")
		})
	}()
	<-locked
	other, _ := OpenExisting(s.path)
	deletion := make(chan error, 1)
	go func() { deletion <- other.Delete(ctx, target.ID, target.Revision) }()
	select {
	case err := <-deletion:
		t.Fatal("deletion bypassed writer lock", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if err := <-writer; err != nil {
		t.Fatal(err)
	}
	var blocked *DeleteBlocked
	if err := <-deletion; !errors.As(err, &blocked) || len(blocked.Records) != 1 {
		t.Fatal("concurrent reference missed", err)
	}
}

func TestConnectionsPreserveUnrelatedMetadata(t *testing.T) {
	d := Parse([]byte("---\ncustom: {a: [3, false]}\nrelations:\n - {type: related_to, target: e_000000000000}\n---\nText"))
	relation := Relation{Type: "related_to", Target: "m_000000000000"}
	if err := d.EditConnection(relation, false); err != nil {
		t.Fatal(err)
	}
	if len(d.Metadata().Relations) != 2 || d.Body != "Text" {
		t.Fatal("reference absent")
	}
	if err := d.EditConnection(relation, true); err != nil {
		t.Fatal(err)
	}
	if d.Body != "Text" || metadataField(&d.Frontmatter, "custom") == nil || len(d.Metadata().Relations) != 1 {
		t.Fatal("connection edit lost data")
	}
}

func TestDeletionChecksUnpublishedProposalsAndRetainsPendingWork(t *testing.T) {
	ctx := context.Background()
	s, _ := Open(t.TempDir())
	target, _ := s.Create(Entities, nil, "")
	incoming, _ := s.Create(Memories, map[string]any{"user": "ari"}, "Original")
	doc, _ := s.Read(incoming.ID)
	doc.Body = "Pending"
	_ = doc.Set("relations", []Relation{{"related_to", target.ID}})
	data, _ := doc.Marshal()
	name, _ := recordName(incoming.ID)
	if err := s.lockedContext(ctx, func(root *os.Root) error { _, err := stage(root, name, incoming.Revision, data); return err }); err != nil {
		t.Fatal(err)
	}
	var blocked *DeleteBlocked
	if err := s.Delete(ctx, target.ID, target.Revision); !errors.As(err, &blocked) || len(blocked.Records) != 1 {
		t.Fatal("pending incoming update missed", err)
	}
	if _, err := os.Stat(filepath.Join(s.path, pendingName(name))); err != nil {
		t.Fatal("pending work removed")
	}
	if err := s.Delete(ctx, incoming.ID, incoming.Revision); !errors.As(err, &blocked) {
		t.Fatal("unfinished target deleted", err)
	}
	// A journal can exist durably before publication creates its claims.
	separate, _ := Open(t.TempDir())
	other, _ := separate.Create(Entities, nil, "")
	body := "Unpublished [[" + other.ID + "]]"
	request := BatchRequest{Key: "unpublished", User: "ari", Changes: []Change{{Kind: Memories, Body: &body, Fields: map[string]any{"relations": []Relation{{"related_to", other.ID}}}}}}
	if err := separate.lockedContext(ctx, func(root *os.Root) error {
		digest, _ := requestRevision(ctx, request)
		journal, err := prepareBatch(ctx, root, request, digest)
		if err != nil {
			return err
		}
		dir := batchDirectory(request.Key)
		if err := fixtureRootMkdir(root, ".batches", 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		if err := fixtureRootMkdir(root, dir, 0700); err != nil {
			return err
		}
		encoded, err := json.Marshal(journal)
		if err != nil {
			return err
		}
		return syncNew(root, filepath.Join(dir, dataRevision(encoded)+".json"), encoded)
	}); err != nil {
		t.Fatal(err)
	}
	if err := separate.Delete(ctx, other.ID, other.Revision); !errors.As(err, &blocked) || len(blocked.Records) != 1 {
		t.Fatal("unpublished journal link missed", err)
	}
}

func TestDirectConnectionsAreCompleteAndKeepOriginalRedirectTargets(t *testing.T) {
	s, _ := Open(t.TempDir())
	ctx := context.Background()
	target, _ := s.Create(Entities, nil, "")
	replacement, _ := s.Create(Entities, nil, "")
	for range 65 {
		if _, err := s.Create(Memories, map[string]any{"user": "ari", "relations": []Relation{{"related_to", target.ID}}}, "Linked"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Create(Topics, map[string]any{"redirects": []string{target.ID}}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(target.ID, target.Revision, map[string]any{"redirects": []string{replacement.ID}}, ""); err != nil {
		t.Fatal(err)
	}
	c, err := s.Scan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	links := c.Connections(target.ID)
	if len(links) != 67 {
		t.Fatal("connections clipped or rewritten", len(links))
	}
}

func TestDeleteIgnoresOwnOutgoingAndSelfLinks(t *testing.T) {
	s, _ := Open(t.TempDir())
	other, _ := s.Create(Entities, nil, "")
	record, _ := s.Create(Memories, map[string]any{"user": "ari"}, "")
	record, err := s.Update(record.ID, record.Revision, map[string]any{"relations": []Relation{{Type: "related_to", Target: record.ID}, {Type: "related_to", Target: other.ID}}}, "[["+record.ID+"]] [["+other.ID+"]]")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(context.Background(), record.ID, record.Revision); err != nil {
		t.Fatal("own outgoing links blocked deletion", err)
	}
	if _, err := s.Read(other.ID); err != nil {
		t.Fatal("deletion cascaded", err)
	}
}

func TestSourceBodyIsLiteralAndPreservesOpenYAML(t *testing.T) {
	s, _ := Open(t.TempDir())
	source := "---\nuser: ari\ntitle: 3\ndefaults: &anchor {nested: [3, true]}\ncustom: *anchor\n---\nLiteral example [[$record]]\n\n```yaml\n[[$record]]\n```\n"
	receipt, err := s.SaveSource(context.Background(), "literal-source", "", Memories, "", source)
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.Read(receipt.Refs["record"])
	if err != nil {
		t.Fatal(err)
	}
	if d.Body != Parse([]byte(source)).Body {
		t.Fatal("source editor expanded literal text", d.Body)
	}
	var fields map[string]any
	if err := d.Frontmatter.Decode(&fields); err != nil {
		t.Fatal(err)
	}
	if fields["custom"] == nil || fields["defaults"] == nil || d.Metadata().Title != "3" {
		t.Fatal("open metadata lost", fields)
	}
}
