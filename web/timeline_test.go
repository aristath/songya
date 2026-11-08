package web

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aristath/songya"
)

func TestTimelineHTTPShowsUnknownDatesAndConflictsWithoutChangingRecords(t *testing.T) {
	store, _ := songya.Open(t.TempDir())
	first, _ := store.Create(songya.Memories, map[string]any{"user": "reader", "title": "Mary's walk", "time": map[string]any{"date": "2000-01-01"}}, "A walk by the lake.")
	second, _ := store.Create(songya.Memories, map[string]any{"user": "reader", "title": "John's visit", "relations": []songya.Relation{{Type: "after", Target: first.ID}}}, "A later visit.")
	other, _ := store.Create(songya.Memories, map[string]any{"user": "other", "title": "Another account"}, "A separate collection.")
	path := filepath.Join(store.Directory(), "memories", second.ID+".md")
	// A direct edit leaves an unreadable date, but the intact account stays usable.
	if err := second.Set("time", map[string]any{"earliest": "sometimes"}); err != nil {
		t.Fatal(err)
	}
	damaged, err := second.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, damaged, 0600); err != nil {
		t.Fatal(err)
	}
	app := Handler(store)
	defer app.Close()
	code, result := call(t, app, "GET", "/api/timeline?user=reader&classification_user=reader", nil)
	if code != 200 {
		t.Fatal(code, result)
	}
	rows := result["records"].([]any)
	if len(rows) != 2 {
		t.Fatal("owner filter lost accounts", result)
	}
	seen := map[string]map[string]any{}
	for _, row := range rows {
		r := row.(map[string]any)
		seen[r["id"].(string)] = r
	}
	if seen[other.ID] != nil || seen[first.ID]["time"].(map[string]any)["date"] != "2000-01-01" {
		t.Fatal(result)
	}
	if seen[second.ID]["excerpt"] != "A later visit." || len(seen[second.ID]["issues"].([]any)) == 0 {
		t.Fatal("bad timing hid the account or went unreported", result)
	}
	if len(result["constraints"].([]any)) != 1 {
		t.Fatal("a damaged date disabled a valid after link", result)
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(damaged) {
		t.Fatal("timeline read rewrote the record")
	}
}
