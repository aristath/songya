package songya

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeRecallPolicy(t *testing.T, s *Store, policy RecallPolicy) {
	t.Helper()
	data, _ := json.Marshal(map[string]any{"recall": policy})
	if err := fixtureWriteFile(filepath.Join(s.path, "config.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
}
func retrieveTest(t *testing.T, s *Store, text string) SemanticRecallResult {
	t.Helper()
	r, err := s.Retrieve(context.Background(), RecallRequest{Message: text, Owners: []string{"ari", "clara"}})
	if err != nil || len(r.Results) != 1 {
		t.Fatal(r, err)
	}
	return r.Results[0]
}
func TestRetrievalAllKindsSearchReturnAndGraphPolicies(t *testing.T) {
	f := embeddingService(t)
	s, _ := Open(t.TempDir(), EmbeddingConfig{BaseURL: f.server.URL})
	entity := createEmbeddedRecord(t, s, Entities, map[string]any{"name": "Alex"}, "A violoncello player")
	memory := createEmbeddedRecord(t, s, Memories, map[string]any{"user": "ari", "emotion": "uncertain tenderness", "time": map[string]any{"date": "2001-06-12"}, "relations": []Relation{{"related_to", entity.ID}}}, "Around college, we talked after rehearsal.")
	topic := createEmbeddedRecord(t, s, Topics, map[string]any{"name": "Orchestra"}, "Orchestral music")
	story := createEmbeddedRecord(t, s, Stories, map[string]any{"user": "ari", "title": "Orchestral rehearsals", "relations": []Relation{{"related_to", memory.ID}}}, "College story introduction")
	excluded := createEmbeddedRecord(t, s, Memories, map[string]any{"user": "bob", "relations": []Relation{{"related_to", entity.ID}}}, "Bob's violoncello account")
	createEmbeddedRecord(t, s, Memories, map[string]any{"user": "clara"}, "A violoncello performance")
	// Cycle plus a second path. An incoming edge must still be traversable.
	_, err := s.Update(entity.ID, entity.Revision, map[string]any{"relations": []Relation{{"related_to", topic.ID}, {"possibly_same_as", story.ID}}}, entity.Body)
	if err != nil {
		t.Fatal(err)
	}
	p := DefaultRecallPolicy()
	p.DirectLimit = 20
	p.ConnectionDepth = 0
	p.MinScore = 0.9
	writeRecallPolicy(t, s, p)
	r := retrieveTest(t, s, "orchestral performer")
	kinds := map[Destination]bool{}
	for _, item := range r.Items {
		kinds[item.Kind] = true
		if item.ID == excluded.ID || item.Reason != "semantic" || item.Score == nil || *item.Score < .9 {
			t.Fatal(item)
		}
	}
	for _, kind := range destinations {
		if !kinds[kind] {
			t.Fatal("record type was not embedded", kind, r)
		}
	}
	count := f.count()
	retrieveTest(t, s, "orchestral performer")
	if f.count() != count {
		t.Fatal("unchanged queries/records re-embedded")
	}
	p.SearchKinds = []Destination{Entities}
	p.ReturnKinds = []Destination{Memories}
	p.DirectLimit = 1
	p.ConnectionDepth = 1
	p.ConnectedLimit = 1
	writeRecallPolicy(t, s, p)
	r = retrieveTest(t, s, "orchestral performer")
	if len(r.Items) != 1 || r.Items[0].ID != memory.ID || r.Items[0].Score != nil || r.Items[0].Reason != "connection" || r.Items[0].Depth != 1 || r.Items[0].OriginID != entity.ID || r.Items[0].Path[0].Type != "related_to" {
		t.Fatal(r)
	}
	for _, text := range []string{"uncertain tenderness", "2001-06-12", "Around college"} {
		if !strings.Contains(r.Items[0].Content, text) {
			t.Fatal("lost substantive content", r)
		}
	}
	p.ConnectionDepth = 0
	writeRecallPolicy(t, s, p)
	if len(retrieveTest(t, s, "orchestral performer").Items) != 0 {
		t.Fatal("depth zero expanded")
	}
	p.ConnectionDepth = 2
	p.ReturnKinds = []Destination{Stories}
	p.RelationTypes = []string{"related_to"}
	writeRecallPolicy(t, s, p)
	r = retrieveTest(t, s, "orchestral performer")
	if len(r.Items) != 1 || r.Items[0].ID != story.ID || r.Items[0].Depth != 2 || len(r.Items[0].Path) != 2 {
		t.Fatal("multi-hop/relation filter", r)
	}
	p.ReturnKinds = []Destination{Entities, Memories, Topics, Stories}
	p.RelationTypes = []string{"related_to", "possibly_same_as"}
	p.ConnectionDepth = 8
	p.ConnectedLimit = 10
	writeRecallPolicy(t, s, p)
	a := retrieveTest(t, s, "orchestral performer")
	b := retrieveTest(t, s, "orchestral performer")
	if !reflect.DeepEqual(a, b) || len(a.Items) != 4 {
		t.Fatal("cycles/duplicates/order", a, b)
	}
	p.ConnectedLimit = 1
	writeRecallPolicy(t, s, p)
	r = retrieveTest(t, s, "orchestral performer")
	if len(r.Items) != 2 || !r.Truncated {
		t.Fatal("connected cap", r)
	}
}

func TestRetrievalPartialFailuresCacheChangesAndWritesContinue(t *testing.T) {
	f := embeddingService(t)
	s, _ := Open(t.TempDir(), EmbeddingConfig{BaseURL: f.server.URL})
	memory := createEmbeddedRecord(t, s, Memories, map[string]any{"user": "ari"}, "A violoncello performance")
	p := DefaultRecallPolicy()
	p.ConnectionDepth = 0
	writeRecallPolicy(t, s, p)
	r := retrieveTest(t, s, "orchestral")
	if len(r.Items) != 1 {
		t.Fatal(r)
	}
	count := f.count()
	doc, _ := s.Read(memory.ID)
	_, err := s.Update(doc.ID, doc.Revision, nil, "A garden of roses.")
	if err != nil {
		t.Fatal(err)
	}
	r = retrieveTest(t, s, "garden")
	if len(r.Items) != 1 || r.Items[0].ID != memory.ID || f.count() != count+2 {
		t.Fatal("content cache", r, f.count(), count)
	}
	files, _ := os.ReadDir(filepath.Join(s.path, ".index", "embeddings"))
	for _, file := range files {
		fixtureWriteFile(filepath.Join(s.path, ".index", "embeddings", file.Name()), []byte("damaged"), 0600)
	}
	r = retrieveTest(t, s, "garden")
	if r.Incomplete || len(r.Items) != 1 {
		t.Fatal("damaged vectors not repaired", r)
	}
	f.mu.Lock()
	f.status = func(string) int { return http.StatusServiceUnavailable }
	f.mu.Unlock()
	r = retrieveTest(t, s, "new uncached query")
	if !r.Incomplete || r.Status != "unavailable" || len(r.Items) != 0 {
		t.Fatal("outage looked empty", r)
	}
	if _, err := s.Create(Memories, map[string]any{"user": "ari"}, "A memory saved while offline"); err != nil {
		t.Fatal(err)
	}
	r = retrieveTest(t, s, "garden")
	if !r.Incomplete || len(r.Items) != 1 {
		t.Fatal("healthy cached work hidden", r)
	}
	f.mu.Lock()
	f.status = nil
	f.mu.Unlock()
	r = retrieveTest(t, s, "garden")
	if r.Incomplete {
		t.Fatal("did not recover", r)
	}
	count = f.count()
	raw, _ := json.Marshal(map[string]any{"recall": p, "embeddings": map[string]any{"base_url": f.server.URL, "model": "different-model"}})
	fixtureWriteFile(filepath.Join(s.path, "config.json"), raw, 0600)
	retrieveTest(t, s, "garden")
	if f.count() <= count {
		t.Fatal("model change did not invalidate vectors")
	}
	f2 := embeddingService(t)
	raw, _ = json.Marshal(map[string]any{"recall": p, "embeddings": map[string]any{"base_url": f2.server.URL, "model": "different-model"}})
	fixtureWriteFile(filepath.Join(s.path, "config.json"), raw, 0600)
	retrieveTest(t, s, "garden")
	if f2.count() == 0 {
		t.Fatal("endpoint change did not invalidate vectors")
	}
}

func TestRecallConfigurationKeepsHealthyFieldsAndHonestDiagnostics(t *testing.T) {
	s, _ := Open(t.TempDir())
	fixtureWriteFile(filepath.Join(s.path, "config.json"), []byte(`{"recall":{"direct_limit":"2","min_score":"broken","connection_depth":"3","relation_types":["related_to","invalid"]},"embeddings":{"model":"fixture"}}`), 0600)
	c, err := s.Configuration()
	if err == nil || c.Recall.DirectLimit != 2 || c.Recall.ConnectionDepth != 3 || c.Recall.MinScore != .42 || c.Embeddings.Model != "fixture" || !reflect.DeepEqual(c.Recall.RelationTypes, []string{"related_to"}) {
		t.Fatal(c, err)
	}
}

func TestRetrievalRetiredTargetsMissingLinksAndContentBounds(t *testing.T) {
	f := embeddingService(t)
	s, _ := Open(t.TempDir(), EmbeddingConfig{BaseURL: f.server.URL})
	old := createEmbeddedRecord(t, s, Entities, map[string]any{"name": "Old musician"}, "A violoncello player")
	other := createEmbeddedRecord(t, s, Entities, map[string]any{"name": "Second musician"}, "A violoncello player")
	memory := createEmbeddedRecord(t, s, Memories, map[string]any{"user": "ari", "relations": []Relation{{"related_to", old.ID}, {"related_to", "e_missing00000"}}}, strings.Repeat("Intact prose. ", 1000))
	receipt, err := s.Merge(context.Background(), MergeRequest{Key: "merge", Sources: []Version{{ID: old.ID, Revision: old.Revision}, {ID: other.ID, Revision: other.Revision}}, Kind: Entities})
	if err != nil {
		t.Fatal(err)
	}
	p := DefaultRecallPolicy()
	p.SearchKinds = []Destination{Entities}
	p.ReturnKinds = []Destination{Entities, Memories}
	p.DirectLimit = 10
	p.ConnectionDepth = 1
	writeRecallPolicy(t, s, p)
	r := retrieveTest(t, s, "orchestral")
	if !r.Incomplete || len(r.Diagnostics) == 0 {
		t.Fatal("missing target not reported", r)
	}
	for _, item := range r.Items {
		if item.ID == old.ID || item.ID == other.ID {
			t.Fatal("retired record returned", receipt, r)
		}
		if item.ID == memory.ID {
			if !item.ContentTruncated || len(item.Content) > 10000 || item.Reason != "connection" {
				t.Fatal(item)
			}
		}
	}
	found := false
	for _, item := range r.Items {
		found = found || item.ID == memory.ID
	}
	if !found {
		t.Fatal("resolved incoming relationship lost", r)
	}
}
