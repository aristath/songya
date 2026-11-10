package mcpserver_test

import (
	"bytes"
	"context"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
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

func TestMCPEntityPhotosReturnPixelsAndPortableOriginalResources(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	store, _ := songya.Open(t.TempDir())
	doc, err := store.Create(songya.Entities, map[string]any{"name": "John"}, "An identification.")
	if err != nil {
		t.Fatal(err)
	}
	var pixels bytes.Buffer
	picture := image.NewRGBA(image.Rect(0, 0, 48, 24))
	picture.Set(5, 5, color.RGBA{R: 255, A: 255})
	if err := png.Encode(&pixels, picture); err != nil {
		t.Fatal(err)
	}
	name := "John's photo #1 ?.png"
	upload, err := store.Upload(ctx, "photo", "test", name, bytes.NewReader(pixels.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.ChangeFile(ctx, songya.FileChange{Key: "photo-attach", ID: doc.ID, Revision: doc.Revision, Action: "add", Upload: upload.ID})
	if err != nil {
		t.Fatal(err)
	}
	session := connect(t, ctx, binary(t), store.Directory(), "")
	read := keyed(t, ctx, session, "read_memory", "read-entity", map[string]any{"id": doc.ID, "view": "body"})
	if read["attachment_count"].(float64) != 1 {
		t.Fatal("Entity read hid its attachment", read)
	}
	listed := keyed(t, ctx, session, "list_files", "list-photo", map[string]any{"id": doc.ID})
	ref := listed["files"].([]any)[0].(map[string]any)
	uri := ref["uri"].(string)
	if !strings.Contains(uri, "%23") || !strings.Contains(uri, "%3F") {
		t.Fatal("Filename was not URI-escaped", uri)
	}
	_, result := invoke(t, ctx, session, "read_file", map[string]any{"id": doc.ID, "filename": name})
	if result.IsError {
		t.Fatal(result)
	}
	images := 0
	for _, part := range result.Content {
		if photo, ok := part.(*mcp.ImageContent); ok {
			images++
			if photo.MIMEType != "image/png" || !bytes.Equal(photo.Data, pixels.Bytes()) {
				t.Fatal("Tool did not return actual image bytes")
			}
		}
	}
	if images != 1 {
		t.Fatal("No native MCP image was returned", result)
	}
	resource, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: uri})
	if err != nil {
		t.Fatal(err)
	}
	if len(resource.Contents) != 1 || !bytes.Equal(resource.Contents[0].Blob, pixels.Bytes()) {
		t.Fatal("Original bytes changed")
	}
	// A selected entity portrait can refer to a linked memory's original file.
	memory, err := store.Create(songya.Memories, map[string]any{"user": "john", "title": "A visit"}, "An account.")
	if err != nil {
		t.Fatal(err)
	}
	borrowed, err := store.Upload(ctx, "borrowed", "test", "memory-photo.png", bytes.NewReader(pixels.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ChangeFile(ctx, songya.FileChange{Key: "memory-photo", ID: memory.ID, Revision: memory.Revision, Action: "add", Upload: borrowed.ID}); err != nil {
		t.Fatal(err)
	}
	current, err := store.Read(doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(doc.ID, current.Revision, map[string]any{"profile_picture": map[string]any{"record": memory.ID, "filename": "memory-photo.png", "x": .5, "y": .5, "zoom": 1}}, current.Body); err != nil {
		t.Fatal(err)
	}
	entityRead := keyed(t, ctx, session, "read_memory", "read-profile", map[string]any{"id": doc.ID, "view": "body"})
	profile := entityRead["profile_picture"].(map[string]any)
	if profile["record"] != memory.ID || profile["filename"] != "memory-photo.png" {
		t.Fatal("Borrowed profile ownership was hidden", profile)
	}
	_, borrowedResult := invoke(t, ctx, session, "read_file", map[string]any{"id": profile["record"], "filename": profile["filename"]})
	if borrowedResult.IsError {
		t.Fatal("Borrowed profile could not be read")
	}
	for _, bad := range []string{"record.md", "../record.md", "/etc/passwd"} {
		_, reply := invoke(t, ctx, session, "read_file", map[string]any{"id": doc.ID, "filename": bad})
		if !reply.IsError {
			t.Fatal("Read exposed a non-attachment", bad)
		}
	}
}

func TestMCPBinaryResourcesPageLargeFilesAndFailedImagesRemainAvailable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	store, _ := songya.Open(t.TempDir())
	doc, _ := store.Create(songya.Entities, map[string]any{"name": "Mary"}, "Keep these words.")
	// Sparse binary fixture avoids retaining a 33 MiB upload in the test process.
	path := filepath.Join(store.Directory(), "entities", doc.ID, "large.bin")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	total := int64(33<<20) + 17
	if err := file.Truncate(total); err != nil {
		t.Fatal(err)
	}
	file.Close()
	upload, _ := store.Upload(ctx, "broken", "test", "broken.heic", strings.NewReader("not an image"+strings.Repeat("\x00", 1024)))
	_, err = store.ChangeFile(ctx, songya.FileChange{Key: "broken-photo", ID: doc.ID, Revision: doc.Revision, Action: "add", Upload: upload.ID})
	if err != nil {
		t.Fatal(err)
	}
	session := connect(t, ctx, binary(t), store.Directory(), "")
	listing := keyed(t, ctx, session, "list_files", "binary-list", map[string]any{"id": doc.ID})
	var large map[string]any
	for _, item := range listing["files"].([]any) {
		ref := item.(map[string]any)
		if ref["filename"] == "large.bin" {
			large = ref
		}
	}
	if large == nil || large["pages_uri"] == nil {
		t.Fatal("No paged original resource", listing)
	}
	if _, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: large["uri"].(string)}); err == nil {
		t.Fatal("Large original was read without a bound")
	}
	page, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: large["pages_uri"].(string)})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Contents[0].Blob) != 1<<20 || page.Contents[0].Meta["next_uri"] == "" {
		t.Fatal("Binary page missing bytes or continuation")
	}
	lastURI := large["uri"].(string) + "/pages/" + strconv.FormatInt(total-17, 10)
	last, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: lastURI})
	if err != nil {
		t.Fatal(err)
	}
	if len(last.Contents[0].Blob) != 17 || last.Contents[0].Meta["next_uri"] != "" {
		t.Fatal("Final binary page did not terminate")
	}
	// Preview failure cannot hide or alter an intact original file.
	_, result := invoke(t, ctx, session, "read_file", map[string]any{"id": doc.ID, "filename": "broken.heic"})
	if !result.IsError {
		t.Fatal("Broken photo was presented as readable")
	}
	uri := ""
	for _, part := range result.Content {
		if link, ok := part.(*mcp.ResourceLink); ok {
			uri = link.URI
		}
	}
	if uri == "" {
		t.Fatal("Preview failure lost its original reference")
	}
	original, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: uri})
	if err != nil || len(original.Contents[0].Blob) == 0 {
		t.Fatal("Preview failure blocked original bytes", err)
	}
}
