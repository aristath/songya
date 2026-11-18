package web

import (
	"bytes"
	"encoding/json"
	"github.com/aristath/songya"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStoreRoutesAuthenticateAndKeepViewsInsideTheirDirectory(t *testing.T) {
	app := HandlerForStores(songya.ApplicationConfiguration{Stores: map[string]string{"first": t.TempDir(), "second": t.TempDir()}, Password: "test-secret"})
	defer app.Close()
	request := func(method, path string, input any, authenticated bool) (int, string) {
		body, _ := json.Marshal(input)
		r := httptest.NewRequest(method, path, bytes.NewReader(body))
		if authenticated {
			r.SetBasicAuth("admin", "test-secret")
		}
		w := httptest.NewRecorder()
		app.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	for _, route := range []string{"/", "/assets/theme.js", "/stores/first/", "/stores/first/api/records"} {
		if code, _ := request("GET", route, nil, false); code != 401 {
			t.Fatal("authentication bypass", route, code)
		}
	}
	code, body := request("POST", "/stores/first/api/records", map[string]any{"key": "save", "kind": "memories", "source": "---\ntitle: Private\n---\nOnly first"}, true)
	if code != 200 {
		t.Fatal(code, body)
	}
	var saved map[string]any
	if err := json.Unmarshal([]byte(body), &saved); err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{"api/records", "api/graph", "api/timeline", "api/chat/sessions"} {
		code, body = request("GET", "/stores/second/"+route, nil, true)
		if code != 200 || strings.Contains(body, "Only first") || strings.Contains(body, saved["id"].(string)) {
			t.Fatal(route, code, body)
		}
	}
	if code, _ := request("GET", "/stores/second/api/records/"+saved["id"].(string), nil, true); code == 200 {
		t.Fatal("cross-store ID read")
	}
	code, body = request("GET", "/stores/first/", nil, true)
	if code != 200 || !strings.Contains(body, `content="first"`) || !strings.Contains(body, `aria-label="Store"`) {
		t.Fatal(code, body)
	}
}
