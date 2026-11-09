package web

import (
	"errors"
	"fmt"
	"github.com/aristath/songya"
	"net/http"
	"strings"
)

func (a *chatApp) registerTypes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/type-users", func(w http.ResponseWriter, r *http.Request) {
		users, err := songya.TypeUsers(r.Context(), a.store)
		if err != nil {
			fail(w, err)
			return
		}
		write(w, map[string]any{"users": users})
	})
	mux.HandleFunc("GET /api/types/{user}", a.typeSettingsHTTP)
	mux.HandleFunc("PUT /api/types/{user}", a.typeSettingsHTTP)
}
func (a *chatApp) typeSettingsHTTP(w http.ResponseWriter, r *http.Request) {
	user := strings.TrimSpace(r.PathValue("user"))
	if user == "" || user == "all" {
		writeStatus(w, 400, map[string]any{"error": "Choose one user for type settings."})
		return
	}
	if r.Method == http.MethodPut {
		var input struct {
			Key, Version string
			KindColors   map[songya.Destination]string `json:"kind_colors"`
			Kinds        map[songya.Destination][]songya.TypeDefinition
			Replacements map[songya.Destination]map[string]string
		}
		if !decode(w, r, &input) {
			return
		}
		receipt, err := a.store.SaveTypeConfiguration(songya.WithTypeUser(r.Context(), user), songya.TypeConfiguration{User: user, Kinds: input.Kinds, KindColors: input.KindColors}, input.Version, input.Replacements, input.Key)
		if err != nil {
			if receipt.Status == "not_accepted" || receipt.Status == "" {
				status := 422
				if errors.Is(err, songya.ErrConflict) {
					status = 409
				}
				writeStatus(w, status, map[string]any{"error": err.Error(), "rejected": true})
				return
			}
			writeStatus(w, 503, map[string]any{"error": fmt.Sprintf("Type settings work is saved for recovery. Retry this same save: %v", err), "receipt": receipt})
			return
		}
		a.recordsChanged()
		if receipt.Status != "committed" {
			writeStatus(w, 503, map[string]any{"error": "Settings work is retained; retry the same save.", "receipt": receipt})
			return
		}
	}
	cfg, err := a.store.TypeConfiguration(r.Context(), user)
	if err != nil {
		fail(w, err)
		return
	}
	write(w, cfg)
}

// Color failures do not hide readable records or disable the graph.
func (a *chatApp) colorConfiguration(r *http.Request) songya.TypeConfiguration {
	cfg := songya.DefaultTypeConfiguration("")
	user := strings.TrimSpace(r.URL.Query().Get("classification_user"))
	if user != "" && user != "all" {
		if saved, err := a.store.TypeConfiguration(r.Context(), user); err == nil {
			cfg = saved
		}
	}
	return cfg
}
