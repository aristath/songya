package web

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aristath/songya"
)

func TestThinkingProgressStreamsReasoningWithoutRewritingEveryChunk(t *testing.T) {
	store, _ := songya.Open(t.TempDir())
	release := make(chan struct{})
	sent := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i < 5; i++ {
			chatChunk(w, map[string]any{"reasoning_content": "Model reasoning sentinel"}, nil)
			time.Sleep(180 * time.Millisecond)
		}
		close(sent)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		chatChunk(w, map[string]any{"content": "Finished."}, nil)
		chatFinish(w, "stop")
	}))
	defer provider.Close()
	app := Handler(store)
	defer app.Close()
	configureChat(t, app, provider.URL)
	server := httptest.NewServer(app)
	defer server.Close()
	id := chatID()
	postChat(t, app, id, "thinking", "john", "Hello")
	deadline := time.Now().Add(5 * time.Second)
	for app.chat.currentProgress(id).Stage != "thinking" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	before, err := app.chat.conversation(id)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/chat/sessions/"+id+"/events", nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 65536), 2<<20)
	event := ""
	found := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			event = strings.TrimPrefix(line, "event: ")
		}
		if event == "progress" && strings.HasPrefix(line, "data: ") {
			var progress chatProgress
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &progress); err != nil {
				t.Fatal(err)
			}
			if progress.Stage == "thinking" {
				if !strings.Contains(progress.Reasoning, "Model reasoning sentinel") || progress.MessageIndex != len(before.Messages) {
					t.Fatal("reasoning missing or attached to the wrong message", progress)
				}
				found = true
				break
			}
		}
	}
	response.Body.Close()
	if !found {
		t.Fatal("thinking progress missing", scanner.Err())
	}
	<-sent
	after, err := app.chat.conversation(id)
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != before.Revision || after.Draft != nil {
		t.Fatal("reasoning-only chunks rewrote the transcript", before.Revision, after.Revision, after.Draft)
	}
	data, _ := json.Marshal(after)
	if strings.Contains(string(data), "Model reasoning sentinel") {
		t.Fatal("private reasoning was saved")
	}
	deadline = time.Now().Add(6 * time.Second)
	var checkpoint chatConversation
	for time.Now().Before(deadline) {
		checkpoint, _ = app.chat.conversation(id)
		if checkpoint.Draft != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if checkpoint.Draft == nil || checkpoint.Revision != before.Revision+1 || checkpoint.Draft.Reasoning != strings.Repeat("Model reasoning sentinel", 5) {
		t.Fatal("reasoning checkpoint missing or written repeatedly", checkpoint.Revision, checkpoint.Draft)
	}
	once.Do(func() { close(release) })
	c := waitChat(t, app, id, "done")
	message := c.Messages[len(c.Messages)-1]
	if message.Content != "Finished." || message.Reasoning != strings.Repeat("Model reasoning sentinel", 5) {
		t.Fatal("answer or reasoning not retained", message)
	}
	outgoing, _ := json.Marshal(messageParam(message))
	if strings.Contains(string(outgoing), "Model reasoning sentinel") || strings.Contains(string(outgoing), "reasoning") {
		t.Fatal("display-only reasoning was sent back to the model", string(outgoing))
	}
	reopened := Handler(store)
	defer reopened.Close()
	saved, err := reopened.chat.conversation(id)
	if err != nil || saved.Messages[len(saved.Messages)-1].Reasoning != message.Reasoning {
		t.Fatal("reasoning lost after reopening", err)
	}
}

func TestChatFileArgumentsKeepBulkFilenameBindingsAndAllowOtherRenames(t *testing.T) {
	c := chatConversation{TurnID: "turn", Messages: []chatMessage{{Role: "user", TurnID: "turn", Uploads: []songya.Upload{{ID: "john", Filename: "john.txt"}, {ID: "mary", Filename: "mary.txt"}}}}}
	for _, tool := range []string{"attach_file", "replace_file"} {
		if err := chatFileArguments(c, tool, map[string]any{"filename": "john.txt", "upload": "mary"}); err == nil {
			t.Fatal("swapped upload accepted", tool)
		}
		for _, filename := range []string{"john.txt", "renamed.txt", ""} {
			if err := chatFileArguments(c, tool, map[string]any{"filename": filename, "upload": "john"}); err != nil {
				t.Fatal("valid pairing rejected", tool, err)
			}
		}
	}
}

func TestChatRejectsSwappedBulkFileBeforeRunningTool(t *testing.T) {
	store, _ := songya.Open(t.TempDir())
	ctx := context.Background()
	record, _ := store.Create(songya.Memories, map[string]any{"user": "john", "title": "Letters"}, "Keep these words.")
	john, _ := store.Upload(ctx, "john", "chat:john", "john.txt", strings.NewReader("John's original"))
	mary, _ := store.Upload(ctx, "mary", "chat:john", "mary.txt", strings.NewReader("Mary's original"))
	requests := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			chatFunction(w, "wrong", "attach_file", `{"id":"`+record.ID+`","revision":"`+record.Revision+`","filename":"john.txt","upload":"`+mary.ID+`"}`)
			return
		}
		var body struct {
			Messages []chatMessage `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if requests == 2 {
			if !strings.Contains(body.Messages[len(body.Messages)-1].Content, "different uploaded file") {
				t.Error("missing binding feedback")
			}
			files, _ := store.Attachments(ctx, record.ID)
			if len(files.Files) != 0 {
				t.Error("bad binding wrote a file")
			}
			chatFunction(w, "correct", "attach_file", `{"id":"`+record.ID+`","revision":"`+record.Revision+`","filename":"john.txt","upload":"`+john.ID+`"}`)
			return
		}
		chatChunk(w, map[string]any{"content": "Saved John's file."}, nil)
		chatFinish(w, "stop")
	}))
	defer provider.Close()
	app := Handler(store)
	defer app.Close()
	configureChat(t, app, provider.URL)
	id := chatID()
	code, result := call(t, app, "POST", "/api/chat", map[string]any{"id": id, "request_id": "files", "user": "john", "text": "Save John's file.", "uploads": []string{john.ID, mary.ID}})
	if code != 202 {
		t.Fatal(code, result)
	}
	waitChat(t, app, id, "done")
	data, _, err := store.ReadAttachmentText(ctx, record.ID, "john.txt", 0, 100)
	if err != nil || data != "John's original" {
		t.Fatal("wrong file contents", data, err)
	}
	read, _ := store.Read(record.ID)
	if read.Body != record.Body {
		t.Fatal("prose changed")
	}
}
