package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aristath/songya"
)

func TestChatTemperatureDefaultsZeroPersistenceAndAcceptedTurn(t *testing.T) {
	root := t.TempDir()
	store, _ := songya.Open(root)
	var count atomic.Int32
	var app *Application
	var provider *httptest.Server
	provider = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Temperature *float64 `json:"temperature"`
			Parallel    *bool    `json:"parallel_tool_calls"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Temperature == nil || *body.Temperature != 0 || body.Parallel == nil || *body.Parallel {
			t.Error("temperature or single-tool setting lost", body)
		}
		if count.Add(1) == 1 {
			// A settings change must not alter an already accepted turn.
			code, _ := call(t, app, "PUT", "/api/chat/settings", map[string]any{"base_url": provider.URL + "/v1", "model": "memory-model", "temperature": 1.2})
			if code != 200 {
				t.Error(code)
			}
			chatFunction(w, "lookup", "prepare_memory", `{"text":"hello"}`)
		} else {
			chatChunk(w, map[string]any{"content": "Checked."}, nil)
			chatFinish(w, "stop")
		}
	}))
	defer provider.Close()
	app = Handler(store)
	defer func() { app.Close() }()
	configureChat(t, app, provider.URL)
	_, settings := call(t, app, "GET", "/api/chat/settings", nil)
	if settings["temperature"] != 0.15 {
		t.Fatal(settings)
	}
	for _, value := range []any{-0.1, 2.1, "bad", nil} {
		code, _ := call(t, app, "PUT", "/api/chat/settings", map[string]any{"base_url": provider.URL + "/v1", "model": "memory-model", "temperature": value})
		if code != 400 {
			t.Fatal("invalid temperature accepted", value, code)
		}
	}
	code, _ := call(t, app, "PUT", "/api/chat/settings", map[string]any{"base_url": provider.URL + "/v1", "model": "memory-model", "temperature": "0"})
	if code != 200 {
		t.Fatal(code)
	}
	app.Close()
	app = Handler(store)
	_, settings = call(t, app, "GET", "/api/chat/settings", nil)
	if settings["temperature"] != float64(0) {
		t.Fatal("zero temperature lost after restart", settings)
	}
	id := chatID()
	postChat(t, app, id, "accepted", "ari", "Hello")
	completed := waitChat(t, app, id, "done")
	if completed.Temperature == nil || *completed.Temperature != 0 {
		t.Fatal(completed.Temperature)
	}
}

func TestChatRepairsMalformedEntityCallWithoutRunningIt(t *testing.T) {
	store, _ := songya.Open(t.TempDir())
	old := "Former partner in 2019–2020."
	saved, _ := store.SaveNamed(context.Background(), songya.Entities, songya.NamedInput{Create: true, Name: "Semina", Description: &old})
	id := saved.Refs["record"]
	var requests atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch requests.Add(1) {
		case 1:
			chatChunk(w, map[string]any{"content": "<parameter=description>May to November 2019</parameter>\n</function>\n</tool_call>"}, nil)
			chatFunction(w, "broken", "save_entity", fmt.Sprintf(`{"id":%q,"name":"WRONG NAME"}`, id))
		case 2:
			doc, _ := store.Read(id)
			if doc.Body != old || doc.Record(songya.Entities).Title != "Semina" {
				t.Error("malformed reply mutated the record")
			}
			chatFunction(w, "fixed", "save_entity", fmt.Sprintf(`{"id":%q,"description":"Former partner in May–November 2019."}`, id))
		default:
			chatChunk(w, map[string]any{"content": "The description is corrected."}, nil)
			chatFinish(w, "stop")
		}
	}))
	defer provider.Close()
	app := Handler(store)
	defer app.Close()
	configureChat(t, app, provider.URL)
	chat := chatID()
	words := "  Semina and I were together from May to November 2019.\n"
	postChat(t, app, chat, "correction", "ari", words)
	c := waitChat(t, app, chat, "done")
	if !strings.Contains(c.Messages[2].Content, "No tool in that reply was run") {
		t.Fatal(c.Messages)
	}
	doc, _ := store.Read(id)
	if !strings.Contains(doc.Body, "May–November 2019") {
		t.Fatal(doc.Body)
	}
	fixedIndex := -1
	for i, m := range c.Messages {
		for _, tool := range m.ToolCalls {
			if tool.ID == "fixed" {
				fixedIndex = i
			}
		}
	}
	account, err := store.BatchData(context.Background(), fmt.Sprintf("chat:%s:%d:fixed", chat, fixedIndex), "", "account")
	if err != nil || string(account) != words {
		t.Fatalf("source is not the person's exact words: %q %v", account, err)
	}
}

func TestChatStopsRepeatedNoOpCallsAndCanResumeWithCorrectArguments(t *testing.T) {
	store, _ := songya.Open(t.TempDir())
	old := "Old date."
	saved, _ := store.SaveNamed(context.Background(), songya.Entities, songya.NamedInput{Create: true, Name: "Semina", Description: &old})
	id := saved.Refs["record"]
	var requests atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requests.Add(1)
		if n <= 3 {
			chatFunction(w, fmt.Sprint(n), "save_entity", fmt.Sprintf(`{"id":%q,"name":"Semina"}`, id))
		} else if n == 4 {
			chatFunction(w, "fixed", "save_entity", fmt.Sprintf(`{"id":%q,"description":"Correct date."}`, id))
		} else {
			chatChunk(w, map[string]any{"content": "Corrected."}, nil)
			chatFinish(w, "stop")
		}
	}))
	defer provider.Close()
	app := Handler(store)
	defer app.Close()
	configureChat(t, app, provider.URL)
	chat := chatID()
	postChat(t, app, chat, "change", "ari", "Correct the date.")
	c := waitChat(t, app, chat, "error")
	if !strings.Contains(c.Error, "repeated a call") || requests.Load() != 3 {
		t.Fatal(c.Error, requests.Load())
	}
	code, _ := call(t, app, "POST", "/api/chat/sessions/"+chat+"/retry", nil)
	if code != 202 {
		t.Fatal(code)
	}
	waitChat(t, app, chat, "done")
	doc, _ := store.Read(id)
	if doc.Body != "Correct date." {
		t.Fatal(doc.Body)
	}
}

func TestChatStopsRepeatedFailedEmptyWritesAndResumesWithValidCreation(t *testing.T) {
	store, _ := songya.Open(t.TempDir())
	var requests atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requests.Add(1)
		if n <= 3 {
			chatFunction(w, fmt.Sprint(n), "save_entity", `{}`)
		} else if n == 4 {
			chatFunction(w, "fixed", "save_entity", `{"create":true,"name":"Alex","description":"Sam's coworker."}`)
		} else {
			chatChunk(w, map[string]any{"content": "The new coworker is saved."}, nil)
			chatFinish(w, "stop")
		}
	}))
	defer provider.Close()
	app := Handler(store)
	defer app.Close()
	configureChat(t, app, provider.URL)
	id := chatID()
	postChat(t, app, id, "new-person", "sam", "Remember my coworker Alex.")
	c := waitChat(t, app, id, "error")
	if !strings.Contains(c.Error, "repeated a call") || requests.Load() != 3 {
		t.Fatal("failed writes kept looping", c.Error, requests.Load())
	}
	catalog, _ := store.Scan(context.Background())
	if len(catalog.Records) != 0 {
		t.Fatal("empty calls created records", catalog.Records)
	}
	code, _ := call(t, app, "POST", "/api/chat/sessions/"+id+"/retry", nil)
	if code != 202 {
		t.Fatal(code)
	}
	waitChat(t, app, id, "done")
	catalog, _ = store.Scan(context.Background())
	if len(catalog.Records) != 1 || catalog.Records[0].Title != "Alex" {
		t.Fatal("recovery did not create exactly one person", catalog.Records)
	}
}

func TestChatRawToolTagsRecoverWithoutExecutingText(t *testing.T) {
	for _, repeated := range []bool{false, true} {
		t.Run(fmt.Sprint(repeated), func(t *testing.T) {
			store, _ := songya.Open(t.TempDir())
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
						t.Error("late system message", body.Messages)
					}
				}
				n := requests.Add(1)
				if n == 2 && !strings.Contains(body.Messages[0].Content, toolRepair) {
					t.Error("opening prompt lacks active repair instructions")
				}
				text := "I could not complete a tool call."
				if n == 1 || repeated {
					text = `<tool_call>{"name":"save_memory","arguments":{"text":"Do not execute this text."}}</tool_call>`
				}
				chatChunk(w, map[string]any{"content": text}, nil)
				chatFinish(w, "stop")
			}))
			defer provider.Close()
			app := Handler(store)
			defer app.Close()
			configureChat(t, app, provider.URL)
			id := chatID()
			postChat(t, app, id, "accepted", "ari", "Hello")
			status := "done"
			if repeated {
				status = "error"
			}
			c := waitChat(t, app, id, status)
			if requests.Load() != 2 {
				t.Fatal("unbounded tool repair", requests.Load())
			}
			for _, m := range c.Messages {
				if m.Role == "system" {
					t.Fatal("app recovery note was added to conversation", c.Messages)
				}
			}
			if originalTurnText(c) != "Hello" {
				t.Fatal("recovery changed original testimony")
			}
			catalog, _ := store.Scan(context.Background())
			if len(catalog.Records) != 0 {
				t.Fatal("XML text was executed")
			}
		})
	}
}
