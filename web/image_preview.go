package web

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"time"
)

var imageWorkers = make(chan struct{}, 4)

// Browser-sized previews are derived on demand; original attachments stay intact.
// FFmpeg also decodes HEIC/HEIF, which browsers cannot consistently display.
func imagePreview(w http.ResponseWriter, r *http.Request, file *os.File, size int) {
	stat, err := file.Stat()
	if err != nil {
		fail(w, err)
		return
	}
	etag := fmt.Sprintf("\"image-%x-%x-%d\"", stat.ModTime().UnixNano(), stat.Size(), size)
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "private, max-age=0, must-revalidate")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	select {
	case imageWorkers <- struct{}{}:
		defer func() { <-imageWorkers }()
	case <-ctx.Done():
		http.Error(w, "Image preview is busy; retry shortly", http.StatusServiceUnavailable)
		return
	}
	cmd := exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-threads", "1", "-i", "/proc/self/fd/3", "-frames:v", "1", "-filter_complex", fmt.Sprintf("scale=min(%d\\,iw):min(%d\\,ih):force_original_aspect_ratio=decrease", size, size), "-threads", "1", "-f", "image2pipe", "-c:v", "png", "-")
	cmd.ExtraFiles = []*os.File{file}
	var output bytes.Buffer
	cmd.Stdout = &output
	if err := cmd.Run(); err != nil || output.Len() == 0 {
		http.Error(w, "Image preview unavailable. The original file is still available to download.", http.StatusUnprocessableEntity)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Disposition", "inline")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, "preview.png", stat.ModTime(), bytes.NewReader(output.Bytes()))
}
