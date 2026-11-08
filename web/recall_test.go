package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aristath/songya"
)

func TestRecallHTTPSettingsConflictPreservationAndLiveConfiguration(t *testing.T) {
	root := t.TempDir()
	var requests atomic.Int32
	embed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		write(w, map[string]any{"data": []any{map[string]any{"embedding": []int{1, 0}}}})
	}))
	defer embed.Close()
	store, _ := songya.Open(root, songya.EmbeddingConfig{BaseURL: embed.URL})
	doc, err := store.Create(songya.Memories, map[string]any{"user": "ari"}, "A saved afternoon")
	if err != nil {
		t.Fatal(err)
	}
	original := `{"embeddings":{"model":"configured","base_url":"` + embed.URL + `","api_key":"private-key"},"other":{"keep":[3,true]}}`
	fixtureWriteFile(filepath.Join(root, "config.json"), []byte(original), 0600)
	app := Handler(store)
	defer app.Close()
	code, settings := call(t, app, "GET", "/api/recall/settings", nil)
	if code != 200 || strings.Contains(string(mustJSON(settings)), "private-key") {
		t.Fatal(code, settings)
	}
	policy := settings["recall"].(map[string]any)
	policy["direct_limit"] = "1"
	policy["connection_depth"] = 0
	code, updated := call(t, app, "PUT", "/api/recall/settings", map[string]any{"version": settings["version"], "recall": policy})
	if code != 200 {
		t.Fatal(code, updated)
	}
	data, _ := os.ReadFile(filepath.Join(root, "config.json"))
	if !strings.Contains(string(data), "private-key") || !strings.Contains(string(data), "keep") {
		t.Fatal("unrelated config lost", string(data))
	}
	code, _ = call(t, app, "PUT", "/api/recall/settings", map[string]any{"version": settings["version"], "recall": policy})
	if code != 409 {
		t.Fatal("stale config overwrote newer save", code)
	}
	code, response := call(t, app, "POST", "/api/recall", map[string]any{"message": "Remember that afternoon", "owners": []string{"ari"}})
	if code != 200 {
		t.Fatal(code, response)
	}
	result := response["results"].([]any)[0].(map[string]any)
	if result["status"] != "ready" || result["items"].([]any)[0].(map[string]any)["id"] != doc.ID {
		t.Fatal(response)
	}
	count := requests.Load()
	call(t, app, "POST", "/api/recall", map[string]any{"messages": []string{"Remember that afternoon", "Remember that afternoon"}, "owners": []string{"ari"}})
	if requests.Load() != count {
		t.Fatal("batch did not reuse vectors")
	}
	code, _ = call(t, app, "POST", "/api/recall", map[string]any{"message": "afternoon"})
	if code != 400 {
		t.Fatal("implicit all-owner retrieval", code)
	}
	// Reopening the application uses the same persisted policy.
	app.Close()
	app = Handler(store)
	defer app.Close()
	_, settings = call(t, app, "GET", "/api/recall/settings", nil)
	if settings["recall"].(map[string]any)["direct_limit"] != float64(1) {
		t.Fatal(settings)
	}
}
func mustJSON(v any) []byte { data, _ := json.Marshal(v); return data }

func TestChatAutomaticallyCapturesRecallAndKeepsItAcrossRetry(t *testing.T) {
	root := t.TempDir()
	embed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		write(w, map[string]any{"data": []any{map[string]any{"embedding": []int{1, 0}}}})
	}))
	defer embed.Close()
	store, _ := songya.Open(root, songya.EmbeddingConfig{BaseURL: embed.URL})
	memory, _ := store.Create(songya.Memories, map[string]any{"user": "ari"}, "Memory evidence: the blue bicycle.")
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []chatMessage `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		data := string(mustJSON(body.Messages))
		if !strings.Contains(data, memory.ID) || !strings.Contains(data, "blue bicycle") || !strings.Contains(data, "untrusted data") {
			t.Error("automatic recall absent", data)
		}
		if strings.Contains(data, "replacement content") {
			t.Error("accepted recall changed during retry")
		}
		chatChunk(w, map[string]any{"role": "assistant", "content": "Your bicycle was blue."}, nil)
		if calls.Add(1) > 1 {
			chatFinish(w, "stop")
		}
	}))
	defer provider.Close()
	app := Handler(store)
	defer app.Close()
	configureChat(t, app, provider.URL)
	id := chatID()
	postChat(t, app, id, "accepted", "ari", "What color was my bicycle?")
	c := waitChat(t, app, id, "error")
	if c.Recall == nil || c.RecallConfiguration == nil || len(c.Recall.Results[0].Items) != 1 {
		t.Fatal(c)
	}
	store.Update(memory.ID, memory.Revision, nil, "replacement content")
	p := songya.DefaultRecallPolicy()
	p.DirectLimit = 0
	raw, _ := json.Marshal(map[string]any{"recall": p})
	fixtureWriteFile(filepath.Join(root, "config.json"), raw, 0600)
	app.Close()
	app = Handler(store)
	defer app.Close()
	code, _ := call(t, app, "POST", "/api/chat/sessions/"+id+"/retry", nil)
	if code != 202 {
		t.Fatal(code)
	}
	waitChat(t, app, id, "done")
	_, public := call(t, app, "GET", "/api/chat/sessions/"+id, nil)
	if public["recall_configuration"] != nil {
		t.Fatal("captured credentials exposed", public)
	}
	// A new turn uses the new policy rather than the prior turn's snapshot.
	_, err := app.chat.edit(context.Background(), id, func(c *chatConversation) error { c.Status = "done"; return nil })
	if err != nil {
		t.Fatal(err)
	}
}
