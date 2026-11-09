package web

import (
	"net/http"

	"github.com/aristath/songya"
)

// A picker references existing files; nothing is copied into the entity folder.
func (a *chatApp) profilePictures(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	catalog, err := a.store.Scan(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	records := map[string]songya.Record{}
	for _, record := range catalog.Records {
		records[record.ID] = record
	}
	if records[id].Kind != songya.Entities {
		writeStatus(w, 400, map[string]any{"error": "Choose an entity to select a profile picture."})
		return
	}
	linked := map[string]bool{}
	for _, edge := range catalog.Connections(id) {
		other := edge.Source
		if other == id {
			other = edge.Target
		}
		if records[other].Kind == songya.Memories {
			linked[other] = true
		}
	}
	pictures := []map[string]any{}
	diagnostics := []string{}
	for _, record := range catalog.Records {
		if !linked[record.ID] {
			continue
		}
		listing, err := a.store.Attachments(r.Context(), record.ID)
		if err != nil {
			diagnostics = append(diagnostics, record.Title+": "+err.Error())
			continue
		}
		for _, diagnostic := range listing.Diagnostics {
			diagnostics = append(diagnostics, record.Title+": "+diagnostic.Message)
		}
		for _, file := range listing.Files {
			if file.Preview == "image" {
				pictures = append(pictures, map[string]any{"record": record.ID, "title": record.Title, "revision": listing.Revision, "filename": file.Filename})
			}
		}
	}
	write(w, map[string]any{"pictures": pictures, "diagnostics": diagnostics})
}
