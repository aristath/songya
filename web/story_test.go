package web

import (
	"strings"
	"testing"

	"github.com/aristath/songya"
)

func TestStoryReaderHTTPPagesCanonicalPartsWithoutChangingRecords(t *testing.T) {
	store, _ := songya.Open(t.TempDir())
	memory, err := store.Create(songya.Memories, map[string]any{"user": "reader", "title": "A walk"}, strings.Repeat("Mary remembered the lake. ", 1000))
	if err != nil {
		t.Fatal(err)
	}
	story, err := store.Create(songya.Stories, map[string]any{"user": "reader", "title": "A journey", "relations": []songya.Relation{{Type: "related_to", Target: memory.ID}, {Type: "related_to", Target: "m_000000000000"}}}, "An introduction.")
	if err != nil {
		t.Fatal(err)
	}
	app := Handler(store)
	defer app.Close()
	code, first := call(t, app, "GET", "/api/stories/"+story.ID+"/compose", nil)
	if code != 200 || first["truncated"] != true || first["next"].(float64) != 16384 {
		t.Fatal(code, first)
	}
	parts := first["parts"].([]any)
	if parts[0].(map[string]any)["id"] != story.ID || parts[1].(map[string]any)["id"] != memory.ID {
		t.Fatal("parts lost their record IDs", first)
	}
	code, second := call(t, app, "GET", "/api/stories/"+story.ID+"/compose?offset=16384", nil)
	if code != 200 || second["truncated"] != false || len(second["diagnostics"].([]any)) == 0 {
		t.Fatal(code, second)
	}
	continuation := second["parts"].([]any)[0].(map[string]any)
	if continuation["id"] != memory.ID || continuation["start"] != parts[1].(map[string]any)["start"] {
		t.Fatal("continuation lost its original memory", second)
	}
	if parts[1].(map[string]any)["text"].(string)+continuation["text"].(string) != memory.Body {
		t.Fatal("paging changed the memory")
	}
	code, _ = call(t, app, "GET", "/api/stories/"+story.ID+"/compose?offset=oops", nil)
	if code != 400 {
		t.Fatal("invalid cursor was accepted", code)
	}
	for _, before := range []*songya.Document{story, memory} {
		after, _ := store.Read(before.ID)
		if after.Revision != before.Revision {
			t.Fatal("reading a story modified a record")
		}
	}
}
