package mcpserver_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aristath/songya"
)

func TestMCPEmbeddingConfigurationAndSemanticSearch(t *testing.T) {
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Model, Input string }
		if r.URL.Path != "/v1/embeddings" || json.NewDecoder(r.Body).Decode(&in) != nil {
			http.Error(w, "bad request", 400)
			return
		}
		if (in.Model != "77" && in.Model != "custom-model") || r.Header.Get("Authorization") != "Bearer fixture-key" {
			http.Error(w, "wrong auth", 401)
			return
		}
		vector := []float64{1, 0}
		if strings.Contains(in.Input, "garden") {
			vector = []float64{0, 1}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"embedding": vector}}})
	}))
	defer service.Close()
	path := t.TempDir()
	s, err := songya.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	entity, err := s.Create(songya.Entities, map[string]any{"name": "Alex Morgan", "description": "A cellist."}, "A musician.")
	if err != nil {
		t.Fatal(err)
	}
	memory, err := s.Create(songya.Memories, map[string]any{"user": "ari", "title": "An evening"}, "She played the violoncello.")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Create(songya.Memories, map[string]any{"user": "bob"}, "Bob grows a garden.")
	if err != nil {
		t.Fatal(err)
	}
	config := map[string]any{"embeddings": map[string]any{"model": 77, "base_url": service.URL + "/v1/", "api_key": "fixture-key", "chunk_size": "2000"}}
	data, _ := json.Marshal(config)
	if err := fixtureWriteFile(filepath.Join(path, "config.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	bin := binary(t)
	session := connect(t, ctx, bin, path, "")
	envelope, r := invoke(t, ctx, session, "memory_maintenance", map[string]any{"action": "embed"})
	status := payload(t, envelope)
	if r.IsError || status["model"] != "77" || status["indexed"] != float64(3) || status["incomplete"] != false || status["dimensions"] != float64(2) {
		t.Fatal(envelope)
	}
	envelope, r = invoke(t, ctx, session, "search_memory", map[string]any{"mode": "semantic", "text": "orchestral performer", "kind": "memories", "user": "ari"})
	result := payload(t, envelope)
	if r.IsError || result["mode"] != "semantic" {
		t.Fatal(envelope)
	}
	matches := result["matches"].([]any)
	if len(matches) != 1 || matches[0].(map[string]any)["id"] != memory.ID {
		t.Fatal(envelope)
	}
	envelope, _ = invoke(t, ctx, session, "search_memory", map[string]any{"mode": "semantic", "text": "orchestral performer", "kind": "entities", "user": "bob"})
	matches = payload(t, envelope)["matches"].([]any)
	if len(matches) != 1 || matches[0].(map[string]any)["id"] != entity.ID {
		t.Fatal("shared entity unavailable", envelope)
	}
	// The operator can choose a separate configuration file without editing records.
	config["embeddings"].(map[string]any)["model"] = "custom-model"
	data, _ = json.Marshal(config)
	override := filepath.Join(t.TempDir(), "songya.json")
	if err := fixtureWriteFile(override, data, 0600); err != nil {
		t.Fatal(err)
	}
	second := connect(t, ctx, bin, path, "", "--config", override)
	envelope, r = invoke(t, ctx, second, "memory_maintenance", map[string]any{"action": "embed"})
	if r.IsError || payload(t, envelope)["model"] != "custom-model" {
		t.Fatal("config flag ignored", envelope)
	}
}
