package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aristath/songya"
)

type queuedModelRequest struct {
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
}

func nextQueuedRequest(t *testing.T, requests <-chan queuedModelRequest, users []string, temperature float64, prompt string) {
	t.Helper()
	select {
	case request := <-requests:
		var actual []string
		for _, message := range request.Messages {
			if message.Role == "user" && !strings.HasPrefix(message.Content, "Retrieved Songya memory evidence for this turn.") {
				actual = append(actual, message.Content)
			}
		}
		if !reflect.DeepEqual(actual, users) || request.Temperature != temperature || !strings.HasPrefix(request.Messages[0].Content, prompt) {
			t.Fatalf("wrong queued request: users=%v temperature=%v expected prompt prefix=%q", actual, request.Temperature, prompt)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("queued turn did not start")
	}
}

func TestChatQueueSurvivesRestartAndRunsInOrder(t *testing.T) {
	root := t.TempDir()
	store, _ := songya.Open(root)
	requests := make(chan queuedModelRequest, 8)
	release := map[string]chan struct{}{"First": make(chan struct{}), "Second": make(chan struct{}), "Third": make(chan struct{})}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input queuedModelRequest
		_ = json.NewDecoder(r.Body).Decode(&input)
		requests <- input
		text := input.Messages[len(input.Messages)-1].Content
		chatChunk(w, map[string]any{"role": "assistant", "content": "Reply to " + text}, nil)
		select {
		case <-release[text]:
			chatFinish(w, "stop")
		case <-r.Context().Done():
		}
	}))
	defer provider.Close()
	app := Handler(store)
	defer func() { _ = app.Close() }()
	configureChat(t, app, provider.URL)
	if err := fixtureWriteFile(filepath.Join(root, "chat-system.md"), []byte("First prompt"), 0600); err != nil {
		t.Fatal(err)
	}
	id := chatID()
	postChat(t, app, id, "first", "sam", "First")
	nextQueuedRequest(t, requests, []string{"First"}, .15, "First prompt")
	postChat(t, app, id, "second", "sam", "Second")
	postChat(t, app, id, "second", "sam", "Second") // Lost acknowledgement.
	code, _ := call(t, app, "POST", "/api/chat", map[string]any{"id": id, "request_id": "second", "user": "sam", "text": "Different"})
	if code != 409 {
		t.Fatal("request identity did not protect a queued message", code)
	}
	_, _ = call(t, app, "PUT", "/api/chat/settings", map[string]any{"base_url": provider.URL + "/v1", "model": "memory-model", "temperature": .7})
	if err := fixtureWriteFile(filepath.Join(root, "chat-system.md"), []byte("Third prompt"), 0600); err != nil {
		t.Fatal(err)
	}
	postChat(t, app, id, "third", "sam", "Third")
	c, err := app.chat.conversation(id)
	if err != nil || len(c.Queue) != 2 || len(c.Requests) != 3 || c.TurnID != "first" {
		t.Fatal(c, err)
	}
	_, public := call(t, app, "GET", "/api/chat/sessions/"+id, nil)
	data, _ := json.Marshal(public)
	if strings.Contains(string(data), "recall_configuration") || strings.Contains(string(data), "First prompt") || strings.Contains(string(data), "Third prompt") {
		t.Fatal("private queued settings were exposed", string(data))
	}
	_ = app.Close()
	c, err = app.chat.conversation(id)
	if err != nil || c.Status != "pending" || len(c.Queue) != 2 {
		t.Fatal("shutdown lost queued work", c, err)
	}
	app = Handler(store)
	nextQueuedRequest(t, requests, []string{"First"}, .15, "First prompt")
	close(release["First"])
	nextQueuedRequest(t, requests, []string{"First", "Second"}, .15, "First prompt")
	close(release["Second"])
	nextQueuedRequest(t, requests, []string{"First", "Second", "Third"}, .7, "Third prompt")
	close(release["Third"])
	c = waitChat(t, app, id, "done")
	if len(c.Queue) != 0 || len(c.Messages) != 6 || c.Messages[5].Content != "Reply to Third" {
		t.Fatal("queued history is incomplete", c)
	}
	postChat(t, app, id, "second", "sam", "Second")
	c, _ = app.chat.conversation(id)
	if c.Status != "done" || len(c.Queue) != 0 || len(c.Messages) != 6 {
		t.Fatal("a retry duplicated a completed queued message", c)
	}
}

func TestChatQueueWaitsForPausedAndFailedTurn(t *testing.T) {
	store, _ := songya.Open(t.TempDir())
	var attempts atomic.Int32
	entered := make(chan struct{})
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		chatChunk(w, map[string]any{"role": "assistant", "content": "Reply"}, nil)
		if n == 1 {
			close(entered)
			<-r.Context().Done()
			return
		}
		if n != 2 { // Retry first fails, so later queued messages must still wait.
			chatFinish(w, "stop")
		}
	}))
	defer provider.Close()
	app := Handler(store)
	defer app.Close()
	configureChat(t, app, provider.URL)
	id := chatID()
	postChat(t, app, id, "first", "sam", "First")
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("first turn did not start")
	}
	postChat(t, app, id, "second", "sam", "Second")
	_, _ = call(t, app, "POST", "/api/chat/sessions/"+id+"/pause", nil)
	postChat(t, app, id, "third", "sam", "Third")
	c := waitChat(t, app, id, "paused")
	if len(c.Queue) != 2 || attempts.Load() != 1 {
		t.Fatal("pause did not preserve and hold the queue", c)
	}
	// Wait for the cancelled worker to release its lease, without starting work.
	deadline := time.Now().Add(10 * time.Second)
	for {
		app.chat.mu.Lock()
		active := app.chat.active[id] != nil
		app.chat.mu.Unlock()
		if !active {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("paused worker did not stop")
		}
		time.Sleep(time.Millisecond)
	}
	_, _ = call(t, app, "POST", "/api/chat/sessions/"+id+"/retry", nil)
	c = waitChat(t, app, id, "error")
	if len(c.Queue) != 2 || attempts.Load() != 2 {
		t.Fatal("error advanced or lost queued work", c)
	}
	_, _ = call(t, app, "POST", "/api/chat/sessions/"+id+"/retry", nil)
	c = waitChat(t, app, id, "done")
	if len(c.Queue) != 0 || len(c.Messages) != 6 || attempts.Load() != 5 {
		t.Fatal("retry did not complete the retained queue", c, attempts.Load())
	}
}
