package web

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/aristath/songya"
)

func (a *chatApp) timelineHTTP(w http.ResponseWriter, r *http.Request) {
	catalog, err := a.store.Scan(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	user := r.URL.Query().Get("user")
	timeline, err := catalog.Timeline(r.Context(), songya.Query{User: user, Kind: songya.Memories})
	if err != nil {
		fail(w, err)
		return
	}
	colors := a.colorConfiguration(r)
	byID := map[string]songya.Record{}
	for _, record := range catalog.Records {
		byID[record.ID] = record
	}
	rows := []map[string]any{}
	for _, entry := range timeline.Records {
		record := byID[entry.ID]
		issues := []string{}
		for _, issue := range record.Diagnostics {
			if strings.Contains(strings.ToLower(issue), "time") && !strings.Contains(issue, "created_at") && !strings.Contains(issue, "updated_at") {
				issues = append(issues, issue)
			}
		}
		for _, issue := range timeline.Diagnostics {
			if issue.Path == entry.ID || issue.Path == record.Source {
				issues = append(issues, issue.Message)
			}
		}
		orderLinks := []map[string]any{}
		for _, relation := range record.Relations {
			if relation.Type != "after" && relation.Type != "caused_by" {
				continue
			}
			target, exists := byID[relation.Target]
			orderLinks = append(orderLinks, map[string]any{"id": relation.Target, "title": target.Title, "relation": relation.Type, "present": exists})
			if !exists {
				issues = append(issues, "An ordering link points to an unavailable record: "+relation.Target)
			}
		}
		// Excerpts are display text only. The saved body remains in its record.
		excerpt := strings.ToValidUTF8(record.Body, "�")
		if len(excerpt) > 480 {
			end := 480
			for !utf8.RuneStart(excerpt[end]) {
				end--
			}
			excerpt = excerpt[:end] + "…"
		}
		rows = append(rows, map[string]any{"id": entry.ID, "source": record.Source, "title": record.Title, "user": record.User, "kind": record.Kind, "time": entry.Time, "relations": record.Relations, "order_links": orderLinks, "excerpt": excerpt, "color": colors.RecordColor(record), "issues": issues, "pending": record.Pending || record.Unconfirmed})
	}
	write(w, map[string]any{"records": rows, "constraints": timeline.Constraints, "diagnostics": timeline.Diagnostics})
}
