package web

import (
	"fmt"
	"net/http"
	"time"
)

// A fresh connection always invalidates the browser's view, including after a
// restart or missed notification. Only the counter is polled, never the store.
func (a *chatApp) recordEventsHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	flush := http.NewResponseController(w)
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	var previous uint64
	first := true
	heartbeat := time.Now()
	for {
		revision := a.recordsRevision.Load()
		if first || revision != previous {
			if _, err := fmt.Fprintf(w, "data: %d\n\n", revision); err != nil {
				return
			}
			first, previous = false, revision
		} else if time.Since(heartbeat) > 10*time.Second {
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return
			}
			heartbeat = time.Now()
		}
		if err := flush.Flush(); err != nil {
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
