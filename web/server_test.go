package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aristath/songya"
)

func call(t *testing.T, h http.Handler, method, path string, input any) (int, map[string]any) {
	t.Helper()
	data, _ := json.Marshal(input)
	request := httptest.NewRequest(method, path, bytes.NewReader(data))
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	var out map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &out); err != nil {
		t.Fatal(response.Body.String(), err)
	}
	return response.Code, out
}
func TestHTTPDocumentEditingAndDeletion(t *testing.T) {
	store, _ := songya.Open(t.TempDir())
	h := Handler(store)
	t.Cleanup(func() { _ = h.Close() })
	for _, kind := range []string{"memories", "entities", "topics", "stories"} {
		input := map[string]any{"key": kind, "kind": kind, "source": "---\nuser: ari\ntitle: Original\ncustom: {nested: [3, false, yes]}\n---\n# Body\n"}
		code, saved := call(t, h, "POST", "/api/records", input)
		if code != 200 {
			t.Fatal(code, saved)
		}
		_, retry := call(t, h, "POST", "/api/records", input)
		if retry["id"] != saved["id"] {
			t.Fatal("duplicate creation")
		}
		code, opened := call(t, h, "GET", "/api/records/"+saved["id"].(string), nil)
		if code != 200 || opened["version"] != saved["version"] {
			t.Fatal(opened)
		}
		_, composed := call(t, h, "POST", "/api/draft", map[string]any{"source": saved["source"], "kind": kind, "fields": []Field{{Key: "user", Value: "ari"}, {Key: "custom", Value: "{nested: [8, false]}"}, {Key: "created_at", Value: "1900-01-01"}}, "body": "Edited"})
		if strings.Contains(composed["source"].(string), "1900-01-01") {
			t.Fatal("visual editor changed automatic field")
		}
		code, changed := call(t, h, "POST", "/api/records", map[string]any{"key": kind + "edit", "kind": kind, "id": saved["id"], "version": saved["version"], "source": composed["source"]})
		if code != 200 {
			t.Fatal(changed)
		}
		if !strings.Contains(changed["source"].(string), "nested") || strings.Contains(changed["source"].(string), "title:") {
			t.Fatal("full metadata replacement failed", changed)
		}
		code, _ = call(t, h, "POST", "/api/records", map[string]any{"key": kind + "stale", "id": saved["id"], "version": saved["version"], "source": "---\nuser: ari\n---\nStale"})
		if code != 409 {
			t.Fatal("stale overwrite")
		}
	}
	target, _ := store.Create(songya.Entities, map[string]any{"name": "Alex"}, "")
	incoming, _ := store.Create(songya.Stories, map[string]any{"user": "ari", "title": "A story", "relations": []songya.Relation{{Type: "related_to", Target: target.ID}}}, "")
	request := httptest.NewRequest("DELETE", "/api/records/"+target.ID, nil)
	request.Header.Set("If-Match", target.Revision)
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != 409 || !strings.Contains(response.Body.String(), incoming.ID) || !strings.Contains(response.Body.String(), "A story") {
		t.Fatal(response.Body.String())
	}
	if _, err := store.Update(incoming.ID, incoming.Revision, map[string]any{"relations": []songya.Relation{}}, ""); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
}

func TestTableProjectsMetadataAndRevisionFromTheCatalog(t *testing.T) {
	store, err := songya.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Create(songya.Entities, map[string]any{"name": "Cedar", "description": "Stale metadata", "types": map[string]string{"ari": "plant"}, "aliases": []string{"Cedrus", "Atlas cedar"}}, "A tree")
	if err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 2; pass++ { // Fresh scan and cached projection.
		code, result := call(t, testApplication(t, store), "GET", "/api/records?classification_user=ari", nil)
		if code != 200 {
			t.Fatal(code, result)
		}
		rows := result["records"].([]any)
		if len(rows) != 1 {
			t.Fatal(result)
		}
		row := rows[0].(map[string]any)
		if row["title"] != "Cedar" || row["description"] != "A tree" || row["type"] != "plant" || row["version"] != record.Revision || row["updated_at"] != record.Metadata().UpdatedAt.UTC().Format(time.RFC3339Nano) || len(row["aliases"].([]any)) != 2 || result["types"].(map[string]any)["plant"] != true {
			t.Fatal("table lost metadata or revision", result)
		}
	}
	description := "A tall conifer."
	if receipt, err := store.SaveNamed(context.Background(), songya.Entities, songya.NamedInput{ID: record.ID, Description: &description}); err != nil || receipt.Status != "committed" {
		t.Fatal(receipt, err)
	}
	code, result := call(t, testApplication(t, store), "GET", "/api/records?q=conifer", nil)
	rows := result["records"].([]any)
	if code != 200 || len(rows) != 1 || rows[0].(map[string]any)["description"] != description {
		t.Fatal("MCP-style description edit did not reach the table/search", result)
	}
}

func TestGraphIncludesAllRecordKindsAndStoredConnections(t *testing.T) {
	root := t.TempDir()
	store, _ := songya.Open(root)
	entity, _ := store.Create(songya.Entities, map[string]any{"name": "Alex", "aliases": []string{"Alexander"}}, "Private body")
	topic, _ := store.Create(songya.Topics, map[string]any{"name": "Travel"}, "")
	memory, err := store.Create(songya.Memories, map[string]any{
		"user": "ari", "title": "A trip",
		"relations": []songya.Relation{{Type: "related_to", Target: entity.ID}, {Type: "related_to", Target: topic.ID}, {Type: "related_to", Target: entity.ID}},
	}, "Private memory")
	if err != nil {
		t.Fatal(err)
	}
	story, _ := store.Create(songya.Stories, map[string]any{"user": "clara", "title": "Journey", "relations": []songya.Relation{{Type: "related_to", Target: memory.ID}}}, "")
	redirect, _ := store.Create(songya.Entities, map[string]any{"name": "Old Alex", "redirects": []string{entity.ID}}, "")
	// A missing target must not stop the rest of the graph from being displayed.
	if err := os.WriteFile(filepath.Join(root, "entities", "e_123456789abc.md"), []byte("---\nname: Lonely\nrelations:\n  - type: related_to\n    target: m_missing\n---\n"), 0600); err != nil {
		t.Fatal(err)
	}
	code, result := call(t, testApplication(t, store), "GET", "/api/graph", nil)
	if code != 200 {
		t.Fatal(code, result)
	}
	nodes := map[string]map[string]any{}
	for _, node := range result["nodes"].([]any) {
		n := node.(map[string]any)
		nodes[n["id"].(string)] = n
		if _, ok := n["body"]; ok {
			t.Fatal("Graph unnecessarily exposes full bodies")
		}
	}
	for id, kind := range map[string]string{entity.ID: "entities", topic.ID: "topics", memory.ID: "memories", story.ID: "stories"} {
		if nodes[id]["kind"] != kind || nodes[id]["title"] == "" {
			t.Fatal("Missing graph identity", id, nodes)
		}
	}
	if aliases := nodes[entity.ID]["aliases"].([]any); len(aliases) != 1 || aliases[0] != "Alexander" {
		t.Fatal("Graph lost searchable aliases", nodes[entity.ID])
	}
	if nodes[memory.ID]["user"] != "ari" || nodes[story.ID]["user"] != "clara" || nodes[entity.ID]["user"] != "" || nodes[topic.ID]["user"] != "" {
		t.Fatal("Graph lost record ownership or assigned an owner to shared identities", nodes)
	}
	want := map[[3]string]bool{
		{memory.ID, "related_to", entity.ID}:          true,
		{memory.ID, "related_to", topic.ID}:           true,
		{story.ID, "related_to", memory.ID}:           true,
		{redirect.ID, "redirects", entity.ID}:         true,
		{"e_123456789abc", "related_to", "m_missing"}: true,
	}
	for _, value := range result["links"].([]any) {
		link := value.(map[string]any)
		key := [3]string{link["source"].(string), link["type"].(string), link["target"].(string)}
		if !want[key] {
			t.Fatal("Unexpected or duplicate graph link", key)
		}
		delete(want, key)
	}
	if len(want) != 0 {
		t.Fatal("Graph lost stored connections", want)
	}
}

func TestBundledAssetsAndCrossSiteWrites(t *testing.T) {
	store, _ := songya.Open(t.TempDir())
	h := Handler(store)
	t.Cleanup(func() { _ = h.Close() })
	for _, path := range []string{"/", "/assets/app.js", "/assets/app.css", "/assets/editors.css"} {
		response := httptest.NewRecorder()
		h.ServeHTTP(response, httptest.NewRequest("GET", path, nil))
		if response.Code != 200 || response.Body.Len() == 0 {
			t.Fatal(path, response.Code)
		}
	}
	request := httptest.NewRequest("POST", "/api/records", strings.NewReader(`{}`))
	request.Header.Set("Origin", "https://another-site.invalid")
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != 403 {
		t.Fatal("foreign origin wrote store")
	}
}

func TestVisualMetadataEditsPreserveAnchorsAndCustomValues(t *testing.T) {
	store, _ := songya.Open(t.TempDir())
	h := Handler(store)
	t.Cleanup(func() { _ = h.Close() })
	original := "---\nuser: ari\ntitle: Original\ndefaults: &defaults\n  nested: [3, false, yes]\nlinked: *defaults\n---\nBody"
	fields := describe(original, songya.Memories).Fields
	for i := range fields {
		if fields[i].Key == "title" {
			fields[i].Value = "Changed"
		}
	}
	code, value := call(t, h, "POST", "/api/draft", map[string]any{"source": original, "fields": fields})
	if code != 200 {
		t.Fatal(value)
	}
	doc := songya.Parse([]byte(value["source"].(string)))
	var data map[string]any
	if err := doc.Frontmatter.Decode(&data); err != nil {
		t.Fatal(err)
	}
	if data["title"] != "Changed" || data["linked"] == nil || data["defaults"] == nil {
		t.Fatal("aliases or fields lost", data)
	}
}

func TestDraftConnectionsUseCurrentSourceIncludingRedirects(t *testing.T) {
	store, _ := songya.Open(t.TempDir())
	target, err := store.Create(songya.Entities, map[string]any{"name": "Target"}, "")
	if err != nil {
		t.Fatal(err)
	}
	h := Handler(store)
	t.Cleanup(func() { _ = h.Close() })
	input := map[string]any{"kind": "entities", "source": "---\nname: Draft\nredirects: [" + target.ID + "]\n---\n"}
	code, draft := call(t, h, "POST", "/api/draft", input)
	if code != 200 {
		t.Fatal(code, draft)
	}
	links := draft["links"].([]any)
	if len(links) != 1 || links[0].(map[string]any)["type"] != "redirects" || links[0].(map[string]any)["target_title"] != "Target" {
		t.Fatal("redirect missing from draft", draft)
	}
	input["source"] = "---\nname: Draft\n---\n"
	code, draft = call(t, h, "POST", "/api/draft", input)
	if code != 200 || len(draft["links"].([]any)) != 0 {
		t.Fatal("removed redirect still in draft", draft)
	}
}

func TestInvalidUTF8CannotBeSilentlyRewrittenThroughTheEditor(t *testing.T) {
	path := t.TempDir()
	store, _ := songya.Open(path)
	id := "m_000000000097"
	original := []byte("---\nuser: ari\n---\nBad byte: ")
	original = append(original, 0xff)
	if err := os.WriteFile(filepath.Join(path, "memories", id+".md"), original, 0600); err != nil {
		t.Fatal(err)
	}
	h := Handler(store)
	t.Cleanup(func() { _ = h.Close() })
	code, _ := call(t, h, "GET", "/api/records/"+id, nil)
	if code != 422 {
		t.Fatal("lossy editor opened damaged encoding")
	}
	after, err := os.ReadFile(filepath.Join(path, "memories", id+".md"))
	if err != nil || !bytes.Equal(after, original) {
		t.Fatal("original bytes changed")
	}
	healthy, err := store.Create(songya.Entities, map[string]any{"name": "Healthy"}, "Readable")
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := call(t, h, "GET", "/api/records/"+healthy.ID, nil); code != 200 {
		t.Fatal("one corrupt record disabled healthy reads")
	}
}

func testApplication(t *testing.T, store *songya.Store) *Application {
	t.Helper()
	app := Handler(store)
	t.Cleanup(func() { _ = app.Close() })
	return app
}
