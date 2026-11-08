package web

import (
	"context"
	"testing"

	"github.com/aristath/songya"
)

func TestTypeColorsReachGraphAndLibraryForTheSelectedUser(t *testing.T) {
	store, _ := songya.Open(t.TempDir())
	ctx := context.Background()
	app := Handler(store)
	defer app.Close()
	cfg, _ := store.TypeConfiguration(ctx, "reader")
	cfg.Kinds[songya.Entities][0].Color = "#e84d92"
	cfg.KindColors[songya.Memories] = "blue"
	cfg.Kinds[songya.Stories] = []songya.TypeDefinition{{ID: "project", Name: "Project", Description: "A project account.", Color: "teal"}}
	if _, err := store.SaveTypeConfiguration(ctx, cfg, cfg.Version, nil, "colors"); err != nil {
		t.Fatal(err)
	}
	entity, _ := store.Create(songya.Entities, map[string]any{"name": "John", "types": map[string]string{"reader": "person", "other": "person"}}, "A named person.")
	memory, _ := store.Create(songya.Memories, map[string]any{"user": "reader", "title": "A walk"}, "An afternoon.")
	story, _ := store.Create(songya.Stories, map[string]any{"user": "reader", "title": "Project Orion", "types": map[string]string{"reader": "project"}}, "A project.")
	for _, user := range []string{"reader", "other"} {
		for _, route := range []string{"/api/graph?classification_user=", "/api/records?classification_user="} {
			code, result := call(t, app, "GET", route+user, nil)
			if code != 200 {
				t.Fatal(code, result)
			}
			key := "records"
			if result["nodes"] != nil {
				key = "nodes"
			}
			found := map[string]string{}
			for _, item := range result[key].([]any) {
				row := item.(map[string]any)
				found[row["id"].(string)] = row["color"].(string)
			}
			if user == "reader" {
				if found[entity.ID] != "#e84d92" || found[memory.ID] != "blue" || found[story.ID] != "teal" {
					t.Fatal("configured colors were not projected", result)
				}
			} else if found[entity.ID] != "mauve" || found[memory.ID] != "peach" || found[story.ID] != "red" {
				t.Fatal("another user's colors leaked", result)
			}
		}
	}
}
