package web

import (
	"encoding/json"
	"fmt"
	"time"
)

// Progress is transient connection state, never testimony or a saved record.
type chatProgress struct {
	TurnID       string `json:"turn_id"`
	Stage        string `json:"stage"`
	StartedAt    int64  `json:"started_at"`
	UpdatedAt    int64  `json:"updated_at"`
	MessageIndex int    `json:"message_index"`
	Reasoning    string `json:"reasoning,omitempty"`
}

func (a *chatApp) setProgress(id, turn, stage string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	p := a.progress[id]
	now := time.Now().UnixMilli()
	if p.TurnID != turn {
		p = chatProgress{TurnID: turn, StartedAt: now}
	}
	if stage == "waiting" {
		p.Reasoning = ""
		p.MessageIndex = 0
	}
	p.Stage, p.UpdatedAt = stage, now
	a.progress[id] = p
}

// Only the current model request is streamed here. Its final reasoning is stored
// with that assistant message, separately from the reply and original testimony.
func (a *chatApp) setModelProgress(id, stage string, index int, reasoning string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	p := a.progress[id]
	p.Stage, p.MessageIndex, p.Reasoning, p.UpdatedAt = stage, index, reasoning, time.Now().UnixMilli()
	a.progress[id] = p
}

func (a *chatApp) currentProgress(id string) chatProgress {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.progress[id]
}

// In a multi-file turn, a named file must not be paired with another file's
// upload value. The public file tools and their rename support are unchanged.
func chatFileArguments(c chatConversation, name string, args map[string]any) error {
	if name != "attach_file" && name != "replace_file" {
		return nil
	}
	filename, _ := args["filename"].(string)
	upload, _ := args["upload"].(string)
	if filename == "" || upload == "" {
		return nil
	}
	for _, m := range c.Messages {
		if m.Role != "user" || m.TurnID != c.TurnID || len(m.Uploads) < 2 {
			continue
		}
		expected := ""
		matches := 0
		for _, file := range m.Uploads {
			if file.Filename == filename {
				expected = file.ID
				matches++
			}
		}
		if matches == 1 && expected != upload {
			correction, _ := json.Marshal(map[string]string{"filename": filename, "upload": expected})
			return fmt.Errorf("Nothing changed: this filename belongs to a different uploaded file. Use the matching pair %s. Do not swap filenames to bypass this check", correction)
		}
	}
	return nil
}
