package songya

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

type embeddingFixture struct {
	server   *httptest.Server
	mu       sync.Mutex
	requests []struct{ Model, Input, Auth string }
	status   func(string) int
}

func embeddingService(t *testing.T) *embeddingFixture {
	t.Helper()
	f := &embeddingFixture{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
			Input string `json:"input"`
		}
		if r.URL.Path != "/embeddings" || json.NewDecoder(r.Body).Decode(&body) != nil {
			http.Error(w, "bad request", 400)
			return
		}
		f.mu.Lock()
		f.requests = append(f.requests, struct{ Model, Input, Auth string }{body.Model, body.Input, r.Header.Get("Authorization")})
		status := 200
		if f.status != nil {
			status = f.status(body.Input)
		}
		f.mu.Unlock()
		if status != 200 {
			w.WriteHeader(status)
			return
		}
		vector := []float64{0, 0, 1}
		text := strings.ToLower(body.Input)
		if strings.Contains(text, "violoncello") || strings.Contains(text, "orchestr") {
			vector = []float64{5, 0, 0}
		}
		if strings.Contains(text, "rose") || strings.Contains(text, "garden") {
			vector = []float64{0, 3, 0}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"index": 0, "embedding": vector}}})
	}))
	t.Cleanup(f.server.Close)
	return f
}
func (f *embeddingFixture) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.requests) }
func createEmbeddedRecord(t *testing.T, s *Store, kind Destination, fields map[string]any, body string) *Document {
	t.Helper()
	d, err := s.Create(kind, fields, body)
	if err != nil {
		t.Fatal(err)
	}
	return d
}
func TestSemanticIndexNamesDescriptionsContentsAndRefresh(t *testing.T) {
	f := embeddingService(t)
	cfg := EmbeddingConfig{BaseURL: f.server.URL, APIKey: "fixture-key"}
	s, err := Open(t.TempDir(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	entity := createEmbeddedRecord(t, s, Entities, map[string]any{"name": "Alex Morgan", "title": "A friend", "aliases": []string{"Alex"}}, "Brief identity. She plays the violoncello.")
	memory := createEmbeddedRecord(t, s, Memories, map[string]any{"title": "An evening", "user": "ari"}, "She performed on the violoncello.")
	createEmbeddedRecord(t, s, Memories, map[string]any{"user": "ari"}, "The roses grow in the garden.")
	createEmbeddedRecord(t, s, Memories, map[string]any{"user": "bob"}, "Bob plays the violoncello.")
	original, err := s.ReadSource("memories/" + memory.ID + "/record.md")
	if err != nil {
		t.Fatal(err)
	}
	status, err := s.IndexEmbeddings(context.Background())
	if err != nil || status.Incomplete || status.Indexed != 4 || status.Dimensions != 3 || status.Model != "BAAI/bge-m3" {
		t.Fatal(status, err)
	}
	f.mu.Lock()
	entityText := ""
	for _, r := range f.requests {
		if r.Auth != "Bearer fixture-key" || r.Model != "BAAI/bge-m3" {
			t.Fatal("config not applied", r)
		}
		if strings.Contains(r.Input, "Alex Morgan") {
			entityText = r.Input
		}
	}
	f.mu.Unlock()
	for _, word := range []string{"Alex Morgan", "Alex", "violoncello", "Brief identity"} {
		if !strings.Contains(entityText, word) {
			t.Fatal("entity embedding lacks", word, entityText)
		}
	}
	before := f.count()
	result, err := s.Search(context.Background(), Query{Mode: "semantic", Text: "orchestral performer", User: "ari", Kind: Memories})
	if err != nil || result.Mode != "semantic" || result.Incomplete || result.Matches[0].ID != memory.ID || f.count() != before+1 {
		t.Fatal(result, err, "requests", f.count()-before)
	}
	for _, m := range result.Matches {
		if m.User != "ari" {
			t.Fatal("wrong user", m)
		}
	}
	result, err = s.Search(context.Background(), Query{Mode: "semantic", Text: "orchestral performer", User: "bob", Kind: Entities})
	if err != nil || len(result.Matches) != 1 || result.Matches[0].ID != entity.ID || result.Matches[0].User != "" {
		t.Fatal("shared identity lost", result, err)
	}
	after, _ := s.ReadSource("memories/" + memory.ID + "/record.md")
	if string(after) != string(original) {
		t.Fatal("indexing changed original memory")
	}
	doc, _ := s.Read(memory.ID)
	doc.Body = "A garden full of roses."
	raw, _ := doc.Marshal()
	if err := fixtureWriteFile(filepath.Join(s.path, "memories", memory.ID+"/record.md"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	before = f.count()
	status, err = s.IndexEmbeddings(context.Background())
	if err != nil || status.Incomplete || f.count() != before+1 {
		t.Fatal("changed content not refreshed independently", status, err, f.count()-before)
	}
	edited, _ := s.Read(entity.ID)
	_ = edited.Set("name", "Sam Lee")
	edited.Body = "A gardener tending roses."
	changed, _ := edited.Marshal()
	if err := fixtureWriteFile(filepath.Join(s.path, "entities", entity.ID+"/record.md"), changed, 0600); err != nil {
		t.Fatal(err)
	}
	before = f.count()
	status, err = s.IndexEmbeddings(context.Background())
	if err != nil || status.Incomplete || f.count() != before+1 {
		t.Fatal("entity change failed to refresh", status, err)
	}
	cfg.Model = "another-model"
	reopened, err := Open(s.path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	before = f.count()
	status, err = reopened.IndexEmbeddings(context.Background())
	if err != nil || status.Model != cfg.Model || f.count() != before+4 {
		t.Fatal("model change reused old vectors", status, err, f.count()-before)
	}
	another := embeddingService(t)
	cfg.BaseURL = another.server.URL
	other, err := Open(s.path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	status, err = other.IndexEmbeddings(context.Background())
	if err != nil || status.Incomplete || another.count() != 4 {
		t.Fatal("endpoint change reused old vectors", status, err)
	}
}

func TestEmbeddingCacheRepairAndUnwritableIndex(t *testing.T) {
	f := embeddingService(t)
	s, err := Open(t.TempDir(), EmbeddingConfig{BaseURL: f.server.URL})
	if err != nil {
		t.Fatal(err)
	}
	memory := createEmbeddedRecord(t, s, Memories, map[string]any{"user": "ari"}, "A violoncello performance.")
	_, err = s.IndexEmbeddings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(s.path, ".index", "embeddings", "*.json"))
	if len(files) != 1 {
		t.Fatal(files)
	}
	if err := fixtureWriteFile(files[0], []byte(`{"vector":[0,0,0],"checksum":"damaged"}`), 0600); err != nil {
		t.Fatal(err)
	}
	before := f.count()
	status, err := s.IndexEmbeddings(context.Background())
	if err != nil || status.Incomplete || f.count() != before+1 {
		t.Fatal("damaged vector not recovered", status, err)
	}
	// A cache symlink must never let a rebuild overwrite a canonical record.
	original, _ := os.ReadFile(filepath.Join(s.path, "memories", memory.ID+"/record.md"))
	_ = os.Remove(files[0])
	if err := os.Symlink(filepath.Join(s.path, "memories", memory.ID+"/record.md"), files[0]); err != nil {
		t.Fatal(err)
	}
	status, err = s.IndexEmbeddings(context.Background())
	after, _ := os.ReadFile(filepath.Join(s.path, "memories", memory.ID+"/record.md"))
	if err != nil || status.Incomplete || string(after) != string(original) {
		t.Fatal("cache repair damaged record", status, err)
	}
	if err := os.RemoveAll(filepath.Join(s.path, ".index", "embeddings")); err != nil {
		t.Fatal(err)
	}
	if err := fixtureWriteFile(filepath.Join(s.path, ".index", "embeddings"), []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := s.Search(context.Background(), Query{Mode: "semantic", Text: "orchestral performer"})
	if err != nil || result.Mode != "semantic" || result.Matches[0].ID != memory.ID || len(result.Diagnostics) == 0 {
		t.Fatal("cache issue blocked useful work", result, err)
	}
	status, err = s.IndexEmbeddings(context.Background())
	if err != nil || !status.Incomplete {
		t.Fatal("claimed blocked cache was ready", status, err)
	}
	_ = os.Remove(filepath.Join(s.path, ".index", "embeddings"))
	status, err = s.IndexEmbeddings(context.Background())
	if err != nil || status.Incomplete {
		t.Fatal("cache did not recover", status, err)
	}
}

func TestEmbeddingFailuresAreIsolatedAndLexicalFallbackIsExplicit(t *testing.T) {
	f := embeddingService(t)
	s, err := Open(t.TempDir(), EmbeddingConfig{BaseURL: f.server.URL})
	if err != nil {
		t.Fatal(err)
	}
	good := createEmbeddedRecord(t, s, Memories, map[string]any{"user": "ari"}, "A violoncello performance.")
	createEmbeddedRecord(t, s, Memories, map[string]any{"user": "ari"}, "REJECT_THIS record.")
	f.mu.Lock()
	f.status = func(text string) int {
		if strings.Contains(text, "REJECT_THIS") {
			return 400
		}
		return 200
	}
	f.mu.Unlock()
	status, err := s.IndexEmbeddings(context.Background())
	if err != nil || !status.Incomplete || status.Indexed != 1 || status.Records != 2 {
		t.Fatal(status, err)
	}
	result, err := s.Search(context.Background(), Query{Mode: "semantic", Text: "orchestral performer"})
	if err != nil || result.Mode != "semantic" || !result.Incomplete || result.Matches[0].ID != good.ID {
		t.Fatal("one bad record disabled healthy result", result, err)
	}
	f.mu.Lock()
	f.status = func(string) int { return 503 }
	f.mu.Unlock()
	before := f.count()
	createEmbeddedRecord(t, s, Memories, map[string]any{"user": "ari"}, "Another memory remains writable.")
	if f.count() != before {
		t.Fatal("save depended on embedding service")
	}
	result, err = s.Search(context.Background(), Query{Mode: "semantic", Text: "violoncello"})
	if err != nil || result.Mode != "lexical" || len(result.Diagnostics) == 0 || result.Matches[0].ID != good.ID {
		t.Fatal("missing explicit useful fallback", result, err)
	}
	if _, err = s.Read(good.ID); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.status = nil
	f.mu.Unlock()
	status, err = s.IndexEmbeddings(context.Background())
	if err != nil || status.Incomplete || status.Indexed != 3 {
		t.Fatal("endpoint recovery did not resume indexing", status, err)
	}
}

func TestLongEmbeddingInputsAndConfigurationNormalization(t *testing.T) {
	var cfg EmbeddingConfig
	if err := yaml.Unmarshal([]byte(`{"model":3,"base_url":"http://example.test/v1/","chunk_size":"20"}`), &cfg); err != nil || cfg.Model != "3" || cfg.ChunkSize != 20 {
		t.Fatal(cfg, err)
	}
	f := embeddingService(t)
	cfg = EmbeddingConfig{BaseURL: f.server.URL, ChunkSize: 20}
	s, err := Open(t.TempDir(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Repeat("α😀 calm ", 100) + "violoncello"
	createEmbeddedRecord(t, s, Memories, map[string]any{"user": "ari"}, body)
	status, err := s.IndexEmbeddings(context.Background())
	if err != nil || status.Incomplete || status.Chunks < 2 {
		t.Fatal(status, err)
	}
	f.mu.Lock()
	joined := ""
	for _, r := range f.requests {
		if len([]rune(r.Input)) > 20 {
			t.Fatal("input exceeds configured size")
		}
		joined += r.Input
	}
	f.mu.Unlock()
	if !strings.Contains(joined, "violoncello") {
		t.Fatal("tail silently truncated")
	}
	result, err := s.Search(context.Background(), Query{Mode: "semantic", Text: "orchestral"})
	if err != nil || result.Mode != "semantic" || result.Matches[0].Score < 0.99 {
		t.Fatal("tail cannot be retrieved", result, err)
	}
}

func TestEmbeddingNumericStringsAndEmptySelection(t *testing.T) {
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"embedding":["3",4,"0"]}]}`))
	}))
	defer service.Close()
	s, err := Open(t.TempDir(), EmbeddingConfig{BaseURL: service.URL})
	if err != nil {
		t.Fatal(err)
	}
	createEmbeddedRecord(t, s, Memories, map[string]any{"user": "ari"}, "A remembered evening.")
	status, err := s.IndexEmbeddings(context.Background())
	if err != nil || status.Incomplete || status.Dimensions != 3 {
		t.Fatal("numeric strings were rejected", status, err)
	}
	result, err := s.Search(context.Background(), Query{Mode: "semantic", Text: "an evening", User: "ari"})
	if err != nil || result.Mode != "semantic" || result.Matches[0].Score < 0.99 {
		t.Fatal(result, err)
	}
	// No selected records needs no provider, and is an ordinary empty result.
	offline, err := Open(s.path, EmbeddingConfig{BaseURL: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	result, err = offline.Search(context.Background(), Query{Mode: "semantic", Text: "anything", User: "nobody"})
	if err != nil || result.Mode != "semantic" || len(result.Diagnostics) > 0 || len(result.Matches) != 0 {
		t.Fatal("empty selection treated as failure", result, err)
	}
}

func TestEmbeddingCancellationLeavesMemoriesReadable(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-release }))
	defer service.Close()
	defer close(release)
	s, err := Open(t.TempDir(), EmbeddingConfig{BaseURL: service.URL})
	if err != nil {
		t.Fatal(err)
	}
	memory := createEmbeddedRecord(t, s, Memories, map[string]any{"user": "ari"}, "An intact account.")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := s.Search(ctx, Query{Mode: "semantic", Text: "some account"}); done <- err }()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("embedding cancellation did not stop the request")
	}
	doc, err := s.Read(memory.ID)
	if err != nil || doc.Body != memory.Body {
		t.Fatal("cancellation damaged memory", err)
	}
}

func TestConcurrentEmbeddingRefreshKeepsCanonicalRecords(t *testing.T) {
	f := embeddingService(t)
	s, err := Open(t.TempDir(), EmbeddingConfig{BaseURL: f.server.URL})
	if err != nil {
		t.Fatal(err)
	}
	memory := createEmbeddedRecord(t, s, Memories, map[string]any{"user": "ari"}, "A violoncello performance.")
	errors := make(chan error, 8)
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			result, err := s.Search(context.Background(), Query{Mode: "semantic", Text: "orchestral performer"})
			if err == nil && (result.Mode != "semantic" || len(result.Matches) != 1 || result.Matches[0].ID != memory.ID) {
				err = fmt.Errorf("healthy result missing: %+v", result)
			}
			errors <- err
		})
	}
	workers.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	status, err := s.IndexEmbeddings(context.Background())
	if err != nil || status.Incomplete {
		t.Fatal("concurrent cache did not settle", status, err)
	}
	doc, err := s.Read(memory.ID)
	if err != nil || doc.Body != memory.Body {
		t.Fatal("concurrent refresh changed memory", err)
	}
}
