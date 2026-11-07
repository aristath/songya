package web

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aristath/songya"
)

func TestRecordEventsPublishReconnectAndClose(t *testing.T) {
	store, _ := songya.Open(t.TempDir())
	app := Handler(store)
	server := httptest.NewServer(app)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	t.Cleanup(func() { _ = app.Close(); server.Close() })
	connect := func() (*http.Response, *bufio.Scanner) {
		req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/records/events", nil)
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { response.Body.Close() })
		if response.Header.Get("Content-Type") != "text/event-stream" {
			t.Fatal(response.Header)
		}
		return response, bufio.NewScanner(response.Body)
	}
	next := func(scanner *bufio.Scanner) string {
		for scanner.Scan() {
			if line := scanner.Text(); strings.HasPrefix(line, "data: ") {
				return line
			}
		}
		t.Fatal("record stream ended before an update", scanner.Err())
		return ""
	}
	_, stream := connect()
	initial := next(stream)
	code, saved := call(t, app, "POST", "/api/records", map[string]any{"key": "live-create", "kind": "memories", "source": "---\nuser: reader\ntitle: A walk with John\n---\nA quiet afternoon."})
	if code != 200 {
		t.Fatal(code, saved)
	}
	changed := next(stream)
	if changed == initial {
		t.Fatal("save did not invalidate the live record view")
	}
	_, reconnected := connect()
	if next(reconnected) != changed {
		t.Fatal("reconnection did not receive the current view")
	}
	req := httptest.NewRequest("DELETE", "/api/records/"+saved["id"].(string), nil)
	req.Header.Set("If-Match", saved["version"].(string))
	reply := httptest.NewRecorder()
	app.ServeHTTP(reply, req)
	if reply.Code != 200 {
		t.Fatal(reply.Code, reply.Body.String())
	}
	if next(stream) == changed || next(reconnected) == changed {
		t.Fatal("deletion did not reach both live views")
	}
	_ = app.Close()
	for stream.Scan() {
	}
	if stream.Err() != nil {
		t.Fatal("application shutdown did not close the stream cleanly", stream.Err())
	}
}
