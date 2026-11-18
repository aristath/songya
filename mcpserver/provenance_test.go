package mcpserver_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/aristath/songya"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPOriginalWordsAndEntitySpecificFeedback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	root := t.TempDir()
	session := connect(t, ctx, binary(t), root, "")
	invoke := func(name, key string, args any) *mcp.CallToolResult {
		bindStore(args)
		r, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args, Meta: mcp.Meta{"songya/request-key": key, "songya/original-text": "  My exact words.\n"}})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	read := func(r *mcp.CallToolResult) map[string]any {
		if r.IsError {
			t.Fatal(r)
		}
		encoded, _ := json.Marshal(r.StructuredContent)
		var envelope map[string]any
		if err := json.Unmarshal(encoded, &envelope); err != nil {
			t.Fatal(err)
		}
		return payload(t, envelope)
	}
	entity := read(invoke("save_entity", "new", map[string]any{"create": true, "name": "Semina", "description": "Old description."}))
	id := entity["id"].(string)
	noop := read(invoke("save_entity", "noop", map[string]any{"id": id, "name": "Semina"}))
	feedback := fmt.Sprint(noop["diagnostics"])
	if !strings.Contains(feedback, "send description") || strings.Contains(feedback, "send text") {
		t.Fatal(feedback)
	}
	updated := read(invoke("save_entity", "correct", map[string]any{"id": id, "description": "Correct description."}))
	fields, _ := json.Marshal(updated["changed_fields"])
	if string(fields) != `["description"]` {
		t.Fatal(updated)
	}
	result := invoke("save_memory", "memory", map[string]any{"user": "ari", "text": "Model-written memory."})
	if result.IsError {
		t.Fatal(result)
	}
	store, _ := songya.Open(root)
	account, err := store.BatchData(ctx, "memory", "", "account")
	if err != nil || string(account) != "  My exact words.\n" {
		t.Fatalf("wrong source: %q %v", account, err)
	}
	blocked := invoke("save_memory", "forged", map[string]any{"user": "ari", "text": "Some prose", "metadata": map[string]any{"original_accounts": []string{"Model-written fake quote"}}})
	if !blocked.IsError {
		t.Fatal("model could replace original accounts")
	}
}

func TestMCPExplicitSameNameCreationAndDateOnlyValues(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	session := connect(t, ctx, binary(t), t.TempDir(), "")
	first := keyed(t, ctx, session, "save_entity", "cousin", map[string]any{"create": true, "name": "Alex", "description": "Sam's cousin."})
	second := keyed(t, ctx, session, "save_entity", "coworker", map[string]any{"create": true, "name": "Alex", "description": "Sam's coworker, a different person."})
	if first["id"] == second["id"] {
		t.Fatal("MCP creation reused a matching name", first, second)
	}
	_, unsafe := invoke(t, ctx, session, "save_entity", map[string]any{"name": "Alex", "description": "An unsafe name-only replacement"})
	if !unsafe.IsError {
		t.Fatal("name-only update was allowed")
	}
	read, _ := invoke(t, ctx, session, "read_memory", map[string]any{"id": first["id"]})
	if payload(t, read)["text"] != "Sam's cousin." {
		t.Fatal("the first person was overwritten", read)
	}
	memory := keyed(t, ctx, session, "save_memory", "ongoing", map[string]any{"user": "sam", "title": "Tea preference", "text": "Since July 2025, Sam prefers tea.", "time": map[string]any{"date": nil, "earliest": "2025-07-01", "latest": nil}})
	timing := memory["time"].(map[string]any)
	if timing["earliest"] != "2025-07-01" || timing["latest"] != nil {
		t.Fatal("ongoing end was invented", memory)
	}
	for _, value := range []any{"ongoing", "2025-02-29", "2025-07-01T00:00:00Z"} {
		_, bad := invoke(t, ctx, session, "save_memory", map[string]any{"id": memory["id"], "time": map[string]any{"latest": value}})
		if !bad.IsError {
			t.Fatal("MCP accepted a non-date end", value, bad)
		}
	}
}

func TestDocumentedCorrectionPreservesHeaderFieldsAndQuotedBody(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	markdown, err := os.ReadFile(filepath.Join("..", "web", "chat-system.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, example, found := strings.Cut(string(markdown), "# Worked example: find, correct, and check\n")
	if !found {
		t.Fatal("missing worked example")
	}
	example, _, _ = strings.Cut(example, "# Use tools and check the work")
	arguments := regexp.MustCompile("(?s)```json\\n(.*?)\\n```").FindAllStringSubmatch(example, -1)
	sources := regexp.MustCompile("(?s)```markdown\\n(.*?)\\n```").FindAllStringSubmatch(example, -1)
	if len(arguments) != 3 || len(sources) != 1 {
		t.Fatal("worked example needs find, read, save arguments and one complete source")
	}
	root := t.TempDir()
	store, _ := songya.Open(root)
	breakup, err := store.Create(songya.Memories, map[string]any{"user": "example_user", "title": "Breakup"}, "Sam and Jamie broke up.")
	if err != nil {
		t.Fatal(err)
	}
	source := strings.ReplaceAll(sources[0][1], "BREAKUP_ID", breakup.ID)
	seed, err := store.SaveSource(ctx, "seed-example", "", songya.Memories, "", source)
	if err != nil {
		t.Fatal(err)
	}
	id := seed.Refs["record"]
	before, err := store.Read(id)
	if err != nil {
		t.Fatal(err)
	}
	session := connect(t, ctx, binary(t), root, "")
	for i, toolName := range []string{"prepare_memory", "read_memory", "save_memory"} {
		var args map[string]any
		if err := json.Unmarshal([]byte(strings.ReplaceAll(arguments[i][1], "MOVE_ID", id)), &args); err != nil {
			t.Fatal("invalid documented JSON", toolName, err)
		}
		result := keyed(t, ctx, session, toolName, fmt.Sprintf("example-%d", i), args)
		if toolName == "save_memory" && result["outcome"] != "updated" {
			t.Fatal(result)
		}
	}
	after, err := store.Read(id)
	if err != nil {
		t.Fatal(err)
	}
	old, updated := before.Metadata(), after.Metadata()
	if updated.Title != old.Title || len(updated.Relations) != 1 || updated.Relations[0] != old.Relations[0] {
		t.Fatal("documented call changed owner, title, or link", updated)
	}
	timing := after.Record(songya.Memories).Time
	if timing.Date == nil || *timing.Date != "2019-12-01" || timing.Earliest != nil || timing.Latest != nil {
		t.Fatal("documented call did not replace bounds with the exact date", timing)
	}
	if strings.TrimSpace(after.Body) != strings.Replace(strings.TrimSpace(before.Body), "In 2020", "On December 1, 2019", 1) {
		t.Fatal("documented call copied header into the body or changed unrelated wording", after.Body)
	}
}
