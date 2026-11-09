package web

import (
	"mime"
	"net/http"
	"strconv"

	"github.com/aristath/songya"
)

func (a *chatApp) registerFiles(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/records/{id}/profile-pictures", a.profilePictures)
	mux.HandleFunc("PUT /api/uploads/{key}", func(w http.ResponseWriter, r *http.Request) {
		receipt, err := a.store.Upload(r.Context(), r.PathValue("key"), r.URL.Query().Get("scope"), r.URL.Query().Get("filename"), r.Body)
		if err != nil {
			fail(w, err)
			return
		}
		write(w, receipt)
	})
	mux.HandleFunc("GET /api/uploads/by-key/{key}", func(w http.ResponseWriter, r *http.Request) {
		state, err := a.store.UploadStateByKey(r.Context(), r.PathValue("key"), r.URL.Query().Get("scope"))
		if err != nil {
			fail(w, err)
			return
		}
		write(w, state)
	})
	mux.HandleFunc("GET /api/uploads/{upload}", func(w http.ResponseWriter, r *http.Request) {
		state, err := a.store.UploadState(r.Context(), r.PathValue("upload"), r.URL.Query().Get("scope"))
		if err != nil {
			fail(w, err)
			return
		}
		write(w, state)
	})

	mux.HandleFunc("GET /api/uploads", func(w http.ResponseWriter, r *http.Request) {
		scope := r.URL.Query().Get("scope")
		if scope == "" {
			writeStatus(w, 400, map[string]any{"error": "An upload scope is required"})
			return
		}
		uploads, err := a.store.Uploads(r.Context(), scope)
		if err != nil {
			fail(w, err)
			return
		}
		write(w, map[string]any{"uploads": uploads})
	})
	mux.HandleFunc("GET /api/records/{id}/files", func(w http.ResponseWriter, r *http.Request) {
		files, err := a.store.Attachments(r.Context(), r.PathValue("id"))
		if err != nil {
			fail(w, err)
			return
		}
		write(w, files)
	})
	mux.HandleFunc("POST /api/records/{id}/files", func(w http.ResponseWriter, r *http.Request) {
		var request songya.FileChange
		if !decode(w, r, &request) {
			return
		}
		request.ID = r.PathValue("id")
		receipt, err := a.store.ChangeFile(r.Context(), request)
		// A pending effect may already be visible; clients must refresh after either result.
		a.recordsRevision.Add(1)
		if err != nil {
			writeStatus(w, http.StatusConflict, map[string]any{"error": err.Error(), "receipt": receipt})
			return
		}
		write(w, receipt)
	})
	mux.HandleFunc("GET /api/records/{id}/files/{filename}/text", func(w http.ResponseWriter, r *http.Request) {
		offset, _ := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
		text, next, err := a.store.ReadAttachmentText(r.Context(), r.PathValue("id"), r.PathValue("filename"), offset, 16384)
		if err != nil {
			fail(w, err)
			return
		}
		write(w, map[string]any{"text": text, "next": next})
	})
	mux.HandleFunc("GET /api/records/{id}/files/{filename}", func(w http.ResponseWriter, r *http.Request) {
		file, info, err := a.store.OpenAttachment(r.Context(), r.PathValue("id"), r.PathValue("filename"))
		if err != nil {
			fail(w, err)
			return
		}
		defer file.Close()
		if info.Preview == "image" && r.URL.Query().Get("preview") == "1" {
			if r.URL.Query().Get("thumbnail") == "1" {
				imagePreview(w, r, file, 512)
				return
			}
			if info.MediaType == "image/heic" || info.MediaType == "image/heif" {
				imagePreview(w, r, file, 1600)
				return
			}
		}
		disposition := "attachment"
		media := "application/octet-stream"
		if r.URL.Query().Get("preview") == "1" && info.Preview != "download" {
			disposition = "inline"
			media = info.MediaType
		}
		w.Header().Set("Content-Type", media)
		w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": info.Filename}))
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
		stat, err := file.Stat()
		if err != nil {
			fail(w, err)
			return
		}
		http.ServeContent(w, r, info.Filename, stat.ModTime(), file)
	})
}
