package mcpserver_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/aristath/songya"
	"github.com/aristath/songya/mcpserver"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestDeleteMemoryUsesReadRevisionReportsLinksAndIsolatesStores(t *testing.T) {
	ctx := context.Background()
	first, _ := songya.Open(t.TempDir())
	second, _ := songya.Open(t.TempDir())
	id, linked := "m_000000000001", "m_000000000002"
	path := func(store *songya.Store, id string) string {
		return filepath.Join(store.Directory(), "memories", id, "record.md")
	}
	for _, store := range []*songya.Store{first, second} {
		if err := fixtureWriteFile(path(store, id), []byte("---\ntitle: Target\n---\nExact account"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := fixtureWriteFile(path(first, linked), []byte("---\ntitle: Linked\nrelations:\n  - type: related_to\n    target: "+id+"\n---\nOther account"), 0600); err != nil {
		t.Fatal(err)
	}
	ct, st := mcp.NewInMemoryTransports()
	server, err := mcpserver.New(ctx, map[string]*songya.Store{"first": first, "second": second}, nil).Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, err := mcp.NewClient(&mcp.Implementation{Name: "deletion", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	read, result := invoke(t, ctx, client, "read_memory", map[string]any{"store": "first", "id": id})
	if result.IsError {
		t.Fatal(read)
	}
	revision := payload(t, read)["revision"]
	blocked, result := invoke(t, ctx, client, "delete_memory", map[string]any{"store": "first", "id": id, "revision": revision})
	if !result.IsError || len(payload(t, blocked)["blocked_by"].([]any)) != 1 || payload(t, blocked)["blocked_by"].([]any)[0] != linked {
		t.Fatal(blocked)
	}
	if err := os.RemoveAll(filepath.Dir(path(first, linked))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path(first, id), []byte("---\ntitle: Target\n---\nChanged account"), 0600); err != nil {
		t.Fatal(err)
	}
	stale, result := invoke(t, ctx, client, "delete_memory", map[string]any{"store": "first", "id": id, "revision": revision})
	if !result.IsError || stale["status"] != "conflict" {
		t.Fatal(stale)
	}
	read, result = invoke(t, ctx, client, "read_memory", map[string]any{"store": "first", "id": id})
	if result.IsError {
		t.Fatal(read)
	}
	args := map[string]any{"store": "first", "id": id, "revision": payload(t, read)["revision"]}
	for range 2 {
		deleted, result := invoke(t, ctx, client, "delete_memory", args)
		if result.IsError || payload(t, deleted)["status"] != "committed" || payload(t, deleted)["outcome"] != "deleted" {
			t.Fatal(deleted)
		}
	}
	if _, err := first.Read(id); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("record survived deletion", err)
	}
	if doc, err := second.Read(id); err != nil || doc.Body != "Exact account" {
		t.Fatal("another store was changed", err)
	}
}
