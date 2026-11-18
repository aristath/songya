package web

import (
	"context"
	"testing"

	"github.com/aristath/songya"
)

func TestTypeColorsReachGraphAndLibraryForTheStore(t *testing.T) {
	store, _ := songya.Open(t.TempDir())
	ctx := context.Background()
	app := Handler(store)
	defer app.Close()
	cfg, _ := store.TypeConfiguration(ctx)
	cfg.Kinds[songya.Entities][0].Color = "#e84d92"
	cfg.KindColors[songya.Memories] = "blue"
	cfg.Kinds[songya.Stories] = []songya.TypeDefinition{{ID: "project", Name: "Project", Description: "A project account.", Color: "teal"}}
	if _, err := store.SaveTypeConfiguration(ctx, cfg, cfg.Version, nil, "colors"); err != nil {
		t.Fatal(err)
	}
	entity, _ := store.Create(songya.Entities, map[string]any{"name": "John", "type": "person"}, "A named person.")
	memory, _ := store.Create(songya.Memories, map[string]any{"user": "reader", "title": "A walk"}, "An afternoon.")
	story, _ := store.Create(songya.Stories, map[string]any{"user": "reader", "title": "Project Orion", "type": "project"}, "A project.")
	{
		for _, route := range []string{"/api/graph", "/api/records"} {
			code, result := call(t, app, "GET", route, nil)
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
			if found[entity.ID] != "#e84d92" || found[memory.ID] != "blue" || found[story.ID] != "teal" {
				t.Fatal(result)
			}
		}
	}
}
