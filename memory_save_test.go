package songya

import (
	"context"
	"strings"
	"testing"
)

func TestMemoryDatePatchRetainsDamagedInformationAndHealthyWork(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for i, previous := range []any{"summer of an unknown year", map[string]any{"date": []any{"1981-03-06"}}} {
		text := "The original account remains intact."
		created := createDamagedTimeRecord(t, store, previous, text)
		id := created.ID
		patched, err := store.SaveMemory(ctx, MemoryInput{ID: id, Append: "The year was later confirmed as 1981.", Time: map[string]any{"earliest": "1981-01-01", "latest": "1981-12-31"}})
		if err != nil || patched.Status != "committed" {
			t.Fatal(patched, err)
		}
		doc, err := store.Read(id)
		if err != nil || !strings.Contains(doc.Body, text) || !strings.Contains(doc.Body, "confirmed") {
			t.Fatal(doc, err)
		}
		raw, err := doc.Marshal()
		if err != nil || !strings.Contains(string(raw), "unparsed_previous_time") {
			t.Fatalf("case %d lost unreadable prior data: %s %v", i, raw, err)
		}
		if tme := doc.Record(Memories).Time; dateValue(tme.Earliest) != "1981-01-01" || dateValue(tme.Latest) != "1981-12-31" {
			t.Fatal(tme)
		}
		original, err := store.BatchData(ctx, patched.Key, id, "before")
		if err != nil || !strings.Contains(string(original), "time:") {
			t.Fatal("repair lost original snapshot", err)
		}
	}
	healthy := "A separate healthy memory still saves."
	if saved, err := store.SaveMemory(ctx, MemoryInput{Text: &healthy}); err != nil || saved.Status != "committed" {
		t.Fatal(saved, err)
	}
}
