package mcpserver_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aristath/songya"
)

func TestEntityLifetimeMCPDatesPartialUpdatesNullAndCustomTypes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	root := t.TempDir()
	store, _ := songya.Open(root)
	cfg, err := store.TypeConfiguration(ctx, "reader")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Kinds[songya.Entities] = append(cfg.Kinds[songya.Entities], songya.TypeDefinition{ID: "project", Name: "Project", Description: "A named project."})
	if _, err = store.SaveTypeConfiguration(ctx, cfg, cfg.Version, nil, "project-type"); err != nil {
		t.Fatal(err)
	}
	session := connect(t, ctx, binary(t), root, "")
	data := keyed(t, ctx, session, "type_configuration", "choices", map[string]any{"user": "reader", "schemas": true})
	schemas := data["write_schemas"].(map[string]any)
	entity := schemas["save_entity"].(map[string]any)
	raw, _ := json.Marshal(entity["properties"].(map[string]any)["time"])
	if !strings.Contains(string(raw), "user-configured entity type") || strings.Contains(string(raw), "earliest") {
		t.Fatal(string(raw))
	}
	created := keyed(t, ctx, session, "save_entity", "project", map[string]any{"create": true, "name": "Orion project", "type": "project", "type_user": "reader", "description": "A named research project.", "time": map[string]any{"start": "2020-05-04"}})
	id := created["id"].(string)
	if created["kind"] != "entities" || created["outcome"] != "created" {
		t.Fatal("save feedback did not identify the created entity", created)
	}
	timing := created["time"].(map[string]any)
	if len(timing) != 2 || timing["start"] != "2020-05-04" || timing["end"] != nil {
		t.Fatal(created)
	}
	saved := keyed(t, ctx, session, "save_entity", "project-end", map[string]any{"id": id, "time": map[string]any{"end": "2025-06-01"}})
	if saved["kind"] != "entities" || saved["id"] != id || saved["outcome"] != "updated" || saved["description"] != created["description"] {
		t.Fatal("date update feedback did not identify the same entity and preserved description", saved)
	}
	timing = saved["time"].(map[string]any)
	if timing["start"] != "2020-05-04" || timing["end"] != "2025-06-01" {
		t.Fatal(saved)
	}
	before, _ := store.Read(id)
	for _, bad := range []any{map[string]any{"start": "2020"}, map[string]any{"end": "ongoing"}, map[string]any{"date": "2020-05-04"}, map[string]any{"earliest": "2020-05-04"}, map[string]any{"end": "2019-01-01"}} {
		_, result := invoke(t, ctx, session, "save_entity", map[string]any{"id": id, "time": bad, "description": "Must not overwrite."})
		if !result.IsError {
			t.Fatal("invalid lifetime accepted", bad)
		}
	}
	after, _ := store.Read(id)
	if after.Revision != before.Revision {
		t.Fatal("rejected writes changed the entity")
	}
	cleared := keyed(t, ctx, session, "save_entity", "clear-one", map[string]any{"id": id, "time": map[string]any{"end": nil}})
	if cleared["time"].(map[string]any)["start"] != "2020-05-04" {
		t.Fatal(cleared)
	}
	cleared = keyed(t, ctx, session, "save_entity", "clear-all", map[string]any{"id": id, "time": nil})
	timing = cleared["time"].(map[string]any)
	if len(timing) != 2 || timing["start"] != nil || timing["end"] != nil {
		t.Fatal(cleared)
	}
	after, _ = store.Read(id)
	if after.Body != "A named research project." {
		t.Fatal("date edits rewrote the description")
	}
}
