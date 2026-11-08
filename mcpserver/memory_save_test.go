package mcpserver_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aristath/songya"
)

func TestMemorySaveChangesDatesAndRetryOutcomes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	path, bin := t.TempDir(), binary(t)
	session := connect(t, ctx, bin, path, "")
	birth := keyed(t, ctx, session, "save_memory", "birth", map[string]any{
		"user": "ari", "title": "Birth", "text": "Ari was born in Thessaloniki on 6 March 1981.",
		"time":      map[string]any{"date": "1981-03-06"},
		"relations": []any{map[string]any{"type": "related_to", "target": "Ari"}},
	})
	if birth["outcome"] != "created" || birth["time"].(map[string]any)["date"] != "1981-03-06" {
		t.Fatal(birth)
	}
	id := birth["id"].(string)
	filename := filepath.Join(path, "memories", id+"/record.md")
	original, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	for i, args := range []map[string]any{{"id": id}, {"id": id, "user": "ari"}, {"id": id, "text": birth["text"]}} {
		saved := keyed(t, ctx, session, "save_memory", fmt.Sprintf("unchanged-%d", i), args)
		if saved["outcome"] != "unchanged" || len(saved["changed_fields"].([]any)) != 0 || !strings.Contains(fmt.Sprint(saved["diagnostics"]), "Nothing changed") {
			t.Fatal(saved)
		}
		after, err := os.ReadFile(filename)
		if err != nil || string(after) != string(original) {
			t.Fatal("unchanged save rewrote the memory", err)
		}
	}
	move := keyed(t, ctx, session, "save_memory", "move", map[string]any{
		"user": "ari", "text": "Ari's family moved to Mytilene a few months after his birth, still in 1981. May is the earliest plausible month in this account; the exact move date is unknown.",
		"time":      map[string]any{"earliest": "1981-05-01", "latest": "1981-12-31"},
		"relations": []any{map[string]any{"type": "after", "target": id}},
	})
	moveID := move["id"].(string)
	patched := keyed(t, ctx, session, "save_memory", "date-only", map[string]any{"id": moveID, "time": map[string]any{"latest": "1981-11-30"}})
	if patched["outcome"] != "updated" || !reflect.DeepEqual(patched["changed_fields"], []any{"time"}) || patched["text"] != move["text"] || !reflect.DeepEqual(patched["relations"], move["relations"]) {
		t.Fatal("date-only update lost other data", patched)
	}
	if timing := patched["time"].(map[string]any); timing["earliest"] != "1981-05-01" || timing["latest"] != "1981-11-30" {
		t.Fatal(timing)
	}
	appended := keyed(t, ctx, session, "save_memory", "append", map[string]any{"id": id, "append": "His family moved later that year.", "relations": []any{map[string]any{"type": "related_to", "target": "Thessaloniki"}}})
	if appended["text"] != birth["text"].(string)+"\n\nHis family moved later that year." || len(appended["relations"].([]any)) != 2 || !reflect.DeepEqual(appended["time"], birth["time"]) {
		t.Fatal(appended)
	}
	corrected := keyed(t, ctx, session, "save_memory", "replace-and-append", map[string]any{"id": id, "text": birth["text"], "append": "The exact date is confirmed.", "title": ""})
	if corrected["text"] != birth["text"].(string)+"\n\nThe exact date is confirmed." {
		t.Fatal(corrected)
	}
	data, err := os.ReadFile(filename)
	if err != nil || songya.Parse(data).Metadata().Title != "" {
		t.Fatal("title was not cleared", err)
	}
	metadata := keyed(t, ctx, session, "save_memory", "metadata-only", map[string]any{"id": id, "metadata": map[string]any{"confirmed": true}})
	if !reflect.DeepEqual(metadata["changed_fields"], []any{"confirmed"}) || metadata["text"] != corrected["text"] {
		t.Fatal(metadata)
	}
	connections := keyed(t, ctx, session, "save_memory", "connections-only", map[string]any{"id": id, "relations": []any{}})
	if !reflect.DeepEqual(connections["changed_fields"], []any{"relations"}) || len(connections["relations"].([]any)) != 0 || connections["text"] != corrected["text"] {
		t.Fatal(connections)
	}
	cleared := keyed(t, ctx, session, "save_memory", "clear-time", map[string]any{"id": moveID, "time": nil})
	if timing := cleared["time"].(map[string]any); timing["date"] != nil || timing["earliest"] != nil || timing["latest"] != nil {
		t.Fatal(cleared)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	session = connect(t, ctx, bin, path, "")
	retried := keyed(t, ctx, session, "save_memory", "date-only", map[string]any{"id": moveID, "time": map[string]any{"latest": "1981-11-30"}})
	for _, field := range []string{"status", "outcome", "changed_fields", "operation_id", "id", "text", "time"} {
		if !reflect.DeepEqual(retried[field], patched[field]) {
			t.Fatal("retry lost its original saved outcome", field, retried, patched)
		}
	}
	read, r := invoke(t, ctx, session, "read_memory", map[string]any{"id": moveID, "view": "source"})
	if r.IsError || strings.Contains(payload(t, read)["text"].(string), "1981-11-30") {
		t.Fatal("retry replayed an old update", read)
	}
	store, err := songya.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	timeline, err := store.Timeline(ctx, songya.Query{Kind: songya.Memories, User: "ari"})
	if err != nil {
		t.Fatal(err)
	}
	ordered := false
	for _, edge := range timeline.Constraints {
		ordered = ordered || edge.Before == id && edge.After == moveID && edge.Usable
	}
	if !ordered {
		t.Fatal("after relation did not retain chronology", timeline)
	}
}
