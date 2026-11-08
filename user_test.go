package songya

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUsersAreRequiredOnOwnedRecordsAndAreNotPermissions(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []Destination{Memories, Stories} {
		for _, user := range []any{nil, "", "  ", "all", []any{"ari"}, map[string]any{"name": "ari"}} {
			if doc, err := store.Create(kind, map[string]any{"user": user}, "Retain at caller"); err == nil || doc.ID != "" || doc.Pending {
				t.Fatalf("%s accepted an unknown owner: %#v", kind, user)
			}
		}
	}
	if catalog, err := store.Scan(context.Background()); err != nil || len(catalog.Records) != 0 {
		t.Fatal("invalid ownership left published records")
	}
	for _, kind := range []Destination{Entities, Topics} {
		if _, err := store.Create(kind, nil, "Shared"); err != nil {
			t.Fatal(err)
		}
	}
	doc, err := store.Create(Memories, map[string]any{"user": 3}, "Numeric user")
	if err != nil || doc.Metadata().User != "3" {
		t.Fatalf("equivalent scalar rejected: %+v, %v", doc, err)
	}
	if _, err := store.Update(doc.ID, doc.Revision, map[string]any{"user": nil}, "Uncommitted"); err == nil {
		t.Fatal("update removed required ownership")
	}
	read, err := store.Read(doc.ID)
	if err != nil || read.Body != "Numeric user" || read.Revision != doc.Revision {
		t.Fatal("invalid update changed the committed record")
	}
	changed, err := store.Update(doc.ID, doc.Revision, map[string]any{"user": "any-other-user"}, doc.Body)
	if err != nil || changed.Metadata().User != "any-other-user" {
		t.Fatalf("ownership was treated as a permission: %v", err)
	}
}

func TestUserFilteringKeepsSharedRecordsAndRefreshesOwnership(t *testing.T) {
	ctx := context.Background()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	person := createRecord(t, store, Entities, map[string]any{"name": "John"}, "Shared person")
	createRecord(t, store, Topics, map[string]any{"name": "Coffee"}, "Shared topic")
	ari := createRecord(t, store, Memories, map[string]any{"user": "ari", "title": "Coffee", "time": map[string]any{"earliest": "2000-01-01"}, "relations": []Relation{{Type: "with", Target: person.ID}}}, "Ari's account")
	bob := createRecord(t, store, Memories, map[string]any{"user": "bob", "title": "Coffee", "time": map[string]any{"latest": "2001-01-01"}, "relations": []Relation{{Type: "with", Target: person.ID}}}, "Bob's account")
	createRecord(t, store, Stories, map[string]any{"user": "ari", "title": "Meetings"}, "[["+ari.ID+"]]")
	createRecord(t, store, Stories, map[string]any{"user": "bob", "title": "Meetings"}, "[["+bob.ID+"]]")
	person, err = store.Update(person.ID, person.Revision, map[string]any{"user": "bob", "relations": []Relation{{Type: "remembers", Target: bob.ID}}}, person.Body)
	if err != nil {
		t.Fatal(err)
	}
	graph, err := store.Graph(ctx, GraphQuery{User: "ari", ID: person.ID, Direction: "outgoing"})
	if err != nil || len(graph.Edges) != 1 || !graph.Edges[0].Resolved {
		t.Fatalf("a shared reference was reported unavailable by a user filter: %+v, %v", graph, err)
	}
	for user, count := range map[string]int{"ari": 4, "bob": 4, "all": 6, "": 6, "unknown": 2} {
		result, err := store.Search(ctx, Query{User: user, Limit: 50})
		if err != nil || len(result.Matches) != count {
			t.Fatalf("user %q: %+v, %v", user, result, err)
		}
		for _, match := range result.Matches {
			if user != "" && user != "all" && userOwned(match.Kind) && match.User != user {
				t.Fatalf("another user's record entered the filtered result: %+v", match)
			}
		}
	}
	prepared, err := store.Prepare(ctx, "John Coffee Meetings", "ari", 50)
	if err != nil || len(prepared.Matches) != 4 || len(prepared.Ambiguities) != 0 {
		t.Fatalf("preparation mixed users: %+v, %v", prepared, err)
	}
	for user, count := range map[string]int{"ari": 1, "all": 2} {
		graph, err := store.Graph(ctx, GraphQuery{User: user, ID: person.ID, Direction: "incoming"})
		if err != nil || len(graph.Edges) != count {
			t.Fatalf("backlinks mixed users: %+v, %v", graph, err)
		}
		timeline, err := store.Timeline(ctx, Query{User: user, Kind: Memories})
		if err != nil || len(timeline.Records) != count {
			t.Fatalf("timeline mixed users: %+v, %v", timeline, err)
		}
	}
	// Explicit IDs stay readable: selecting a user is not authorization.
	if other, err := store.Read(bob.ID); err != nil || other.Metadata().User != "bob" {
		t.Fatal("direct access was restricted")
	}
	changed, err := store.Update(ari.ID, ari.Revision, map[string]any{"user": "bob"}, ari.Body)
	if err != nil {
		t.Fatal(err)
	}
	for _, rebuild := range []bool{false, true} {
		if rebuild {
			if _, err := store.Rebuild(ctx); err != nil {
				t.Fatal(err)
			}
		}
		found, err := store.Search(ctx, Query{User: "bob", Kind: Memories})
		if err != nil || len(found.Matches) != 2 {
			t.Fatalf("ownership cache stayed stale: %+v, %v", found, err)
		}
	}
	read, _ := store.Read(changed.ID)
	if read.Metadata().User != "bob" {
		t.Fatal("catalogue changed the authoritative owner")
	}
}

func TestBatchesRequireOwnersBeforeAcceptanceAndRetainThemOnRetry(t *testing.T) {
	ctx := context.Background()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	invalid := BatchRequest{Key: "incomplete", Changes: []Change{
		{Ref: "owned", Fields: map[string]any{"user": "ari"}}, {Ref: "missing"},
	}}
	if receipt, err := store.ApplyBatch(ctx, invalid); err == nil || receipt.Status == "committed" {
		t.Fatal("partially owned batch accepted")
	}
	catalog, _ := store.Scan(ctx)
	if len(catalog.Records) != 0 {
		t.Fatal("rejected batch published a subset")
	}
	request := BatchRequest{User: "ari", Key: "related", Changes: []Change{
		{Ref: "person", Kind: Entities}, {Ref: "topic", Kind: Topics},
		{Ref: "memory", Body: textPointer("Ari")}, {Ref: "story", Kind: Stories, Fields: map[string]any{"relations": []Relation{{Type: "related_to", Target: "$memory"}}}},
		{Ref: "other", Fields: map[string]any{"user": "bob"}},
	}}
	first, err := store.ApplyBatch(ctx, request)
	if err != nil || first.Status != "committed" {
		t.Fatalf("capture failed: %+v, %v", first, err)
	}
	for ref, user := range map[string]string{"person": "", "topic": "", "memory": "ari", "story": "ari", "other": "bob"} {
		doc, err := store.Read(first.Refs[ref])
		if err != nil || doc.Metadata().User != user {
			t.Fatalf("wrong owner for %s: %+v %v", ref, doc, err)
		}
	}
	request.User = "bob"
	if _, err := store.ApplyBatch(ctx, request); err == nil {
		t.Fatal("same key accepted different ownership")
	}
	request.User = "ari"
	reopened, _ := Open(store.path)
	retried, err := reopened.ApplyBatch(ctx, request)
	if err != nil || retried.Refs["memory"] != first.Refs["memory"] {
		t.Fatal("retry lost ownership identity")
	}
}

func TestIgnoredSharedRecordUserStillBindsTheExactRetryIntent(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	request := BatchRequest{User: string([]byte{255}), Key: "shared", Changes: []Change{{Kind: Entities}}}
	if _, err := store.ApplyBatch(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	request.User = string([]byte{254})
	if _, err := store.ApplyBatch(context.Background(), request); err == nil {
		t.Fatal("different request user bytes reused an accepted intent")
	}
}

func TestUserIDsAreLiteralEvenWhenTheyMatchALocalRecordReference(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := store.ApplyBatch(context.Background(), BatchRequest{User: "$john", Key: "literal-user", Changes: []Change{
		{Ref: "john", Kind: Entities}, {Ref: "default"}, {Ref: "explicit", Fields: map[string]any{"user": "$john"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"default", "explicit"} {
		doc, _ := store.Read(receipt.Refs[ref])
		if doc.Metadata().User != "$john" {
			t.Fatal("record references changed the user ID")
		}
	}
}

func TestOrganizationInheritsOwnersAndRequiresAChoiceAcrossUsers(t *testing.T) {
	ctx := context.Background()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := createRecord(t, store, Memories, map[string]any{"user": "ari"}, "A")
	b := createRecord(t, store, Memories, map[string]any{"user": "ari"}, "B")
	c := createRecord(t, store, Memories, map[string]any{"user": "bob"}, "C")
	merged, err := store.Merge(ctx, MergeRequest{Key: "same", Sources: []Version{{a.ID, a.Revision}, {b.ID, b.Revision}}})
	if err != nil {
		t.Fatal(err)
	}
	doc, _ := store.Read(merged.Refs["merged"])
	if doc.Metadata().User != "ari" {
		t.Fatal("merge lost the common owner")
	}
	mixed := MergeRequest{Key: "mixed", Sources: []Version{{doc.ID, doc.Revision}, {c.ID, c.Revision}}}
	if _, err := store.Merge(ctx, mixed); err == nil {
		t.Fatal("cross-user merge guessed an owner")
	}
	mixed.Fields = map[string]any{"user": "bob"}
	merged, err = store.Merge(ctx, mixed)
	if err != nil {
		t.Fatal(err)
	}
	doc, _ = store.Read(merged.Refs["merged"])
	parts, err := store.Split(ctx, SplitRequest{Key: "parts", Source: Version{doc.ID, doc.Revision}, Parts: []Part{{Body: "Inherited"}, {Fields: map[string]any{"user": "ari"}, Body: "Explicit"}}})
	if err != nil {
		t.Fatal(err)
	}
	for ref, user := range map[string]string{"part_1": "bob", "part_2": "ari"} {
		part, _ := store.Read(parts.Refs[ref])
		if part.Metadata().User != user {
			t.Fatalf("split owner %s: %q", ref, part.Metadata().User)
		}
	}
}

func TestDamagedOwnershipStaysReadableAndNeedsAnExplicitRepair(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := "m_000000000000"
	path := filepath.Join(store.path, "memories", id+"/record.md")
	original := []byte("---\ntitle: Keep\n---\nOriginal account")
	if err := fixtureWriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	doc, err := store.Read(id)
	if err != nil || doc.Body != "Original account" || !strings.Contains(strings.Join(doc.Diagnostics(), " "), "user field") {
		t.Fatal("missing metadata hid the body")
	}
	if _, err := store.Update(id, doc.Revision, nil, "New body"); err == nil {
		t.Fatal("updated an owned record without assigning a user")
	}
	data, _ := os.ReadFile(path)
	if string(data) != string(original) {
		t.Fatal("read or rejected update rewrote the source")
	}
	if _, err := store.Update(id, doc.Revision, map[string]any{"user": "ari"}, doc.Body); err != nil {
		t.Fatal(err)
	}
}

func TestImportUsesAnExplicitUserWithoutChangingTheOriginal(t *testing.T) {
	ctx := context.Background()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	original := []byte("First.\nSecond.\n")
	if err := fixtureWriteFile(filepath.Join(source, "old.md"), original, 0600); err != nil {
		t.Fatal(err)
	}
	request := ImportRequest{User: "ari", Path: "old.md", Revision: dataRevision(original), Parts: []ImportPart{
		{Start: 0, End: 7}, {Start: 7, End: 15, Fields: map[string]any{"user": "bob"}},
	}}
	mapping, err := store.Import(ctx, source, request)
	if err != nil || !mapping.CoverageComplete {
		t.Fatalf("import failed: %+v %v", mapping, err)
	}
	for i, user := range []string{"ari", "bob"} {
		doc, _ := store.Read(mapping.Parts[i].ID)
		if doc.Metadata().User != user {
			t.Fatal("import lost the reviewed owner")
		}
	}
	if archived, err := store.ReadArchive(mapping.Archive); err != nil || string(archived) != string(original) {
		t.Fatal("import ownership rewrote the original")
	}
}

func TestCaptureUpdatesRetainOrExplicitlyChangeOwnership(t *testing.T) {
	store, _ := Open(t.TempDir())
	ctx := context.Background()
	memory, err := store.SaveMemory(ctx, MemoryInput{User: "ari", Text: textValue("Original event")})
	if err != nil {
		t.Fatal(err)
	}
	story, err := store.SaveStory(ctx, StoryInput{User: "ari", Title: "A story"})
	if err != nil {
		t.Fatal(err)
	}
	for _, user := range []string{"", "bob"} {
		_, err = store.SaveMemory(ctx, MemoryInput{ID: memory.Refs["memory"], User: user, Append: "A detail"})
		if err != nil {
			t.Fatal(err)
		}
		_, err = store.SaveStory(ctx, StoryInput{ID: story.Refs["story"], User: user, Append: "A detail"})
		if err != nil {
			t.Fatal(err)
		}
		want := user
		if want == "" {
			want = "ari"
		}
		for _, id := range []string{memory.Refs["memory"], story.Refs["story"]} {
			doc, err := store.Read(id)
			if err != nil || doc.Metadata().User != want {
				t.Fatal("ownership update ignored or invented", want, err)
			}
		}
	}
}
