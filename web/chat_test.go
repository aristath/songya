package web

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aristath/songya"
)

func chatChunk(w http.ResponseWriter, delta any, finish any) {
	w.Header().Set("Content-Type", "text/event-stream")
	data, _ := json.Marshal(map[string]any{"id": "completion", "object": "chat.completion.chunk", "created": 1, "model": "memory-model", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
	_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
	w.(http.Flusher).Flush()
}
func chatFinish(w http.ResponseWriter, reason string) {
	chatChunk(w, map[string]any{}, reason)
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	w.(http.Flusher).Flush()
}
func chatFunction(w http.ResponseWriter, id, name, args string) {
	mid := len(args) / 2
	chatChunk(w, map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": id, "type": "function", "function": map[string]any{"name": name, "arguments": args[:mid]}}}}, nil)
	chatChunk(w, map[string]any{"tool_calls": []any{map[string]any{"index": 0, "function": map[string]any{"arguments": args[mid:]}}}}, nil)
	chatFinish(w, "tool_calls")
}
func configureChat(t *testing.T, app *Application, base string) {
	t.Helper()
	code, value := call(t, app, "PUT", "/api/chat/settings", map[string]any{"base_url": base + "/v1", "model": "memory-model", "api_key": "test-chat-key"})
	if code != 200 || value["configured"] != true {
		t.Fatal(code, value)
	}
}
func waitChat(t *testing.T, app *Application, id, status string) chatConversation {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		c, err := app.chat.conversation(id)
		if err == nil && c.Status == status {
			return c
		}
		if err == nil && c.Status == "error" && status != "error" {
			t.Fatal(c.Error)
		}
		time.Sleep(10 * time.Millisecond)
	}
	c, err := app.chat.conversation(id)
	t.Fatalf("conversation did not reach %s: %+v %v", status, c, err)
	return c
}
func postChat(t *testing.T, app *Application, id, requestID, user, text string) map[string]any {
	t.Helper()
	code, value := call(t, app, "POST", "/api/chat", map[string]any{"id": id, "request_id": requestID, "user": user, "text": text})
	if code != 202 {
		t.Fatal(code, value)
	}
	return value
}

func TestChatStreamsThroughRealMCPAndSurvivesBrowserDisconnect(t *testing.T) {
	root := t.TempDir()
	store, _ := songya.Open(root)
	var requests atomic.Int32
	release := make(chan struct{})
	var once sync.Once
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("wrong endpoint: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-chat-key" {
			t.Error("configured API key was not used")
		}
		var body struct {
			Stream   bool             `json:"stream"`
			Model    string           `json:"model"`
			Messages []chatMessage    `json:"messages"`
			Tools    []map[string]any `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if !body.Stream || body.Model != "memory-model" || len(body.Tools) != 15 {
			t.Error("streaming request lost model or MCP definitions")
		}
		if !strings.Contains(body.Messages[0].Content, `user "ari"`) || !strings.Contains(body.Messages[0].Content, "Songya stores connected Markdown records") {
			t.Error("system prompt lost user or live MCP guide")
		}
		switch requests.Add(1) {
		case 1:
			chatFunction(w, "prepare", "prepare_memory", `{"text":"Ari was born on 6 March 1981."}`)
		case 2:
			chatFunction(w, "save", "save_memory", `{"text":"Ari was born on 6 March 1981.","time":{"date":"1981-03-06"}}`)
		case 3:
			if body.Messages[len(body.Messages)-1].Role != "tool" || !strings.Contains(body.Messages[len(body.Messages)-1].Content, `"outcome":"created"`) {
				t.Error("saved result did not return to model")
			}
			chatChunk(w, map[string]any{"role": "assistant", "content": "**Saved**"}, nil)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			chatChunk(w, map[string]any{"content": " your birth date."}, nil)
			chatFinish(w, "stop")
		default:
			t.Error("duplicate model request")
		}
	}))
	app := Handler(store)
	server := httptest.NewServer(app)
	t.Cleanup(func() { once.Do(func() { close(release) }); app.Close(); server.Close(); provider.Close() })
	configureChat(t, app, provider.URL)
	id := chatID()
	text := "Remember my birth date, 6 March 1981."
	postChat(t, app, id, "accepted-once", "ari", text)
	response, err := http.Get(server.URL + "/api/chat/sessions/" + id + "/events")
	if err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(response.Body)
	sawPartial := false
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var c chatConversation
		_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &c)
		if c.Draft != nil && c.Draft.Content == "**Saved**" {
			sawPartial = true
			if c.Status != "running" {
				t.Fatal("partial reply was presented as complete")
			}
			break
		}
	}
	response.Body.Close()
	if !sawPartial {
		t.Fatal("stream did not expose partial content before completion")
	}
	once.Do(func() { close(release) })
	c := waitChat(t, app, id, "done")
	if c.Messages[len(c.Messages)-1].Content != "**Saved** your birth date." {
		t.Fatal(c.Messages)
	}
	catalog, err := store.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	memories := catalog.Select(songya.Query{Kind: songya.Memories, User: "ari"})
	if len(memories) != 1 || memories[0].Time.Date == nil || *memories[0].Time.Date != "1981-03-06" {
		t.Fatal(memories)
	}
	postChat(t, app, id, "accepted-once", "ari", text)
	if requests.Load() != 3 {
		t.Fatal("lost acknowledgement replayed accepted work")
	}
	code, _ := call(t, app, "POST", "/api/chat", map[string]any{"id": id, "request_id": "accepted-once", "user": "ari", "text": "Different request"})
	if code != 409 {
		t.Fatal("request identity was reusable", code)
	}
	code, public := call(t, app, "GET", "/api/chat/sessions/"+id, nil)
	if code != 200 || public["prompt"] != nil || public["requests"] != nil {
		t.Fatal("private request state leaked", public)
	}
	if _, err := os.Stat(filepath.Join(root, "chat-system.md")); err != nil {
		t.Fatal(err)
	}
}

func TestChatStartupRecoversAcceptedToolFromOriginalReceipt(t *testing.T) {
	root := t.TempDir()
	store, _ := songya.Open(root)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chatChunk(w, map[string]any{"role": "assistant", "content": "The saved work is complete."}, nil)
		chatFinish(w, "stop")
	}))
	defer provider.Close()
	setup := Handler(store)
	configureChat(t, setup, provider.URL)
	setup.Close()
	id := chatID()
	text := "The original account."
	key := "chat:" + id + ":1:save_existing"
	receipt, err := store.SaveMemory(context.Background(), songya.MemoryInput{Key: key, User: "ari", Text: &text})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := store.Read(receipt.Refs["memory"])
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Update(doc.ID, doc.Revision, nil, "A later edit must remain intact."); err != nil {
		t.Fatal(err)
	}
	tool := chatToolCall{ID: "save_existing", Type: "function"}
	tool.Function.Name = "save_memory"
	tool.Function.Arguments = `{"text":"The original account."}`
	c := chatConversation{ID: id, User: "ari", Status: "running", TurnID: "original", Prompt: defaultChatPrompt, Requests: map[string]string{"original": chatFingerprint("ari", "Save this account.")}, Messages: []chatMessage{{Role: "user", Content: "Save this account.", TurnID: "original"}, {Role: "assistant", ToolCalls: []chatToolCall{tool}, TurnID: "original"}}}
	data, _ := json.Marshal(c)
	if err := os.WriteFile(filepath.Join(root, ".chats", id+".json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	app := Handler(store)
	defer app.Close()
	completed := waitChat(t, app, id, "done")
	if len(completed.Messages) != 4 || completed.Messages[2].Role != "tool" || !strings.Contains(completed.Messages[2].Content, `"outcome":"created"`) {
		t.Fatal(completed.Messages)
	}
	doc, err = store.Read(doc.ID)
	if err != nil || doc.Body != "A later edit must remain intact." {
		t.Fatal("recovery replayed the accepted write", doc, err)
	}
	catalog, _ := store.Scan(context.Background())
	if len(catalog.Select(songya.Query{Kind: songya.Memories})) != 1 {
		t.Fatal("recovery duplicated memory")
	}
}

func TestChatTruncatedStreamRetainsPartialAndRetriesWithoutDuplicatingUser(t *testing.T) {
	store, _ := songya.Open(t.TempDir())
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chatChunk(w, map[string]any{"role": "assistant", "content": "Partial reply"}, nil)
		if calls.Add(1) > 1 {
			chatChunk(w, map[string]any{"content": " completed."}, nil)
			chatFinish(w, "stop")
		}
	}))
	defer provider.Close()
	app := Handler(store)
	defer app.Close()
	configureChat(t, app, provider.URL)
	id := chatID()
	postChat(t, app, id, "one", "ari", "Tell me about my memories.")
	c := waitChat(t, app, id, "error")
	if c.Draft == nil || c.Draft.Content != "Partial reply" || len(c.Messages) != 1 {
		t.Fatal(c)
	}
	code, _ := call(t, app, "POST", "/api/chat/sessions/"+id+"/retry", nil)
	if code != 202 {
		t.Fatal(code)
	}
	c = waitChat(t, app, id, "done")
	if len(c.Messages) != 2 || c.Messages[1].Content != "Partial reply completed." || calls.Load() != 2 {
		t.Fatal(c)
	}
}

func TestChatSettingsAreDurableAndNeverReturnCredentials(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "ambient-key-must-not-be-used")
	root := t.TempDir()
	store, _ := songya.Open(root)
	app := Handler(store)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("ambient or cleared key was sent")
		}
		write(w, map[string]any{"object": "list", "data": []any{map[string]any{"id": "local-model", "object": "model", "owned_by": "local", "created": 1}}})
	}))
	defer provider.Close()
	configureChat(t, app, provider.URL)
	_, value := call(t, app, "GET", "/api/chat/settings", nil)
	data, _ := json.Marshal(value)
	if strings.Contains(string(data), "test-chat-key") || value["has_api_key"] != true {
		t.Fatal(value)
	}
	code, _ := call(t, app, "PUT", "/api/chat/settings", map[string]any{"base_url": provider.URL + "/v1/", "model": "local-model", "api_key": ""})
	if code != 200 {
		t.Fatal(code)
	}
	app.Close()
	app = Handler(store)
	defer app.Close()
	_, value = call(t, app, "GET", "/api/chat/settings", nil)
	if value["model"] != "local-model" || value["has_api_key"] != false {
		t.Fatal(value)
	}
	code, value = call(t, app, "GET", "/api/chat/models", nil)
	if code != 200 || len(value["models"].([]any)) != 1 {
		t.Fatal(code, value)
	}
	info, err := os.Stat(filepath.Join(root, "chat-settings.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal(info, err)
	}
}

func TestChatRestartAndPauseResumeRecoverAcceptedMessage(t *testing.T) {
	store, _ := songya.Open(t.TempDir())
	var calls atomic.Int32
	started := make(chan struct{}, 4)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		chatChunk(w, map[string]any{"role": "assistant", "content": "Partial reply"}, nil)
		started <- struct{}{}
		if n < 3 {
			<-r.Context().Done()
			return
		}
		chatChunk(w, map[string]any{"content": " recovered."}, nil)
		chatFinish(w, "stop")
	}))
	defer provider.Close()
	app := Handler(store)
	configureChat(t, app, provider.URL)
	id := chatID()
	postChat(t, app, id, "accepted", "ari", "Keep this conversation.")
	<-started
	app.Close()
	c, err := app.chat.conversation(id)
	if err != nil || c.Status != "pending" || len(c.Messages) != 1 {
		t.Fatal("shutdown lost accepted work", c, err)
	}
	app = Handler(store)
	defer app.Close()
	<-started
	code, _ := call(t, app, "POST", "/api/chat/sessions/"+id+"/pause", nil)
	if code != 200 {
		t.Fatal(code)
	}
	code, _ = call(t, app, "POST", "/api/chat/sessions/"+id+"/retry", nil)
	if code != 202 {
		t.Fatal(code)
	}
	c = waitChat(t, app, id, "done")
	if len(c.Messages) != 2 || calls.Load() != 3 || c.Messages[1].Content != "Partial reply recovered." {
		t.Fatal("resume duplicated or lost work", c)
	}
}

func TestChatPublicationRecoversAfterStorageRepairAndIsolatesDamagedConversation(t *testing.T) {
	root := t.TempDir()
	store, _ := songya.Open(root)
	app := Handler(store)
	defer app.Close()
	if err := app.chat.initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	id := chatID()
	name := ".chats/" + id + ".json"
	if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- app.chat.publish(ctx, name, []byte(`{"recovered":true}`)) }()
	select {
	case err := <-finished:
		t.Fatal("publication abandoned recoverable write", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := os.Remove(filepath.Join(root, name)); err != nil {
		t.Fatal(err)
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, name))
	if err != nil || string(data) != `{"recovered":true}` {
		t.Fatal(string(data), err)
	}
	goodID := chatID()
	_, err = app.chat.edit(ctx, goodID, func(c *chatConversation) error {
		c.Requests["accepted"] = "receipt"
		c.User = "healthy"
		c.Status = "done"
		c.Title = "Intact conversation"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	rows, issues, err := app.chat.summaries()
	if err != nil || len(rows) != 1 || rows[0]["id"] != goodID || len(issues) != 1 {
		t.Fatal(rows, issues, err)
	}
	retained, _ := os.ReadFile(filepath.Join(root, name))
	if string(retained) != string(data) {
		t.Fatal("damaged conversation was discarded")
	}
}

func TestChatReadsAndEditsExistingMemoryWithoutChangingItsOwner(t *testing.T) {
	store, _ := songya.Open(t.TempDir())
	text := "The family moved to Mytilene after Ari's birth."
	saved, err := store.SaveMemory(context.Background(), songya.MemoryInput{Key: "existing-account", User: "ari", Text: &text})
	if err != nil {
		t.Fatal(err)
	}
	memoryID := saved.Refs["memory"]
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []chatMessage `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch calls.Add(1) {
		case 1:
			chatFunction(w, "same-model-id", "read_memory", fmt.Sprintf(`{"id":%q,"view":"source"}`, memoryID))
		case 2:
			if !strings.Contains(body.Messages[len(body.Messages)-1].Content, "Mytilene") {
				t.Error("read result missing")
			}
			chatChunk(w, map[string]any{"role": "assistant", "content": "I found the existing account."}, nil)
			chatFinish(w, "stop")
		case 3:
			if len(body.Messages) < 5 {
				t.Error("previous turn missing from context")
			}
			// Reused provider call IDs must not confuse receipts across generations.
			chatFunction(w, "same-model-id", "save_memory", fmt.Sprintf(`{"id":%q,"append":"The move was a few months after birth.","time":{"earliest":"1981-05-01"}}`, memoryID))
		case 4:
			if !strings.Contains(body.Messages[len(body.Messages)-1].Content, `"outcome":"updated"`) {
				t.Error("update result missing")
			}
			chatChunk(w, map[string]any{"role": "assistant", "content": "Updated the account and date bound."}, nil)
			chatFinish(w, "stop")
		default:
			t.Error("unexpected request")
		}
	}))
	defer provider.Close()
	app := Handler(store)
	defer app.Close()
	configureChat(t, app, provider.URL)
	id := chatID()
	postChat(t, app, id, "read", "clara", "Read Ari's existing Mytilene account.")
	waitChat(t, app, id, "done")
	postChat(t, app, id, "edit", "clara", "Add that the move was a few months after birth, earliest 1 May 1981.")
	c := waitChat(t, app, id, "done")
	doc, err := store.Read(memoryID)
	if err != nil || doc.Record(songya.Memories).User != "ari" || doc.Record(songya.Memories).Time.Earliest == nil || *doc.Record(songya.Memories).Time.Earliest != "1981-05-01" || !strings.Contains(doc.Body, "few months after birth") {
		t.Fatal(doc, err)
	}
	if c.Messages[1].ToolCalls[0].ID == c.Messages[5].ToolCalls[0].ID {
		t.Fatal("reused model IDs were not normalized")
	}
	catalog, _ := store.Scan(context.Background())
	if len(catalog.Select(songya.Query{Kind: songya.Memories})) != 1 {
		t.Fatal("edit created another memory")
	}
}
