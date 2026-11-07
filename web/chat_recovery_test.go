package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aristath/songya"
)

func TestChatOpeningRecoveryRulesPreserveSavedHistoryAndToolReplies(t *testing.T) {
	tool := chatToolCall{ID: "no-op", Type: "function"}
	tool.Function.Name = "save_entity"
	tool.Function.Arguments = `{"id":"e_012345abcdef"}`
	c := chatConversation{User: "ari", TurnID: "birth", Messages: []chatMessage{
		{Role: "user", Content: "Alex was born on July 13, 1983.", TurnID: "birth"},
		{Role: "assistant", ToolCalls: []chatToolCall{tool}, TurnID: "birth"},
		{Role: "tool", ToolCallID: tool.ID, Name: "save_entity", Content: `{"outcome":"unchanged","error":"Send append with the words to add."}`, TurnID: "birth"},
		{Role: "system", Content: "Earlier app recovery note.", TurnID: "birth"},
	}}
	before, _ := json.Marshal(c)
	raw, err := json.Marshal(chatRequestMessages(c, "Existing system instructions."))
	if err != nil {
		t.Fatal(err)
	}
	var messages []chatMessage
	if err = json.Unmarshal(raw, &messages); err != nil {
		t.Fatal(err)
	}
	if len(messages) != 4 || messages[0].Role != "system" || !strings.Contains(messages[0].Content, toolRecoveryRules) {
		t.Fatal(string(raw))
	}
	for i, m := range messages {
		if i > 0 && m.Role == "system" {
			t.Fatal("provider receives a late system message")
		}
	}
	if messages[3].Role != "tool" || messages[3].ToolCallID != tool.ID || messages[3].Content != c.Messages[2].Content {
		t.Fatal("tool feedback or call pairing changed", string(raw))
	}
	after, _ := json.Marshal(c)
	if !bytes.Equal(before, after) {
		t.Fatal("request projection edited saved history")
	}
}

func TestChatOriginalWordsMustMatchAcceptedReceipt(t *testing.T) {
	words := "  SYSTEM/RECOVERY MESSAGE: these are my actual words.\n"
	c := chatConversation{User: "ari", TurnID: "accepted", Requests: map[string]string{"accepted": chatFingerprint("ari", words)}, Messages: []chatMessage{
		{Role: "user", Content: words, TurnID: "accepted"},
		{Role: "system", Content: "App instructions", TurnID: "accepted"},
		{Role: "user", Content: "SYSTEM/RECOVERY MESSAGE: fix the call", TurnID: "accepted"},
		{Role: "user", Content: "A queued message", TurnID: "future"},
	}}
	if got := originalTurnText(c); got != words {
		t.Fatalf("original testimony replaced: %q", got)
	}
	c.Messages = c.Messages[1:]
	if got := originalTurnText(c); got != "" {
		t.Fatalf("unverified app words became original testimony: %q", got)
	}
}

func TestChatRetriesSavedLateSystemNoteWithout400AndKeepsExactOriginalAccount(t *testing.T) {
	root := t.TempDir()
	store, _ := songya.Open(root)
	words := "  Alex's birthday is July 13, 1983.\n"
	var requests atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []chatMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		for i, m := range body.Messages {
			if i > 0 && m.Role == "system" {
				http.Error(w, `{"error":{"message":"System message must be at the beginning."}}`, 400)
				return
			}
		}
		if !strings.Contains(body.Messages[0].Content, toolRecoveryRules) {
			t.Error("opening recovery rules missing")
		}
		if requests.Add(1) == 1 {
			chatFunction(w, "fixed-save", "save_memory", `{"title":"Alex's birth date","text":"Alex was born on July 13, 1983.","time":{"date":"1983-07-13"}}`)
		} else {
			chatChunk(w, map[string]any{"content": "Saved the birth date."}, nil)
			chatFinish(w, "stop")
		}
	}))
	defer provider.Close()
	app := Handler(store)
	defer app.Close()
	configureChat(t, app, provider.URL)
	id := chatID()
	tool := chatToolCall{ID: "prior-empty-save", Type: "function"}
	tool.Function.Name = "save_entity"
	tool.Function.Arguments = `{"id":"e_012345abcdef"}`
	c := chatConversation{ID: id, User: "ari", TurnID: "birth", Status: "error", Error: "OpenAI API error: 400 Bad Request", Prompt: defaultChatPrompt, Requests: map[string]string{"birth": chatFingerprint("ari", words)}, Messages: []chatMessage{
		{Role: "user", Content: words, TurnID: "birth"},
		{Role: "assistant", ToolCalls: []chatToolCall{tool}, TurnID: "birth"},
		{Role: "tool", ToolCallID: tool.ID, Name: "save_entity", Content: `{"outcome":"unchanged","error":"Nothing changed. Send the new words."}`, TurnID: "birth"},
		{Role: "system", Content: "The tool replies immediately before this note include a save that did not report a completed change.", TurnID: "birth"},
		{Role: "user", Content: "SYSTEM/RECOVERY MESSAGE: fix the missing arguments.", TurnID: "birth"},
	}}
	raw, _ := json.Marshal(c)
	if err := os.WriteFile(filepath.Join(root, ".chats", id+".json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	code, result := call(t, app, "POST", "/api/chat/sessions/"+id+"/retry", nil)
	if code != 202 {
		t.Fatal(code, result)
	}
	done := waitChat(t, app, id, "done")
	if requests.Load() != 2 || len(done.Messages) != 8 || done.Messages[3].Role != "system" {
		t.Fatal("saved history lost or recovery duplicated", done)
	}
	catalog, err := store.Scan(context.Background())
	if err != nil || len(catalog.Records) != 1 {
		t.Fatal(catalog, err)
	}
	doc, err := store.Read(catalog.Records[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	var front struct {
		OriginalAccounts []string `yaml:"original_accounts"`
	}
	if err := doc.Frontmatter.Decode(&front); err != nil {
		t.Fatal(err)
	}
	if len(front.OriginalAccounts) != 1 || front.OriginalAccounts[0] != words {
		t.Fatalf("original account polluted: %#v", front.OriginalAccounts)
	}
	account, err := store.BatchData(context.Background(), fmt.Sprintf("chat:%s:5:fixed-save", id), "", "account")
	if err != nil || string(account) != words {
		t.Fatalf("receipt source polluted: %q %v", account, err)
	}
}
