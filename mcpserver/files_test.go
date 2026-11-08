package mcpserver_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aristath/songya"
)

func TestMCPManagesActualFilesAndBoundedTextWithoutBinaryArguments(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	path := t.TempDir()
	store, _ := songya.Open(path)
	doc, err := store.Create(songya.Memories, map[string]any{"user": "john", "title": "John's document"}, "The original account.")
	if err != nil {
		t.Fatal(err)
	}
	upload, err := store.Upload(ctx, "mcp-upload", "test", "note.txt", strings.NewReader(strings.Repeat("A useful note.\n", 2000)))
	if err != nil {
		t.Fatal(err)
	}
	session := connect(t, ctx, binary(t), path, "")
	listed := keyed(t, ctx, session, "list_files", "list", map[string]any{"id": doc.ID})
	args := map[string]any{"id": doc.ID, "revision": listed["revision"], "upload": upload.ID}
	attached := keyed(t, ctx, session, "attach_file", "attach", args)
	if attached["status"] != "committed" || attached["filename"] != "note.txt" {
		t.Fatal(attached)
	}
	status := keyed(t, ctx, session, "file_status", "status", map[string]any{"key": "attach"})
	if status["status"] != "committed" || status["id"] != doc.ID {
		t.Fatal(status)
	}
	again := keyed(t, ctx, session, "attach_file", "attach", args)
	if again["revision"] != attached["revision"] {
		t.Fatal("retry changed accepted write", again)
	}
	page := keyed(t, ctx, session, "read_file", "read", map[string]any{"id": doc.ID, "filename": "note.txt"})
	if text, _ := page["text"].(string); len(text) > 16384 || !strings.HasPrefix(text, "A useful note.") {
		t.Fatal(page)
	}
	if page["next"].(float64) <= 0 {
		t.Fatal("no continuation", page)
	}
	listed = keyed(t, ctx, session, "list_files", "list2", map[string]any{"id": doc.ID})
	removed := keyed(t, ctx, session, "remove_file", "remove", map[string]any{"id": doc.ID, "revision": listed["revision"], "filename": "note.txt"})
	if removed["status"] != "committed" {
		t.Fatal(removed)
	}
	read, _ := store.Read(doc.ID)
	if read.Body != "The original account." {
		t.Fatal("tool rewrote prose")
	}
}
