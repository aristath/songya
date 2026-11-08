package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aristath/songya"
)

type chatToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}
type chatMessage struct {
	Uploads    []songya.Upload `json:"uploads,omitempty"`
	Role       string          `json:"role"`
	Content    string          `json:"content"`
	ToolCalls  []chatToolCall  `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	Name       string          `json:"name,omitempty"`
	TurnID     string          `json:"turn_id,omitempty"`
}
type chatQueuedTurn struct {
	Uploads             []songya.Upload       `json:"uploads,omitempty"`
	ID                  string                `json:"id"`
	Text                string                `json:"text"`
	Prompt              string                `json:"prompt,omitempty"`
	Temperature         *float64              `json:"temperature,omitempty"`
	RecallConfiguration *songya.Configuration `json:"recall_configuration,omitempty"`
	RecallConfigIssue   string                `json:"recall_config_issue,omitempty"`
}
type chatConversation struct {
	Recall              *songya.RecallResponse `json:"recall,omitempty"`
	RecallConfiguration *songya.Configuration  `json:"recall_configuration,omitempty"`
	RecallConfigIssue   string                 `json:"recall_config_issue,omitempty"`
	ID                  string                 `json:"id"`
	User                string                 `json:"user"`
	Title               string                 `json:"title"`
	Archived            bool                   `json:"archived,omitempty"`
	Status              string                 `json:"status"`
	Error               string                 `json:"error,omitempty"`
	UpdatedAt           int64                  `json:"updated_at"`
	Revision            int64                  `json:"revision"`
	TurnID              string                 `json:"turn_id"`
	Requests            map[string]string      `json:"requests,omitempty"`
	Prompt              string                 `json:"prompt,omitempty"`
	Temperature         *float64               `json:"temperature,omitempty"`
	Messages            []chatMessage          `json:"messages"`
	Draft               *chatMessage           `json:"draft,omitempty"`
	Queue               []chatQueuedTurn       `json:"queue,omitempty"`
}

// Promote only after the previous reply is complete. Future messages must not
// enter the model's history or original testimony for the current turn.
func (c *chatConversation) advance() {
	if len(c.Queue) == 0 || (c.Status != "" && c.Status != "done") {
		return
	}
	turn := c.Queue[0]
	c.Queue = c.Queue[1:]
	c.TurnID, c.Prompt, c.Temperature = turn.ID, turn.Prompt, turn.Temperature
	c.RecallConfiguration, c.RecallConfigIssue = turn.RecallConfiguration, turn.RecallConfigIssue
	c.Status, c.Error = "pending", ""
	c.Draft, c.Recall = nil, nil
	c.Messages = append(c.Messages, chatMessage{Role: "user", Content: turn.Text, TurnID: turn.ID, Uploads: turn.Uploads})
}

// Receipts identify the exact submitted words, independently of message roles.
func chatFingerprint(user, text string) string {
	hash := sha256.Sum256([]byte(user + "\x00" + text))
	return hex.EncodeToString(hash[:])
}
func chatTurnFingerprint(user, text string, uploads []songya.Upload) string {
	fingerprint := chatFingerprint(user, text)
	if len(uploads) > 0 {
		ids := make([]string, len(uploads))
		for i, upload := range uploads {
			ids[i] = upload.ID
		}
		data, _ := json.Marshal(ids)
		fingerprint = chatFingerprint(fingerprint, string(data))
	}
	return fingerprint
}

type chatApp struct {
	store           *songya.Store
	ctx             context.Context
	cancel          context.CancelFunc
	mu              sync.Mutex
	active          map[string]context.CancelFunc
	progress        map[string]chatProgress
	wg              sync.WaitGroup
	closed          bool
	recordsRevision atomic.Uint64
}

func newChatApp(store *songya.Store) *chatApp {
	ctx, cancel := context.WithCancel(context.Background())
	a := &chatApp{store: store, ctx: ctx, cancel: cancel, active: map[string]context.CancelFunc{}, progress: map[string]chatProgress{}}
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			a.recover()
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return a
}

func (a *chatApp) Close() error {
	a.mu.Lock()
	a.closed = true
	a.cancel()
	a.mu.Unlock()
	a.wg.Wait()
	return nil
}

func (a *chatApp) initialize(ctx context.Context) error {
	root, err := os.OpenRoot(a.store.Directory())
	if err != nil {
		return err
	}
	defer root.Close()
	if err := root.Mkdir(".chats", 0700); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	if info, err := root.Lstat(".chats"); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("chat storage directory is unavailable")
	}
	// Sync the parent as well: an accepted conversation must survive even when
	// .chats was just created beside an already existing system prompt.
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	if err := errors.Join(dir.Sync(), dir.Close()); err != nil {
		return err
	}
	if _, err := a.readFile("chat-system.md"); errors.Is(err, fs.ErrNotExist) {
		lock, err := a.fileLock(ctx, ".chat-prompt.lock", true)
		if err != nil {
			return err
		}
		defer lock.Close()
		if _, err := a.readFile("chat-system.md"); errors.Is(err, fs.ErrNotExist) {
			return a.publish(ctx, "chat-system.md", []byte(defaultChatPrompt))
		} else {
			return err
		}
	} else {
		return err
	}
}

func (a *chatApp) conversation(id string) (chatConversation, error) {
	var c chatConversation
	if !validChatID(id) {
		return c, fmt.Errorf("invalid conversation ID")
	}
	data, err := a.readFile(".chats/" + id + ".json")
	if err == nil {
		err = json.Unmarshal(data, &c)
	}
	if err == nil && (c.ID != id || c.User == "" || c.Requests == nil) {
		err = fmt.Errorf("conversation metadata needs repair; its file is retained")
	}
	return c, err
}

func (a *chatApp) edit(ctx context.Context, id string, change func(*chatConversation) error) (chatConversation, error) {
	var c chatConversation
	if !validChatID(id) {
		return c, fmt.Errorf("invalid conversation ID")
	}
	lock, err := a.fileLock(ctx, ".chats/"+id+".lock", true)
	if err != nil {
		return c, err
	}
	defer lock.Close()
	c, err = a.conversation(id)
	if errors.Is(err, fs.ErrNotExist) {
		c = chatConversation{ID: id, Messages: []chatMessage{}, Requests: map[string]string{}}
	} else if err != nil {
		return c, err
	}
	if err = change(&c); err != nil {
		return c, err
	}
	c.Revision++
	c.UpdatedAt = time.Now().UnixMilli()
	data, err := json.Marshal(c)
	if err == nil {
		err = a.publish(ctx, ".chats/"+id+".json", data)
	}
	return c, err
}

func (a *chatApp) summaries() ([]map[string]any, []string, error) {
	root, err := os.OpenRoot(a.store.Directory())
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()
	entries, err := fs.ReadDir(root.FS(), ".chats")
	if errors.Is(err, fs.ErrNotExist) {
		return []map[string]any{}, []string{}, nil
	}
	if err != nil {
		return nil, nil, err
	}
	rows := []map[string]any{}
	issues := []string{}
	for _, entry := range entries {
		id := strings.TrimSuffix(entry.Name(), ".json")
		if !strings.HasSuffix(entry.Name(), ".json") || !validChatID(id) {
			continue
		}
		c, err := a.conversation(id)
		if err != nil {
			issues = append(issues, entry.Name()+": "+err.Error())
			continue
		}
		preview := ""
		for _, message := range c.Messages {
			if (message.Role == "user" || message.Role == "assistant") && strings.TrimSpace(message.Content) != "" {
				preview = strings.Join(strings.Fields(message.Content), " ")
			}
		}
		if len([]rune(preview)) > 140 {
			preview = string([]rune(preview)[:140]) + "…"
		}
		rows = append(rows, map[string]any{"id": c.ID, "user": c.User, "title": c.Title, "status": c.Status, "updated_at": c.UpdatedAt, "archived": c.Archived, "preview": preview, "queued": len(c.Queue)})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i]["updated_at"].(int64) > rows[j]["updated_at"].(int64) })
	return rows, issues, nil
}

func (a *chatApp) recover() {
	rows, _, err := a.summaries()
	if err != nil {
		return
	}
	for _, row := range rows {
		if row["status"] == "pending" || row["status"] == "running" {
			a.start(row["id"].(string))
		}
	}
}

func (a *chatApp) start(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || a.active[id] != nil {
		return
	}
	ctx, cancel := context.WithCancel(a.ctx)
	a.active[id] = cancel
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		defer cancel()
		defer func() { a.mu.Lock(); delete(a.active, id); delete(a.progress, id); a.mu.Unlock() }()
		lease, err := a.fileLock(ctx, ".chats/"+id+".run.lock", false)
		if err != nil {
			return
		}
		defer lease.Close()
		for {
			c, err := a.conversation(id)
			if err != nil || (c.Status != "pending" && c.Status != "running") {
				return
			}
			_, err = a.edit(ctx, id, func(c *chatConversation) error {
				if c.Status == "pending" || c.Status == "running" {
					c.Status = "running"
					c.Error = ""
				}
				return nil
			})
			if err == nil {
				err = a.run(ctx, id)
			}
			if err != nil {
				// Application shutdown leaves accepted work pending for startup recovery.
				saveCtx, stop := context.WithTimeout(context.Background(), 3*time.Second)
				defer stop()
				_, _ = a.edit(saveCtx, id, func(c *chatConversation) error {
					if c.Status == "paused" {
						return nil
					}
					if ctx.Err() != nil && c.Status == "pending" {
						return nil
					}
					c.Status = "error"
					c.Error = err.Error()
					if a.ctx.Err() != nil {
						c.Status = "pending"
						c.Error = ""
					}
					return nil
				})
				return
			}
		}
	}()
}

func publicChat(c chatConversation) chatConversation {
	c.Prompt = ""
	c.Requests = nil
	c.RecallConfiguration = nil
	c.RecallConfigIssue = ""
	c.Queue = append([]chatQueuedTurn(nil), c.Queue...)
	for i := range c.Queue {
		c.Queue[i].Prompt = ""
		c.Queue[i].Temperature = nil
		c.Queue[i].RecallConfiguration = nil
		c.Queue[i].RecallConfigIssue = ""
	}
	return c
}

func (a *chatApp) register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/chat/settings", a.settingsHTTP)
	mux.HandleFunc("PUT /api/chat/settings", a.settingsHTTP)
	mux.HandleFunc("GET /api/chat/models", a.modelsHTTP)
	mux.HandleFunc("GET /api/chat/sessions", func(w http.ResponseWriter, r *http.Request) {
		rows, issues, err := a.summaries()
		if err != nil {
			fail(w, err)
			return
		}
		write(w, map[string]any{"sessions": rows, "diagnostics": issues})
	})
	mux.HandleFunc("GET /api/chat/sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		c, err := a.conversation(r.PathValue("id"))
		if err != nil {
			fail(w, err)
			return
		}
		write(w, publicChat(c))
	})
	mux.HandleFunc("PATCH /api/chat/sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Title    *string `json:"title"`
			Archived *bool   `json:"archived"`
		}
		if !decode(w, r, &input) {
			return
		}
		if input.Title != nil && strings.TrimSpace(*input.Title) == "" {
			writeStatus(w, 400, map[string]any{"error": "Enter a conversation name."})
			return
		}
		c, err := a.edit(r.Context(), r.PathValue("id"), func(c *chatConversation) error {
			if c.User == "" {
				return fs.ErrNotExist
			}
			if input.Archived != nil && *input.Archived && (c.Status != "done" || len(c.Queue) > 0) {
				return fmt.Errorf("%w: finish the saved turn before archiving this conversation", songya.ErrConflict)
			}
			if input.Title != nil {
				c.Title = strings.Join(strings.Fields(*input.Title), " ")
			}
			if input.Archived != nil {
				c.Archived = *input.Archived
			}
			return nil
		})
		if err != nil {
			fail(w, err)
			return
		}
		write(w, publicChat(c))
	})
	mux.HandleFunc("GET /api/chat/sessions/{id}/events", a.eventsHTTP)
	mux.HandleFunc("POST /api/chat", a.sendHTTP)
	mux.HandleFunc("POST /api/chat/sessions/{id}/retry", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		c, err := a.edit(r.Context(), id, func(c *chatConversation) error {
			if c.User == "" {
				return fs.ErrNotExist
			}
			if c.Status == "error" || c.Status == "paused" {
				c.Status = "pending"
				c.Error = ""
			}
			return nil
		})
		if err != nil {
			fail(w, err)
			return
		}
		a.start(id)
		writeStatus(w, 202, publicChat(c))
	})
	mux.HandleFunc("POST /api/chat/sessions/{id}/pause", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		c, err := a.edit(r.Context(), id, func(c *chatConversation) error {
			if c.User == "" {
				return fs.ErrNotExist
			}
			if c.Status == "pending" || c.Status == "running" {
				c.Status = "paused"
			}
			return nil
		})
		if err != nil {
			fail(w, err)
			return
		}
		a.mu.Lock()
		if cancel := a.active[id]; cancel != nil {
			cancel()
		}
		a.mu.Unlock()
		write(w, publicChat(c))
	})
}

func (a *chatApp) sendHTTP(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ID        string   `json:"id"`
		RequestID string   `json:"request_id"`
		User      string   `json:"user"`
		Text      string   `json:"text"`
		Uploads   []string `json:"uploads,omitempty"`
	}
	if !decode(w, r, &input) {
		return
	}
	input.User = strings.TrimSpace(input.User)
	if !validChatID(input.ID) || input.RequestID == "" || len(input.RequestID) > 120 || input.User == "" || input.User == "all" || (strings.TrimSpace(input.Text) == "" && len(input.Uploads) == 0) {
		writeStatus(w, 400, map[string]any{"error": "Choose a user and enter a message with valid conversation/request IDs."})
		return
	}
	if err := a.initialize(r.Context()); err != nil {
		fail(w, err)
		return
	}
	prompt, err := a.readFile("chat-system.md")
	if err != nil {
		fail(w, err)
		return
	}
	configuration, configurationIssue := a.store.Configuration()
	settings, err := a.settings()
	if err != nil {
		fail(w, err)
		return
	}
	uploads := []songya.Upload{}
	for _, id := range input.Uploads {
		state, err := a.store.UploadState(r.Context(), id, "chat:"+input.User)
		if err != nil {
			fail(w, fmt.Errorf("Completed upload unavailable for this user: %w", err))
			return
		}
		uploads = append(uploads, state.Upload)
	}
	fingerprint := chatTurnFingerprint(input.User, input.Text, uploads)
	c, err := a.edit(r.Context(), input.ID, func(c *chatConversation) error {
		if c.User != "" && c.User != input.User {
			return fmt.Errorf("%w: this conversation belongs to another user", songya.ErrConflict)
		}
		if previous, ok := c.Requests[input.RequestID]; ok {
			if previous != fingerprint {
				return fmt.Errorf("%w: request ID already belongs to another message", songya.ErrConflict)
			}
			return nil
		}
		if c.Archived {
			return fmt.Errorf("%w: restore this conversation before sending another message", songya.ErrConflict)
		}
		c.User = input.User
		turn := chatQueuedTurn{ID: input.RequestID, Text: input.Text, Uploads: uploads,
			Prompt:      string(prompt) + "\n\nCurrent turn received at " + time.Now().Format(time.RFC3339) + ".",
			Temperature: &settings.Temperature, RecallConfiguration: &configuration}
		if configurationIssue != nil {
			turn.RecallConfigIssue = configurationIssue.Error()
		}
		if c.Title == "" {
			c.Title = input.Text
			if c.Title == "" && len(uploads) > 0 {
				c.Title = uploads[0].Filename
			}
			if len([]rune(c.Title)) > 60 {
				c.Title = string([]rune(c.Title)[:60]) + "…"
			}
		}
		c.Requests[input.RequestID] = fingerprint
		c.Queue = append(c.Queue, turn)
		c.advance()
		return nil
	})
	if err != nil {
		fail(w, err)
		return
	}
	a.start(input.ID)
	writeStatus(w, 202, publicChat(c))
}

func (a *chatApp) eventsHTTP(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validChatID(id) {
		fail(w, fmt.Errorf("invalid conversation ID"))
		return
	}
	root, err := os.OpenRoot(a.store.Directory())
	if err != nil {
		fail(w, err)
		return
	}
	defer root.Close()
	name := ".chats/" + id + ".json"
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() {
		fail(w, fmt.Errorf("conversation is unavailable"))
		return
	}
	c, err := a.conversation(id)
	if err != nil {
		fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	flush := http.NewResponseController(w)
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	revision := int64(-1)
	previousStage, previousTurn := "", ""
	heartbeat := time.Time{}
	for {
		latest, err := root.Lstat(name)
		if err == nil && (!latest.Mode().IsRegular() || !os.SameFile(info, latest) || !latest.ModTime().Equal(info.ModTime()) || latest.Size() != info.Size()) {
			info = latest
			c, err = a.conversation(id)
		}
		if err != nil {
			data, _ := json.Marshal(map[string]any{"error": err.Error()})
			_, _ = fmt.Fprintf(w, "event: unavailable\ndata: %s\n\n", data)
			_ = flush.Flush()
			return
		}
		if c.Revision != revision {
			data, _ := json.Marshal(publicChat(c))
			if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
				return
			}
			revision = c.Revision
		}
		if c.Status != "running" && c.Status != "pending" {
			_ = flush.Flush()
			return
		}
		p := a.currentProgress(id)
		if p.TurnID != c.TurnID {
			p = chatProgress{TurnID: c.TurnID, Stage: "waiting"}
		}
		if p.Stage != previousStage || p.TurnID != previousTurn || time.Since(heartbeat) > 5*time.Second {
			data, _ := json.Marshal(p)
			if _, err := fmt.Fprintf(w, "event: progress\ndata: %s\n\n", data); err != nil {
				return
			}
			previousStage, previousTurn = p.Stage, p.TurnID
			heartbeat = time.Now()
		}
		if flush.Flush() != nil {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-a.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
