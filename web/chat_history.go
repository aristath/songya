package web

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"

	"github.com/aristath/songya"
	"github.com/gofrs/flock"
)

type chatRun struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// Serialize user actions while a rewind or deletion waits for the worker.
func (a *chatApp) sessionAction(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !validChatID(id) {
			fail(w, fmt.Errorf("invalid conversation ID"))
			return
		}
		lock, err := a.fileLock(r.Context(), ".chats/"+id+".action.lock", true)
		if err != nil {
			fail(w, err)
			return
		}
		defer lock.Close()
		next(w, r)
	}
}

// Stop and join the worker before changing its history. A cancelled HTTP request
// leaves the conversation paused and intact, ready to resume.
func (a *chatApp) stopChat(ctx context.Context, id string) (*flock.Flock, error) {
	_, err := a.edit(ctx, id, func(c *chatConversation) error {
		if c.User == "" {
			return fs.ErrNotExist
		}
		c.Status = "paused"
		return nil
	})
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	run := a.active[id]
	if run != nil {
		run.cancel()
	}
	a.mu.Unlock()
	if run != nil {
		select {
		case <-run.done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return a.fileLock(ctx, ".chats/"+id+".run.lock", true)
}

func (c *chatConversation) messageForTurn(id string) *chatMessage {
	for i := range c.Messages {
		if c.Messages[i].Role == "user" && c.Messages[i].TurnID == id {
			return &c.Messages[i]
		}
	}
	for _, turn := range c.Queue {
		if turn.ID == id {
			return &chatMessage{Role: "user", Content: turn.Text, TurnID: turn.ID, Uploads: turn.Uploads}
		}
	}
	return nil
}

func (c *chatConversation) rewind(turn string) error {
	for i, message := range c.Messages {
		if message.Role == "user" && message.TurnID == turn {
			// Reused tool-call IDs must not replay receipts from discarded replies.
			c.BranchID = chatID()
			c.Messages = c.Messages[:i]
			c.Queue, c.Draft, c.Recall = nil, nil, nil
			c.Status = "done"
			return nil
		}
	}
	for i, queued := range c.Queue {
		if queued.ID == turn {
			c.Queue = c.Queue[:i]
			c.Status = "pending"
			return nil
		}
	}
	return fmt.Errorf("%w: this message has changed; reload the conversation", songya.ErrConflict)
}

func (a *chatApp) deleteChatHTTP(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := a.conversation(id); err == nil {
		lease, err := a.stopChat(r.Context(), id)
		if err != nil {
			fail(w, err)
			return
		}
		defer lease.Close()
	} else if !errors.Is(err, fs.ErrNotExist) {
		fail(w, err)
		return
	}
	root, err := os.OpenRoot(a.store.Directory())
	if err != nil {
		fail(w, err)
		return
	}
	defer root.Close()
	if err := root.Remove(".chats/" + id + ".json"); err != nil && !errors.Is(err, fs.ErrNotExist) {
		fail(w, err)
		return
	}
	dir, err := root.Open(".chats")
	if err == nil {
		err = errors.Join(dir.Sync(), dir.Close())
	}
	if err != nil {
		fail(w, err)
		return
	}
	write(w, map[string]any{"deleted": true})
}
