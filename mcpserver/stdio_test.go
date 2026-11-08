package mcpserver_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aristath/songya"
	"github.com/aristath/songya/web"
	"github.com/gofrs/flock"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestStdioAndWebShareStore(t *testing.T) {
	path := t.TempDir()
	store, err := songya.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	handler := web.Handler(store)
	input, _ := json.Marshal(map[string]any{"key": "web-create", "kind": "memories", "source": "---\nuser: ari\ntitle: Shared store\ncustom: {nested: [3, false]}\n---\nCreated in the browser"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("POST", "/api/records", bytes.NewReader(input)))
	var saved map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &saved); err != nil || response.Code != 200 {
		t.Fatalf("web create: %d %s %v", response.Code, response.Body.String(), err)
	}
	id := saved["id"].(string)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	session := connect(t, ctx, binary(t), path, "")
	read, result := invoke(t, ctx, session, "read_memory", map[string]any{"id": id, "view": "source"})
	if result.IsError || !strings.Contains(payload(t, read)["text"].(string), "Created in the browser") {
		t.Fatal("MCP cannot read the web write", read)
	}
	written := keyed(t, ctx, session, "save_memory", "mcp-update", map[string]any{"id": id, "user": "ari", "text": "Updated through MCP"})
	if written["status"] != "committed" {
		t.Fatal(written)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/api/records/"+id, nil))
	var opened map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &opened); err != nil || response.Code != 200 {
		t.Fatalf("web read: %d %s %v", response.Code, response.Body.String(), err)
	}
	source := opened["source"].(string)
	if !strings.Contains(source, "Updated through MCP") || !strings.Contains(source, "nested:") || opened["version"] == saved["version"] {
		t.Fatal("web did not observe the MCP edit or metadata was lost", opened)
	}
}

func binary(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "songya-mcp")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "build", "-o", path, "../cmd/songya-mcp")
	if data, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, data)
	}
	return path
}
func connect(t *testing.T, ctx context.Context, bin, store, source string, extra ...string) *mcp.ClientSession {
	t.Helper()
	arguments := append([]string{"--store", store}, extra...)
	if source != "" {
		arguments = append(arguments, "--import-root", "notes="+source)
	}
	command := exec.CommandContext(ctx, bin, arguments...)
	command.Stderr = os.Stderr
	client := mcp.NewClient(&mcp.Implementation{Name: "acceptance-client", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}
func invoke(t *testing.T, ctx context.Context, session *mcp.ClientSession, name string, args any) (map[string]any, *mcp.CallToolResult) {
	t.Helper()
	if input, ok := args.(map[string]any); ok && (name == "save_memory" || name == "import_memories") {
		if _, supplied := input["user"]; !supplied {
			input["user"] = "test"
		}
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope, result
}
func payload(t *testing.T, envelope map[string]any) map[string]any {
	t.Helper()
	data, ok := envelope["data"].(map[string]any)
	if !ok {
		t.Fatalf("missing structured result: %+v", envelope)
	}
	return data
}

func TestStdioResourcesAndErrorsStayWithinBudget(t *testing.T) {
	bin := binary(t)
	path := t.TempDir()
	if _, err := songya.Open(path); err != nil {
		t.Fatal(err)
	}
	id := "m_000000000000"
	var raw strings.Builder
	raw.WriteString("---\ntitle: Source\ncreated_at: 2000-01-01\nupdated_at: 2000-01-01\n")
	for i := 0; i < 4; i++ {
		key := strconv.Quote(strings.Repeat("\x00", 900) + strconv.Itoa(i))
		raw.WriteString(key + ": one\n" + key + ": two\n")
	}
	raw.WriteString("---\nAccount")
	if err := fixtureWriteFile(filepath.Join(path, "memories", id+"/record.md"), []byte(raw.String()), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	session := connect(t, ctx, bin, path, "")
	resource, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: "memory://records/" + id + "/0"})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(resource)
	if err != nil || len(encoded) > 32*1024 {
		t.Fatalf("resource exceeded budget: %d %v", len(encoded), err)
	}
	_, err = session.ReadResource(ctx, &mcp.ReadResourceParams{URI: "memory://records/" + strings.Repeat("a", 100000) + "/0"})
	if err == nil || len(err.Error()) > 1500 {
		t.Fatalf("resource error was not bounded: %v", err)
	}
}

func TestStdioRetainedSingleCreationRemainsSearchableAndReadable(t *testing.T) {
	bin := binary(t)
	path := t.TempDir()
	if _, err := songya.Open(path); err != nil {
		t.Fatal(err)
	}
	id := "m_000000000000"
	pending := filepath.Join(path, "memories", id+"/.record.md.pending")
	if err := os.MkdirAll(pending, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(path, "memories", id+"/record.md"), 0700); err != nil {
		t.Fatal(err)
	}
	raw := []byte("---\ntitle: Retained\ncreated_at: 2000-01-01\nupdated_at: 2000-01-01\n---\nRetained account")
	checksum := fmt.Sprintf("%x", sha256.Sum256(raw))
	if err := fixtureWriteFile(filepath.Join(pending, checksum+".tmp"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	session := connect(t, ctx, bin, path, "")
	envelope, result := invoke(t, ctx, session, "search_memory", map[string]any{"text": "Retained"})
	if result.IsError {
		t.Fatal(envelope)
	}
	matches := payload(t, envelope)["matches"].([]any)
	if len(matches) != 1 || matches[0].(map[string]any)["pending"] != true {
		t.Fatal("pending body vanished from MCP discovery")
	}
	for _, args := range []map[string]any{{"id": id, "view": "body"}, {"source": "memories/" + id + "/record.md"}} {
		envelope, result = invoke(t, ctx, session, "read_memory", args)
		if result.IsError {
			t.Fatal(envelope)
		}
		page := payload(t, envelope)
		if page["pending"] != true || page["revision"] != nil || !strings.Contains(page["text"].(string), "Retained account") {
			t.Fatalf("pending content claimed a committed revision: %+v", page)
		}
	}
}

func TestStdioImportRequiresUnambiguousReviewedBounds(t *testing.T) {
	bin := binary(t)
	source := t.TempDir()
	if err := fixtureWriteFile(filepath.Join(source, "reviewed.md"), []byte("abcdef"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	session := connect(t, ctx, bin, t.TempDir(), source)
	envelope, result := invoke(t, ctx, session, "import_memories", map[string]any{"action": "preview", "root": "notes"})
	if result.IsError {
		t.Fatal(envelope)
	}
	for _, part := range []map[string]any{{"start": "not a number", "end": 3}, {"start": 0.5, "end": 3}, {"start": 0}} {
		envelope, result = invoke(t, ctx, session, "import_memories", map[string]any{"action": "apply", "root": "notes", "path": "reviewed.md", "parts": []any{part}})
		if !result.IsError {
			t.Fatalf("unreviewed range was guessed: %+v", part)
		}
	}
	envelope, result = invoke(t, ctx, session, "import_memories", map[string]any{"action": "apply", "root": "notes", "path": "reviewed.md", "parts": []any{map[string]any{"start": "0.0", "end": "3.0"}}})
	if result.IsError {
		t.Fatal(envelope)
	}
	id := payload(t, envelope)["parts"].([]any)[0].(map[string]any)["id"]
	envelope, result = invoke(t, ctx, session, "read_memory", map[string]any{"id": id, "view": "body"})
	if result.IsError || payload(t, envelope)["text"] != "abc" {
		t.Fatal("equivalent integral bounds failed")
	}
	envelope, result = invoke(t, ctx, session, "read_memory", map[string]any{"id": id, "view": "body", "offset": strconv.Itoa(int(^uint(0) >> 1))})
	if result.IsError || payload(t, envelope)["offset"] != float64(3) || payload(t, envelope)["truncated"] != false {
		t.Fatal("large offset wrapped to the beginning")
	}
}

func TestStdioUnconfirmedSnapshotIsReadableWithoutACommittedRevision(t *testing.T) {
	bin := binary(t)
	path := t.TempDir()
	if _, err := songya.Open(path); err != nil {
		t.Fatal(err)
	}
	id := "m_000000000000"
	raw := []byte("---\ntitle: Uncertain\ncreated_at: 2000-01-01\nupdated_at: 2000-01-01\n---\nReadable uncertain account")
	if err := fixtureWriteFile(filepath.Join(path, "memories", id+"/record.md"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	pending := filepath.Join(path, "memories", id+"/.record.md.pending")
	if err := os.MkdirAll(pending, 0700); err != nil {
		t.Fatal(err)
	}
	if err := fixtureWriteFile(filepath.Join(pending, "batch"), []byte(strings.Repeat("0", 64)), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	session := connect(t, ctx, bin, path, "")
	envelope, result := invoke(t, ctx, session, "read_memory", map[string]any{"id": id, "view": "body"})
	if result.IsError {
		t.Fatal(envelope)
	}
	page := payload(t, envelope)
	if page["unconfirmed"] != true || page["revision"] != nil || !strings.Contains(fmt.Sprint(page["text"]), "Readable uncertain account") {
		t.Fatalf("coordination damage concealed content or claimed commitment: %+v", page)
	}
}

// Execute the actual agent example, then check stored meaning and concurrent
// enrichment through the real stdio server rather than a separate Go fixture.

func TestStdioCancellationAndEOFDoNotPublishWaitingRequests(t *testing.T) {
	bin := binary(t)
	path := t.TempDir()
	store, err := songya.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	original, err := store.Create(songya.Memories, map[string]any{"user": "test"}, "Keep")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	session := connect(t, ctx, bin, path, "")
	lock := flock.New(filepath.Join(path, ".lock"))
	if err := lock.Lock(); err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	callCtx, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	defer stop()
	_, err = session.CallTool(callCtx, &mcp.CallToolParams{Name: "save_memory", Arguments: map[string]any{"id": original.ID, "text": "Must not publish"}})
	if err == nil {
		t.Fatal("blocked request ignored cancellation")
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if err := lock.Unlock(); err != nil {
		t.Fatal(err)
	}
	reopened, err := songya.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	read, err := reopened.Read(original.ID)
	if err != nil || read.Body != "Keep" {
		t.Fatal("cancelled waiting request published")
	}
}

func TestStdioShutdownCancelsUnacceptedWaitingWrite(t *testing.T) {
	bin := binary(t)
	for _, shutdown := range []string{"signal", "eof"} {
		t.Run(shutdown, func(t *testing.T) {
			path := t.TempDir()
			store, err := songya.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			doc, err := store.Create(songya.Memories, map[string]any{"user": "test"}, "Keep")
			if err != nil {
				t.Fatal(err)
			}
			lock := flock.New(filepath.Join(path, ".lock"))
			if err := lock.Lock(); err != nil {
				t.Fatal(err)
			}
			defer lock.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, bin, "--store", path)
			stdout, err := command.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			stdin, err := command.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			command.Stderr = os.Stderr
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			defer command.Process.Kill()
			client := mcp.NewClient(&mcp.Implementation{Name: "signal-client", Version: "1"}, nil)
			session, err := client.Connect(ctx, &mcp.IOTransport{Reader: stdout, Writer: stdin}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { command.Process.Kill(); session.Close() }()
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				_, _ = session.CallTool(ctx, &mcp.CallToolParams{Name: "save_memory", Arguments: map[string]any{"id": doc.ID, "text": "Must not publish"}})
			}()
			// Allow the real transport to dispatch the request. The held lock prevents any
			// acceptance; this delay does not determine the expected write outcome.
			time.Sleep(100 * time.Millisecond)
			if shutdown == "signal" {
				if err := command.Process.Signal(syscall.SIGTERM); err != nil {
					t.Fatal(err)
				}
			} else if err := stdin.Close(); err != nil {
				t.Fatal(err)
			}
			waited := make(chan error, 1)
			go func() { waited <- command.Wait() }()
			select {
			case err := <-waited:
				if err != nil {
					t.Fatalf("shutdown: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("shutdown left the server waiting on an unaccepted write")
			}
			<-finished
			if err := lock.Unlock(); err != nil {
				t.Fatal(err)
			}
			current, err := store.Read(doc.ID)
			if err != nil || current.Body != "Keep" {
				t.Fatal("shutdown published an unaccepted request")
			}

		})
	}
}
