package mcpserver_test

import (
	"context"
	"github.com/aristath/songya"
	"github.com/aristath/songya/mcpserver"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"testing"
)

func TestConfiguredStoresIsolateReadsWritesTypesAndToolSchemas(t *testing.T) {
	ctx := context.Background()
	first, _ := songya.Open(t.TempDir())
	second, _ := songya.Open(t.TempDir())
	cfg, _ := first.TypeConfiguration(ctx)
	cfg.Kinds[songya.Entities] = append(cfg.Kinds[songya.Entities], songya.TypeDefinition{ID: "ingredient", Name: "Ingredient", Description: "Cooking ingredient"})
	if _, err := first.SaveTypeConfiguration(ctx, cfg, cfg.Version, nil, "settings"); err != nil {
		t.Fatal(err)
	}
	ct, st := mcp.NewInMemoryTransports()
	server, err := mcpserver.New(ctx, map[string]*songya.Store{"first": first, "second": second}, nil).Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, err := mcp.NewClient(&mcp.Implementation{Name: "stores", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	tools, err := client.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		schema := tool.InputSchema.(map[string]any)
		props := schema["properties"].(map[string]any)
		if props["store"] == nil || props["user"] != nil || props["type_user"] != nil {
			t.Fatal(tool.Name, schema)
		}
	}
	saved := keyed(t, ctx, client, "save_entity", "entity", map[string]any{"store": "first", "create": true, "name": "Bulgur", "type": "ingredient", "description": "Cracked wheat"})
	id := saved["id"].(string)
	_, bad := invoke(t, ctx, client, "read_memory", map[string]any{"store": "second", "id": id})
	if !bad.IsError {
		t.Fatal("cross-store read succeeded")
	}
	_, bad = invoke(t, ctx, client, "save_entity", map[string]any{"store": "second", "create": true, "name": "Rice", "type": "ingredient"})
	if !bad.IsError {
		t.Fatal("another store's types accepted")
	}
	missing, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "read_memory", Arguments: map[string]any{"id": id}})
	if err == nil && !missing.IsError {
		t.Fatal("store selection was optional")
	}
	doc, _ := first.Read(id)
	if doc.Record(songya.Entities).Type != "ingredient" || doc.Body != "Cracked wheat" {
		t.Fatal(doc)
	}
}
