package web

import (
	"context"
	"reflect"
	"testing"

	"github.com/aristath/songya"
)

func TestChatSessionRenameAndArchivePreserveHistoryAndPendingWork(t *testing.T) {
	store, _ := songya.Open(t.TempDir())
	app := Handler(store)
	defer app.Close()
	if err := app.chat.initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	id := chatID()
	seed, err := app.chat.edit(context.Background(), id, func(c *chatConversation) error {
		c.Store, c.Title, c.Status, c.TurnID = "sam", "First conversation", "paused", "first"
		c.Messages = []chatMessage{{Role: "user", Content: "A remembered afternoon.", TurnID: "first"}}
		c.Draft = &chatMessage{Role: "assistant", Content: "Partial reply", TurnID: "first"}
		c.Queue = []chatQueuedTurn{{ID: "second", Text: "A queued detail.", Prompt: "Saved prompt"}}
		c.Requests["first"] = "original request"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	code, _ := call(t, app, "PATCH", "/api/chat/sessions/"+id, map[string]any{"title": "  Island   afternoons  "})
	if code != 200 {
		t.Fatal(code)
	}
	renamed, _ := app.chat.conversation(id)
	if renamed.Title != "Island afternoons" || renamed.Store != seed.Store || renamed.Status != seed.Status || !reflect.DeepEqual(renamed.Messages, seed.Messages) || !reflect.DeepEqual(renamed.Queue, seed.Queue) || !reflect.DeepEqual(renamed.Draft, seed.Draft) || !reflect.DeepEqual(renamed.Requests, seed.Requests) {
		t.Fatal("rename changed saved work", renamed)
	}
	code, _ = call(t, app, "PATCH", "/api/chat/sessions/"+id, map[string]any{"archived": true})
	if code != 409 {
		t.Fatal("unfinished work could be hidden in the archive", code)
	}
	code, _ = call(t, app, "PATCH", "/api/chat/sessions/"+id, map[string]any{"title": " "})
	if code != 400 {
		t.Fatal("blank title accepted", code)
	}
	_, err = app.chat.edit(context.Background(), id, func(c *chatConversation) error {
		c.Status, c.Queue, c.Draft = "done", nil, nil
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := app.chat.conversation(id)
	for range 2 {
		code, _ = call(t, app, "PATCH", "/api/chat/sessions/"+id, map[string]any{"archived": true})
		if code != 200 {
			t.Fatal(code)
		}
	}
	archived, _ := app.chat.conversation(id)
	if !archived.Archived || !reflect.DeepEqual(archived.Messages, before.Messages) || !reflect.DeepEqual(archived.Requests, before.Requests) {
		t.Fatal("archive changed the conversation", archived)
	}
	code, _ = call(t, app, "POST", "/api/chat", map[string]any{"id": id, "request_id": "new", "user": "sam", "text": "New message"})
	if code != 409 {
		t.Fatal("archived conversation accepted a new turn", code)
	}
	rows, issues, err := app.chat.summaries()
	if err != nil || len(issues) != 0 || len(rows) != 1 || rows[0]["archived"] != true || rows[0]["preview"] != "A remembered afternoon." {
		t.Fatal(rows, issues, err)
	}
	// Reopening the application reads the same durable archive and history.
	_ = app.Close()
	reopened := Handler(store)
	defer reopened.Close()
	code, public := call(t, reopened, "GET", "/api/chat/sessions/"+id, nil)
	if code != 200 || public["archived"] != true {
		t.Fatal(public)
	}
	code, _ = call(t, reopened, "PATCH", "/api/chat/sessions/"+id, map[string]any{"archived": false})
	if code != 200 {
		t.Fatal(code)
	}
	restored, _ := reopened.chat.conversation(id)
	if restored.Archived || !reflect.DeepEqual(restored.Messages, before.Messages) || restored.Store != "sam" {
		t.Fatal("restore lost the history", restored)
	}
}
