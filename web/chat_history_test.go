package web

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aristath/songya"
)

func TestChatEditRewindsResendsAndRetriesWithoutRepeating(t *testing.T) {
	store, _ := songya.Open(t.TempDir())
	app := Handler(store)
	defer app.Close()
	if err := app.chat.initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	id := chatID()
	upload := songya.Upload{ID: "saved-file", Filename: "photo.jpg"}
	seed, err := app.chat.edit(context.Background(), id, func(c *chatConversation) error {
		c.User, c.Title, c.Status, c.TurnID = "sam", "Our conversation", "done", "later"
		c.Messages = []chatMessage{{Role: "user", Content: "Keep this", TurnID: "first"}, {Role: "assistant", Content: "Earlier answer", TurnID: "first"}, {Role: "user", Content: "Wrong words", TurnID: "edited", Uploads: []songya.Upload{upload}}, {Role: "assistant", Content: "Discard this answer", TurnID: "edited"}, {Role: "user", Content: "Later words", TurnID: "later"}}
		c.Queue = []chatQueuedTurn{{ID: "queued", Text: "Remove queued words"}}
		for _, m := range c.Messages {
			if m.Role == "user" {
				c.Requests[m.TurnID] = chatTurnFingerprint("sam", m.Content, m.Uploads)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"id": id, "request_id": "corrected", "edit_turn_id": "edited", "user": "sam", "text": "Correct words"}
	code, value := call(t, app, "POST", "/api/chat", input)
	if code != 202 {
		t.Fatal(code, value)
	}
	c := waitChat(t, app, id, "error") // No model configured: the corrected turn remains durable.
	if c.BranchID == "" || c.BranchID == seed.BranchID || len(c.Messages) != 3 || !reflect.DeepEqual(c.Messages[:2], seed.Messages[:2]) || c.Messages[2].Content != "Correct words" || c.Messages[2].TurnID != "corrected" || !reflect.DeepEqual(c.Messages[2].Uploads, []songya.Upload{upload}) || len(c.Queue) != 0 || c.Draft != nil {
		t.Fatal("Incorrect rewind", c)
	}
	branch := c.BranchID
	for range 2 {
		code, value = call(t, app, "POST", "/api/chat", input)
		if code != 202 {
			t.Fatal(code, value)
		}
	}
	c, _ = app.chat.conversation(id)
	if len(c.Messages) != 3 || c.BranchID != branch {
		t.Fatal("Retry replayed the rewind", c)
	}
	// A delayed retry of a discarded message must not put it back in the queue.
	code, _ = call(t, app, "POST", "/api/chat", map[string]any{"id": id, "request_id": "later", "user": "sam", "text": "Later words"})
	if code != 202 {
		t.Fatal(code)
	}
	c, _ = app.chat.conversation(id)
	if len(c.Queue) != 0 || len(c.Messages) != 3 {
		t.Fatal("Discarded message returned", c)
	}
	input["request_id"] = "stale-edit"
	code, _ = call(t, app, "POST", "/api/chat", input)
	if code != 409 {
		t.Fatal("Stale edit accepted", code)
	}
}

func TestChatQueuedEditPreservesCurrentWorkAndEarlierQueue(t *testing.T) {
	c := chatConversation{BranchID: "current", Status: "paused", TurnID: "active", Messages: []chatMessage{{Role: "user", TurnID: "active", Content: "Keep active"}}, Draft: &chatMessage{Role: "assistant", Content: "Partial"}, Queue: []chatQueuedTurn{{ID: "first", Text: "Keep queued"}, {ID: "edit", Text: "Correct this"}, {ID: "last", Text: "Discard this"}}}
	if err := c.rewind("edit"); err != nil {
		t.Fatal(err)
	}
	if len(c.Queue) != 1 || c.Queue[0].ID != "first" || c.Status != "pending" || c.Draft == nil || c.BranchID != "current" || c.Messages[0].Content != "Keep active" {
		t.Fatal(c)
	}
}

func TestChatDeleteAndEditStopStreamingWithoutResurrectingHistory(t *testing.T) {
	for _, action := range []string{"delete", "edit"} {
		t.Run(action, func(t *testing.T) {
			started := make(chan struct{})
			var once sync.Once
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Messages []chatMessage `json:"messages"`
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				corrected := false
				for _, m := range body.Messages {
					if m.Role == "user" && m.Content == "Corrected" {
						corrected = true
					}
					if corrected && strings.Contains(m.Content, "Old reply") {
						t.Error("Discarded reply reached model")
					}
				}
				if corrected {
					chatChunk(w, map[string]any{"role": "assistant", "content": "New reply"}, nil)
					chatFinish(w, "stop")
					return
				}
				chatChunk(w, map[string]any{"role": "assistant", "content": "Old reply"}, nil)
				once.Do(func() { close(started) })
				<-r.Context().Done()
			}))
			defer provider.Close()
			store, _ := songya.Open(t.TempDir())
			app := Handler(store)
			defer app.Close()
			configureChat(t, app, provider.URL)
			saved, err := store.Create(songya.Memories, map[string]any{"user": "sam", "title": "Keep memory"}, "Keep these words")
			if err != nil {
				t.Fatal(err)
			}
			id := chatID()
			postChat(t, app, id, "original", "sam", "Original")
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("Stream did not start")
			}
			postChat(t, app, id, "queued", "sam", "Remove queued turn")
			if action == "delete" {
				code, v := call(t, app, "DELETE", "/api/chat/sessions/"+id, nil)
				if code != 200 {
					t.Fatal(code, v)
				}
				if _, err := app.chat.conversation(id); !errors.Is(err, fs.ErrNotExist) {
					t.Fatal("Deleted chat returned", err)
				}
				code, v = call(t, app, "DELETE", "/api/chat/sessions/"+id, nil)
				if code != 200 {
					t.Fatal("Delete retry", code, v)
				}
				app.chat.recover()
				if _, err := app.chat.conversation(id); !errors.Is(err, fs.ErrNotExist) {
					t.Fatal("Recovery recreated chat", err)
				}
			} else {
				code, v := call(t, app, "POST", "/api/chat", map[string]any{"id": id, "request_id": "corrected", "edit_turn_id": "original", "user": "sam", "text": "Corrected"})
				if code != 202 {
					t.Fatal(code, v)
				}
				c := waitChat(t, app, id, "done")
				if len(c.Messages) != 2 || c.Messages[0].Content != "Corrected" || c.Messages[1].Content != "New reply" || len(c.Queue) != 0 || c.Draft != nil {
					t.Fatal("Old stream survived rewind", c)
				}
			}
			record, err := store.Read(saved.ID)
			if err != nil || record.Body != "Keep these words" {
				t.Fatal("Chat action changed saved memory", err)
			}
		})
	}
}

func TestChatRewindUsesFreshToolReceipts(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []chatMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if body.Messages[len(body.Messages)-1].Role == "tool" {
			chatChunk(w, map[string]any{"role": "assistant", "content": "Saved"}, nil)
			chatFinish(w, "stop")
			return
		}
		name := "John original"
		for _, message := range body.Messages {
			if message.Role == "user" && message.Content == "Corrected" {
				name = "Mary corrected"
			}
		}
		// Providers may reuse a tool-call ID at the same history index after editing.
		args, _ := json.Marshal(map[string]any{"create": true, "name": name, "description": "A colleague."})
		chatFunction(w, "same-call", "save_entity", string(args))
	}))
	defer provider.Close()
	store, _ := songya.Open(t.TempDir())
	app := Handler(store)
	defer app.Close()
	configureChat(t, app, provider.URL)
	id := chatID()
	postChat(t, app, id, "original", "sam", "Original")
	waitChat(t, app, id, "done")
	code, value := call(t, app, "POST", "/api/chat", map[string]any{"id": id, "request_id": "corrected", "edit_turn_id": "original", "user": "sam", "text": "Corrected"})
	if code != 202 {
		t.Fatal(code, value)
	}
	waitChat(t, app, id, "done")
	catalog, err := store.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, record := range catalog.Records {
		found[record.Title] = true
	}
	if !found["John original"] || !found["Mary corrected"] {
		t.Fatal("A discarded branch's tool receipt prevented the corrected write", found)
	}
}
