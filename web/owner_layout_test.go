package web

import (
	"context"
	"testing"

	"github.com/aristath/songya"
)

func TestGraphUsesOwnerIdentityWithoutSavedConnections(t *testing.T) {
	s, _ := songya.Open(t.TempDir())
	app := testApplication(t, s)
	john, _ := s.Create(songya.Entities, map[string]any{"name": "John"}, "Identity")
	mary, _ := s.Create(songya.Entities, map[string]any{"name": "Mary"}, "Identity")
	for user, entity := range map[string]string{"john": john.ID, "mary": mary.ID} {
		cfg, _ := s.TypeConfiguration(context.Background(), user)
		cfg.MyEntity = entity
		if _, err := s.SaveTypeConfiguration(context.Background(), cfg, cfg.Version, nil, "identity-"+user); err != nil {
			t.Fatal(err)
		}
	}
	ids := map[string]string{}
	for _, user := range []string{"john", "mary", "unconfigured"} {
		memory, err := s.Create(songya.Memories, map[string]any{"user": user}, "Event")
		if err != nil {
			t.Fatal(err)
		}
		ids[memory.ID] = map[string]string{"john": john.ID, "mary": mary.ID}[user]
	}
	story, _ := s.Create(songya.Stories, map[string]any{"user": "john"}, "A story")
	ids[story.ID] = ""
	code, result := call(t, app, "GET", "/api/graph?classification_user=mary", nil)
	if code != 200 || len(result["links"].([]any)) != 0 {
		t.Fatal(code, result)
	}
	for _, item := range result["nodes"].([]any) {
		node := item.(map[string]any)
		id := node["id"].(string)
		if entity, ok := ids[id]; ok {
			if entity != "" && node["owner_entity"] != entity {
				t.Fatal("wrong ownership attraction", node)
			}
			if entity == "" && node["owner_entity"] != nil {
				t.Fatal("invented ownership attraction", node)
			}
			doc, _ := s.Read(id)
			if len(doc.Metadata().Relations) != 0 {
				t.Fatal("graph wrote a relationship", doc)
			}
		}
	}
}
