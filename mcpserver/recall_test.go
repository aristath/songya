package mcpserver_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aristath/songya"
)

func TestRecallThroughMCP(t *testing.T) {
	path := t.TempDir()
	store, err := songya.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	makeRecord := func(kind songya.Destination, fields map[string]any, body string) *songya.Document {
		t.Helper()
		d, err := store.Create(kind, fields, body)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	entity := makeRecord(songya.Entities, map[string]any{"name": "Dimitra", "aliases": []string{"Dimitra Sayas"}}, "A musician.")
	before := makeRecord(songya.Memories, map[string]any{"user": "ari", "title": "Before they met"}, "Ari had tried to reach Athanasia.")
	account := makeRecord(songya.Memories, map[string]any{"user": "ari", "title": "Meeting Dimitra", "time": map[string]any{"date": nil, "earliest": "2000-01-01", "latest": "2004-12-31"}, "source": map[string]any{"migration_details": strings.Repeat("historical metadata ", 1000)}, "relations": []songya.Relation{{Type: "related_to", Target: entity.ID}}}, "In the early 2000s, he contacted Dimitra instead and hoped to stay together.")
	makeRecord(songya.Stories, map[string]any{"user": "ari", "title": "Their relationship", "relations": []songya.Relation{{Type: "related_to", Target: before.ID}, {Type: "related_to", Target: account.ID}}}, "")
	makeRecord(songya.Stories, map[string]any{"user": "ari", "title": "A later account", "relations": []songya.Relation{{Type: "related_to", Target: entity.ID}}}, "The breakup still hurt him years later.")
	makeRecord(songya.Memories, map[string]any{"user": "clara", "relations": []songya.Relation{{Type: "related_to", Target: entity.ID}}}, "Clara's distinct recollection.")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	session := connect(t, ctx, binary(t), path, "")
	envelope, result := invoke(t, ctx, session, "recall_memory", map[string]any{"text": "Dimitra Sayas", "user": "ari"})
	data := payload(t, envelope)
	text, _ := data["text"].(string)
	if result.IsError || data["truncated"] != false {
		t.Fatal("normal recall failed", envelope)
	}
	for _, want := range []string{entity.ID, before.ID, account.ID, "Athanasia", "early 2000s", "breakup still hurt"} {
		if !strings.Contains(text, want) {
			t.Fatalf("one call missed %q: %s", want, text)
		}
	}
	if strings.Contains(text, "migration_details") {
		t.Fatal("recall leaked bookkeeping or another user", text)
	}
	for _, view := range []string{"", "source"} {
		envelope, _ = invoke(t, ctx, session, "read_memory", map[string]any{"id": account.ID, "view": view})
		read := payload(t, envelope)["text"].(string)
		if view == "" && read != account.Body || view == "source" && !strings.Contains(read, "migration_details") {
			t.Fatal("wrong read view", view, read)
		}
	}
	envelope, _ = invoke(t, ctx, session, "explore_memory", map[string]any{"id": entity.ID})
	for _, v := range payload(t, envelope)["edges"].([]any) {
		e := v.(map[string]any)
		if e["source_title"] == nil || e["target_title"] != "Dimitra" {
			t.Fatal("graph has bare IDs", e)
		}
	}
	// Escaping expands the wire response. Continuation must still deliver the
	// entire prose rather than the generic envelope's oversized-result message.
	for _, body := range []string{strings.Repeat("\x00<&>α😀", 5000), strings.Repeat(string([]byte{0xff})+"α", 12000)} {
		long := makeRecord(songya.Memories, map[string]any{"user": "ari", "title": "Large account"}, body)
		var joined strings.Builder
		offset := 0
		for calls := 0; calls < 100; calls++ {
			envelope, result = invoke(t, ctx, session, "recall_memory", map[string]any{"text": long.ID, "offset": fmt.Sprint(offset), "limit": "16384"})
			encoded, _ := json.Marshal(envelope)
			if len(encoded) > 28*1024 || result.IsError {
				t.Fatal("wire response exceeded budget", len(encoded), envelope)
			}
			data = payload(t, envelope)
			part, ok := data["text"].(string)
			if encoded, exists := data["base64"].(string); exists {
				raw, err := base64.StdEncoding.DecodeString(encoded)
				if err != nil {
					t.Fatal(err)
				}
				part = string(raw)
				ok = true
			}
			if !ok {
				t.Fatal("lost page to safety cap", envelope)
			}
			joined.WriteString(part)
			if data["truncated"] == false {
				break
			}
			next, ok := data["next"].(float64)
			if !ok || int(next) <= offset {
				t.Fatal("bad continuation", envelope)
			}
			offset = int(next)
		}
		if !strings.HasSuffix(joined.String(), body) {
			t.Fatal("MCP pagination lost prose", joined.Len(), len(body))
		}
	}
}
