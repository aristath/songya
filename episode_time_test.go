package songya

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestExactDatesOpenBoundsAndUnknownTimes(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		fields map[string]any
	}{
		{"exact", map[string]any{"date": "2024-12-31", "earliest": nil, "latest": nil}},
		{"open-start", map[string]any{"date": nil, "earliest": "2024-01-01", "latest": nil}},
		{"open-end", map[string]any{"date": nil, "earliest": nil, "latest": "2023-12-31"}},
		{"unknown", map[string]any{"date": nil, "earliest": nil, "latest": nil}},
	}
	ids := map[string]string{}
	for _, test := range cases {
		doc := createRecord(t, store, Memories, map[string]any{"time": test.fields}, test.name)
		ids[test.name] = doc.ID
		data, err := json.Marshal(doc.Record(Memories).Time)
		if err != nil {
			t.Fatal(err)
		}
		var actual map[string]any
		if err := json.Unmarshal(data, &actual); err != nil {
			t.Fatal(err)
		}
		if len(actual) != 3 {
			t.Fatalf("%s: time is not the three-field contract: %s", test.name, data)
		}
		for key, value := range test.fields {
			if actual[key] != value {
				t.Fatalf("%s: %s became %v instead of %v", test.name, key, actual[key], value)
			}
		}
	}
	timeline, err := store.Timeline(context.Background(), Query{Kind: Memories})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"exact", "open-start"} {
		found := false
		for _, edge := range timeline.Constraints {
			found = found || edge.Before == ids["open-end"] && edge.After == ids[name] && edge.Usable
		}
		if !found {
			t.Fatalf("open-ended bounds did not order %s: %+v", name, timeline)
		}
	}
	for _, row := range timeline.Records {
		if row.ID == ids["unknown"] && !row.Unordered {
			t.Fatal("unknown event acquired an invented position")
		}
	}
}

func TestDamagedTimesRetainDataAndIndependentChronology(t *testing.T) {
	store, _ := Open(t.TempDir())
	for index, fields := range []any{
		map[string]any{"date": "2024-12-31", "earliest": "2023-01-01"},
		map[string]any{"date": []any{"2024-12-31"}},
		map[string]any{"unexpected": "Keep this original detail"},
		"summer with no known year",
	} {
		damaged := createDamagedTimeRecord(t, store, fields, "Intact narrative")
		later := createRecord(t, store, Memories, map[string]any{"relations": []Relation{{"after", damaged.ID}}}, "Later")
		doc, _ := store.Read(damaged.ID)
		if len(doc.Diagnostics()) == 0 || doc.Body != "Intact narrative" {
			t.Fatalf("case %d lost damage or prose", index)
		}
		original := metadataField(&doc.Frontmatter, "time")
		raw, _ := doc.Marshal()
		restored := Parse(raw)
		var oldValue, newValue any
		_ = original.Decode(&oldValue)
		_ = metadataField(&restored.Frontmatter, "time").Decode(&newValue)
		old, _ := json.Marshal(oldValue)
		now, _ := json.Marshal(newValue)
		if string(old) != string(now) {
			t.Fatalf("case %d rewrote unknown data", index)
		}
		timeline, err := store.Timeline(context.Background(), Query{Kind: Memories})
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, edge := range timeline.Constraints {
			found = found || edge.Before == damaged.ID && edge.After == later.ID && edge.Usable
		}
		if !found {
			t.Fatal("damaged date disabled independent relative order")
		}
	}
}

func TestEventTimeNormalizationKeepsAnnotationsAndDuplicateData(t *testing.T) {
	doc := Parse([]byte("---\ntime:\n  date: '2024-12-31' # Important date\n---\nBody"))
	data, err := doc.Marshal()
	if err != nil || !strings.Contains(string(data), "# Important date") {
		t.Fatal("date annotation lost", string(data), err)
	}
	duplicate := Parse([]byte("---\ntime:\n  date: '2024-12-31'\n  date: '2025-01-01'\n---\nBody"))
	data, err = duplicate.Marshal()
	if err != nil || !strings.Contains(string(data), "2024-12-31") || !strings.Contains(string(data), "2025-01-01") || len(duplicate.Diagnostics()) == 0 {
		t.Fatal("duplicate dates discarded", string(data), err)
	}
	compound := Parse([]byte("---\ntime: {date: [2024-12-31]}\n---\nBody"))
	if compound.Record(Memories).Time.Date != nil {
		t.Fatal("compound time became a calendar claim")
	}
}

// Simulate pre-existing damage or an external file edit, not a new API write.
func createDamagedTimeRecord(t *testing.T, store *Store, value any, body string) *Document {
	t.Helper()
	doc := createRecord(t, store, Memories, map[string]any{"user": "ari"}, body)
	if err := doc.Set("time", value); err != nil {
		t.Fatal(err)
	}
	data, err := doc.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := fixtureWriteFile(filepath.Join(store.path, "memories", doc.ID+"/record.md"), data, 0600); err != nil {
		t.Fatal(err)
	}
	result, err := store.Read(doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
