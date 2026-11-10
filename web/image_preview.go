package web

import (
	"bytes"
	"fmt"
	"net/http"
	"os"

	"github.com/aristath/songya"
)

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
	data, mediaType, err := songya.AttachmentImage(r.Context(), file, size)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("Content-Disposition", "inline")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, "preview.png", stat.ModTime(), bytes.NewReader(data))
}
