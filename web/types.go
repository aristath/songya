package web

import (
	"errors"
	"fmt"
	"github.com/aristath/songya"
	"net/http"
)

func (a *chatApp) registerTypes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/types", a.typeSettingsHTTP)
	mux.HandleFunc("PUT /api/types", a.typeSettingsHTTP)
}
func (a *chatApp) typeSettingsHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPut {
		var input struct {
			MyEntity     string `json:"my_entity"`
			Key, Version string
			KindColors   map[songya.Destination]string `json:"kind_colors"`
			Kinds        map[songya.Destination][]songya.TypeDefinition
			Replacements map[songya.Destination]map[string]string
		}
		if !decode(w, r, &input) {
			return
		}
		receipt, err := a.store.SaveTypeConfiguration(r.Context(), songya.TypeConfiguration{MyEntity: input.MyEntity, Kinds: input.Kinds, KindColors: input.KindColors}, input.Version, input.Replacements, input.Key)
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
	cfg, err := a.store.TypeConfiguration(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	write(w, cfg)
}

// Color failures do not hide readable records or disable the graph.
func (a *chatApp) colorConfiguration(r *http.Request) songya.TypeConfiguration {
	cfg, _ := a.store.TypeConfiguration(r.Context())
	return cfg
}
