package web

import (
	"bytes"
	"context"
	"encoding/json"
	"mime"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aristath/songya"
)

func TestFileHTTPStreamsRangesAndDoesNotExecuteActiveContent(t *testing.T) {
	store, _ := songya.Open(t.TempDir())
	doc, _ := store.Create(songya.Memories, map[string]any{"user": "john", "title": "Files"}, "Keep these words.")
	app := Handler(store)
	defer app.Close()
	payload := append([]byte("RIFF\x00\x00\x00\x00WAVEfmt "), bytes.Repeat([]byte{0, 1, 2, 3}, 1024*1024)...)
	request := httptest.NewRequest("PUT", "/api/uploads/music?scope=test&filename=音.wav", bytes.NewReader(payload))
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	var upload songya.Upload
	if err := json.Unmarshal(response.Body.Bytes(), &upload); err != nil {
		t.Fatal(err)
	}
	code, result := call(t, app, "POST", "/api/records/"+doc.ID+"/files", songya.FileChange{Key: "audio", Revision: doc.Revision, Action: "add", Upload: upload.ID})
	if code != 200 || result["status"] != "committed" {
		t.Fatal(code, result)
	}
	request = httptest.NewRequest("GET", "/api/records/"+doc.ID+"/files/音.wav?preview=1", nil)
	request.Header.Set("Range", "bytes=100-199")
	response = httptest.NewRecorder()
	app.ServeHTTP(response, request)
	if response.Code != 206 || !bytes.Equal(response.Body.Bytes(), payload[100:200]) || response.Header().Get("Content-Range") == "" {
		t.Fatal("media seeking", response.Code, response.Header())
	}
	request = httptest.NewRequest("GET", "/api/records/"+doc.ID+"/files/音.wav", nil)
	response = httptest.NewRecorder()
	app.ServeHTTP(response, request)
	_, params, err := mime.ParseMediaType(response.Header().Get("Content-Disposition"))
	if err != nil || params["filename"] != "音.wav" || !bytes.Equal(response.Body.Bytes(), payload) {
		t.Fatal("original download changed", err)
	}
	// An uploaded script is served only as a download, even if preview is requested.
	upload, err = store.Upload(context.Background(), "html", "test", "example.html", strings.NewReader("<!doctype html><script>throw 'executed'</script>"))
	if err != nil {
		t.Fatal(err)
	}
	doc, _ = store.Read(doc.ID)
	_, err = store.ChangeFile(context.Background(), songya.FileChange{Key: "html", ID: doc.ID, Revision: doc.Revision, Action: "add", Upload: upload.ID})
	if err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest("GET", "/api/records/"+doc.ID+"/files/example.html?preview=1", nil))
	if !strings.HasPrefix(response.Header().Get("Content-Disposition"), "attachment") || response.Header().Get("Content-Type") != "application/octet-stream" || !strings.Contains(response.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Fatal(response.Header())
	}
	read, _ := store.Read(doc.ID)
	if read.Body != "Keep these words." {
		t.Fatal("files rewrote text")
	}
}
func TestChatUploadsAreBoundToTurnWithoutChangingOriginalWords(t *testing.T) {
	store, _ := songya.Open(t.TempDir())
	app := Handler(store)
	defer app.Close()
	upload, err := store.Upload(context.Background(), "chat-upload", "chat:john", "note.txt", strings.NewReader("A note"))
	if err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"id": strings.Repeat("a", 32), "request_id": "one", "user": "john", "text": "Keep this file.", "uploads": []string{upload.ID}}
	code, result := call(t, app, "POST", "/api/chat", input)
	if code != 202 {
		t.Fatal(code, result)
	}
	c, err := app.chat.conversation(input["id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Messages) != 1 || len(c.Messages[0].Uploads) != 1 || c.Messages[0].Content != "Keep this file." || originalTurnText(c) != "Keep this file." {
		t.Fatal(c)
	}
	llm, _ := json.Marshal(messageParam(c.Messages[0]))
	if !bytes.Contains(llm, []byte(upload.ID)) || !bytes.Contains(llm, []byte("not yet attached")) {
		t.Fatal(string(llm))
	}
	// Reusing the same request ID with another file must not change accepted work.
	input["uploads"] = []string{}
	code, _ = call(t, app, "POST", "/api/chat", input)
	if code != 409 {
		t.Fatal("different file list reused accepted request", code)
	}
	ready, _ := store.Uploads(context.Background(), "chat:john")
	if len(ready) != 1 {
		t.Fatal("failed downstream save lost upload")
	}
	// Another selected user's turn cannot claim these receipts.
	input["id"] = strings.Repeat("b", 32)
	input["user"] = "mary"
	input["uploads"] = []string{upload.ID}
	code, _ = call(t, app, "POST", "/api/chat", input)
	if code == 202 {
		t.Fatal("cross-user upload accepted")
	}
}
