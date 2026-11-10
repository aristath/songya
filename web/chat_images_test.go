package web

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aristath/songya"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestChatToolPixelsAreDurableImageInputsWithoutBreakingToolPairing(t *testing.T) {
	pixels := []byte("retained image bytes")
	native := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "photo"}, &mcp.ImageContent{Data: pixels, MIMEType: "image/png"}}}
	value, images := chatToolImages(native)
	text, _ := json.Marshal(value)
	if len(images) != 1 || bytes.Contains(text, []byte(base64.StdEncoding.EncodeToString(pixels))) {
		t.Fatal("Pixels remained in tool prose")
	}
	if len(native.Content) != 2 {
		t.Fatal("Extraction modified the original result")
	}
	first, second := chatToolCall{ID: "photo", Type: "function"}, chatToolCall{ID: "text", Type: "function"}
	c := chatConversation{Messages: []chatMessage{
		{Role: "user", Content: "Read John's photo and note."},
		{Role: "assistant", ToolCalls: []chatToolCall{first, second}},
		{Role: "tool", Name: "read_file", ToolCallID: "photo", Content: string(text), Images: images},
		{Role: "tool", Name: "read_file", ToolCallID: "text", Content: "A note."},
	}}
	before, _ := json.Marshal(c)
	data, _ := json.Marshal(chatRequestMessages(c, "Rules"))
	var messages []map[string]any
	if err := json.Unmarshal(data, &messages); err != nil {
		t.Fatal(err)
	}
	if len(messages) != 6 || messages[3]["role"] != "tool" || messages[4]["role"] != "tool" || messages[5]["role"] != "user" {
		t.Fatal("Images split an adjacent tool-result block", string(data))
	}
	parts := messages[5]["content"].([]any)
	imageURL := parts[1].(map[string]any)["image_url"].(map[string]any)["url"]
	if imageURL != "data:image/png;base64,"+base64.StdEncoding.EncodeToString(pixels) {
		t.Fatal("Model did not receive pixels")
	}
	after, _ := json.Marshal(c)
	if !bytes.Equal(before, after) {
		t.Fatal("Projection changed durable history")
	}
}

func TestChatReadsActualEntityImageThroughMCPAndStreamsToVisionProvider(t *testing.T) {
	store, _ := songya.Open(t.TempDir())
	entity, _ := store.Create(songya.Entities, map[string]any{"name": "Mary"}, "Keep the identification.")
	var pixels bytes.Buffer
	_ = png.Encode(&pixels, image.NewRGBA(image.Rect(0, 0, 20, 10)))
	upload, _ := store.Upload(context.Background(), "picture", "test", "photo.png", bytes.NewReader(pixels.Bytes()))
	if _, err := store.ChangeFile(context.Background(), songya.FileChange{Key: "attach", ID: entity.ID, Revision: entity.Revision, Action: "add", Upload: upload.ID}); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if requests.Add(1) == 1 {
			args, _ := json.Marshal(map[string]any{"id": entity.ID, "filename": "photo.png"})
			chatFunction(w, "read-photo", "read_file", string(args))
			return
		}
		messages := body["messages"].([]any)
		last := messages[len(messages)-1].(map[string]any)
		if last["role"] != "user" {
			t.Error("No image observation after the tool result")
			return
		}
		parts, ok := last["content"].([]any)
		if !ok {
			t.Error("Image encoded as text")
			return
		}
		found := false
		for _, p := range parts {
			part := p.(map[string]any)
			if part["type"] == "image_url" {
				found = part["image_url"].(map[string]any)["url"] == "data:image/png;base64,"+base64.StdEncoding.EncodeToString(pixels.Bytes())
			}
		}
		if !found {
			t.Error("Actual pixels did not reach the OpenAI-compatible request")
		}
		previous := messages[len(messages)-2].(map[string]any)
		if previous["role"] != "tool" || strings.Contains(previous["content"].(string), base64.StdEncoding.EncodeToString(pixels.Bytes())) {
			t.Error("Pixels duplicated in tool prose")
		}
		chatChunk(w, map[string]any{"content": "The image is available."}, nil)
		chatFinish(w, "stop")
	}))
	defer provider.Close()
	app := Handler(store)
	defer app.Close()
	configureChat(t, app, provider.URL)
	id := chatID()
	postChat(t, app, id, "inspect-photo", "reader", "Please inspect the saved photo.")
	done := waitChat(t, app, id, "done")
	if requests.Load() != 2 {
		t.Fatal("Unexpected number of provider requests", requests.Load())
	}
	found := false
	for _, message := range done.Messages {
		if message.Role == "tool" && len(message.Images) == 1 {
			found = bytes.Equal(message.Images[0].Data, pixels.Bytes())
		}
	}
	if !found {
		t.Fatal("Image result was not retained with the conversation")
	}
}
