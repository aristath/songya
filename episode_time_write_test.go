package songya

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewTimeFieldsAcceptOnlyCalendarDatesOrNull(t *testing.T) {
	ctx := context.Background()
	for _, bad := range []any{"ongoing", "since July", "2025-07", "2025", "2025-02-29", "2025-04-31", "2025-07-01T00:00:00Z", "", " ", 20250701, false, []string{"2025-07-01"}} {
		t.Run(strings.ReplaceAll(strings.TrimSpace(fieldText(bad)), "/", "-"), func(t *testing.T) {
			store, _ := Open(t.TempDir())
			timing := map[string]any{"date": nil, "earliest": "2025-07-01", "latest": bad}
			if _, err := store.Create(Memories, map[string]any{"user": "ari", "time": timing}, "Not published"); err == nil {
				t.Fatal("Create accepted a non-date", bad)
			}
			if r, err := store.SaveMemory(ctx, MemoryInput{User: "ari", Text: textValue("Not published"), Time: timing}); err == nil || r.Status != "not_accepted" {
				t.Fatal("SaveMemory accepted a non-date", r, err)
			}
			doc := &Document{Body: "Not published"}
			_ = doc.Set("user", "ari")
			_ = doc.Set("time", timing)
			source, _ := doc.Marshal()
			if r, err := store.SaveSource(ctx, "bad-source", "", Memories, "", string(source)); err == nil || r.Status != "not_accepted" {
				t.Fatal("source write accepted a non-date", r, err)
			}
			catalog, _ := store.Scan(ctx)
			if len(catalog.Records) != 0 {
				t.Fatal("rejected dates created records", catalog.Records)
			}
		})
	}
	store, _ := Open(t.TempDir())
	for _, input := range []any{nil, map[string]any{"date": "2024-02-29"}, map[string]any{"date": nil, "earliest": "2025-07-01", "latest": nil}} {
		if _, err := store.SaveMemory(ctx, MemoryInput{User: "ari", Text: textValue("A useful memory"), Time: input}); err != nil {
			t.Fatal("valid date/null rejected", input, err)
		}
	}
}

func TestOngoingStateAndInvalidDateUpdatesPreserveStoredAccount(t *testing.T) {
	store, _ := Open(t.TempDir())
	ctx := context.Background()
	r, err := store.SaveMemory(ctx, MemoryInput{User: "ari", Text: textValue("Since July 2025, Ari prefers tea."), Time: map[string]any{"date": nil, "earliest": "2025-07-01", "latest": nil}})
	if err != nil {
		t.Fatal(err)
	}
	id := r.Refs["memory"]
	doc, _ := store.Read(id)
	timing := doc.Record(Memories).Time
	if timing.Date != nil || dateValue(timing.Earliest) != "2025-07-01" || timing.Latest != nil {
		t.Fatal("ongoing state acquired an exact day or invented end", timing)
	}
	file := filepath.Join(store.path, "memories", id+"/record.md")
	before, _ := os.ReadFile(file)
	if _, err := store.SaveMemory(ctx, MemoryInput{ID: id, Append: "This must not be partly saved.", Time: map[string]any{"latest": "ongoing"}}); err == nil {
		t.Fatal("invalid date update accepted")
	}
	if _, err := store.Update(id, doc.Revision, map[string]any{"time": map[string]any{"latest": "unknown"}}, "This must not replace the body"); err == nil {
		t.Fatal("direct update accepted freeform text")
	}
	if _, err := store.SaveMemory(ctx, MemoryInput{ID: id, Metadata: map[string]any{"time": map[string]any{"latest": "last summer"}}}); err == nil {
		t.Fatal("metadata bypassed date validation")
	}
	after, _ := os.ReadFile(file)
	if string(before) != string(after) {
		t.Fatal("invalid date write changed the stored account")
	}
	if _, err := store.SaveMemory(ctx, MemoryInput{ID: id, Time: map[string]any{"latest": "2026-03-12"}}); err != nil {
		t.Fatal("known ending date could not be saved", err)
	}
}

func TestExistingBadDateRemainsReadableAndAllowsUnrelatedWork(t *testing.T) {
	store, _ := Open(t.TempDir())
	doc := createDamagedTimeRecord(t, store, map[string]any{"date": "next summer", "earliest": "2025-07-01", "latest": nil}, "An intact old account.")
	timing := doc.Record(Memories).Time
	if timing.Date != nil || dateValue(timing.Earliest) != "2025-07-01" || len(doc.Diagnostics()) == 0 {
		t.Fatal("bad date was returned as valid or disabled a healthy bound", timing, doc.Diagnostics())
	}
	changed, err := store.Update(doc.ID, doc.Revision, map[string]any{"title": "A corrected title"}, doc.Body)
	if err != nil {
		t.Fatal("old bad date blocked an unrelated edit", err)
	}
	raw, _ := changed.Marshal()
	if !strings.Contains(string(raw), "next summer") || changed.Body != doc.Body {
		t.Fatal("unrelated edit discarded old damaged data", string(raw))
	}
}
