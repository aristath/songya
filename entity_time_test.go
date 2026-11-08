package songya

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestEntityLifetimeWritesPatchClearAndKeepRecordContent(t *testing.T) {
	s, _ := Open(t.TempDir())
	ctx := context.Background()
	doc, err := s.Create(Entities, map[string]any{"name": "John", "types": map[string]string{"reader": "person"}, "time": map[string]any{"start": "1990-04-22", "end": nil}}, "A short identification.")
	if err != nil {
		t.Fatal(err)
	}
	before := doc.Metadata()
	key := "entity-end"
	r, err := s.SaveNamed(ctx, Entities, NamedInput{Key: key, ID: doc.ID, Time: map[string]any{"end": "2070-06-01"}})
	if err != nil || r.Status != "committed" {
		t.Fatal(r, err)
	}
	after, _ := s.Read(doc.ID)
	time := after.Record(Entities).Time
	if dateValue(time.Start) != "1990-04-22" || dateValue(time.End) != "2070-06-01" || after.Body != doc.Body || after.Metadata().CreatedAt != before.CreatedAt || !reflect.DeepEqual(after.Types(), doc.Types()) {
		t.Fatal(after, time)
	}
	_, err = s.SaveNamed(ctx, Entities, NamedInput{ID: doc.ID, Time: map[string]any{"end": nil}})
	if err != nil {
		t.Fatal(err)
	}
	// A completed retry cannot undo the subsequent explicit clear.
	if _, err = s.SaveNamed(ctx, Entities, NamedInput{Key: key, ID: doc.ID, Time: map[string]any{"end": "2070-06-01"}}); err != nil {
		t.Fatal(err)
	}
	after, _ = s.Read(doc.ID)
	time = after.Record(Entities).Time
	if time.End != nil || dateValue(time.Start) != "1990-04-22" {
		t.Fatal(time)
	}
	if _, err = s.SaveNamed(ctx, Entities, NamedInput{ID: doc.ID, Metadata: map[string]any{"time": nil}}); err != nil {
		t.Fatal(err)
	}
	after, _ = s.Read(doc.ID)
	raw, _ := after.Marshal()
	if after.Record(Entities).Time.hasDates() || strings.Contains(string(raw), "earliest:") || strings.Contains(string(raw), "date:") {
		t.Fatal(string(raw))
	}
}
func TestEntityTimeRejectsInvalidValuesAcrossEveryWriteBoundary(t *testing.T) {
	ctx := context.Background()
	for _, timing := range []any{map[string]any{"date": "1990-04-22"}, map[string]any{"earliest": "1990-04-22"}, map[string]any{"start": "next summer"}, map[string]any{"end": "2025-02-29"}, map[string]any{"start": "2025-04-02", "end": "2025-04-01"}, RecordTime{Lifetime: true, Date: textValue("1990-04-22")}, "1990-04-22"} {
		s, _ := Open(t.TempDir())
		if _, err := s.Create(Entities, map[string]any{"name": "Mary", "time": timing}, "Do not publish."); err == nil {
			t.Fatal("invalid Create", timing)
		}
		if _, err := s.SaveNamed(ctx, Entities, NamedInput{Create: true, Name: "Mary", Time: timing}); err == nil {
			t.Fatal("invalid MCP/domain write", timing)
		}
		doc, err := s.Create(Entities, map[string]any{"name": "Mary"}, "Intact.")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Update(doc.ID, doc.Revision, map[string]any{"time": timing}, "Do not overwrite."); err == nil {
			t.Fatal("invalid Update", timing)
		}
		node, _ := encodeValue(map[string]any{"name": "Mary", "time": timing})
		candidate := &Document{Frontmatter: *node, Body: doc.Body, Kind: Entities}
		raw, _ := candidate.Marshal()
		if _, err := s.SaveSource(ctx, "bad-time", doc.ID, Entities, doc.Revision, string(raw)); err == nil {
			t.Fatal("invalid source", timing)
		}
		after, _ := s.Read(doc.ID)
		if after.Revision != doc.Revision {
			t.Fatal("rejected lifetime changed stored data")
		}
	}
	s, _ := Open(t.TempDir())
	if _, err := s.SaveMemory(ctx, MemoryInput{User: "reader", Text: textValue("A narrated event."), Time: map[string]any{"start": "1990-04-22"}}); err == nil {
		t.Fatal("memory accepted entity lifetime keys")
	}
}
func TestEntityLifetimeSurvivesDerivedCacheAndKeepsMemoryJSONShape(t *testing.T) {
	s, _ := Open(t.TempDir())
	ctx := context.Background()
	entity, _ := s.Create(Entities, map[string]any{"name": "John"}, "Identification.")
	memory, _ := s.Create(Memories, map[string]any{"user": "reader", "time": map[string]any{"date": "2024-04-22"}}, "John and Mary celebrated.")
	for i := 0; i < 2; i++ {
		records, err := s.Scan(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range records.Records {
			raw, _ := json.Marshal(r.Time)
			var fields map[string]any
			_ = json.Unmarshal(raw, &fields)
			if r.ID == entity.ID {
				if len(fields) != 2 || !r.Time.Lifetime {
					t.Fatal("cached lifetime changed shape", string(raw))
				}
				if _, ok := fields["start"]; !ok {
					t.Fatal(fields)
				}
			}
			if r.ID == memory.ID {
				if len(fields) != 3 || fields["date"] != "2024-04-22" {
					t.Fatal(fields)
				}
			}
		}
	}
}
func TestEntityDateDamageStaysVisibleAndRepairKeepsHealthyComponent(t *testing.T) {
	s, _ := Open(t.TempDir())
	ctx := context.Background()
	doc, _ := s.Create(Entities, map[string]any{"name": "Mary"}, "Intact identification.")
	path := filepath.Join(s.Directory(), "entities", doc.ID+"/record.md")
	if err := fixtureWriteFile(path, []byte("---\nname: Mary\ntime: {start: long ago, end: '2024-06-01'}\n---\nIntact identification.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	doc, _ = s.Read(doc.ID)
	if doc.Record(Entities).Time.Start != nil || dateValue(doc.Record(Entities).Time.End) != "2024-06-01" || len(doc.Diagnostics()) == 0 {
		t.Fatal(doc)
	}
	changed, err := s.Update(doc.ID, doc.Revision, map[string]any{"name": "Mary Example"}, doc.Body)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := changed.Marshal()
	if !strings.Contains(string(raw), "long ago") {
		t.Fatal("unrelated edit discarded damaged date", string(raw))
	}

	if _, err := s.SaveNamed(ctx, Entities, NamedInput{ID: doc.ID, Time: map[string]any{"start": "1990-04-22"}}); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Read(doc.ID)
	raw, _ = after.Marshal()
	timing := after.Record(Entities).Time
	if dateValue(timing.Start) != "1990-04-22" || dateValue(timing.End) != "2024-06-01" || !strings.Contains(string(raw), "long ago") {
		t.Fatal("repair lost readable or raw data", string(raw), timing)
	}
	// Old entity date bounds remain source evidence; they are never relabelled as a lifetime.
	if err := fixtureWriteFile(path, []byte("---\nname: Mary\ntime: {earliest: '1990-04-01', latest: '1990-04-30'}\n---\nIntact identification.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	doc, _ = s.Read(doc.ID)
	changed, err = s.Update(doc.ID, doc.Revision, map[string]any{"name": "Mary Example"}, doc.Body)
	if err != nil || changed.Record(Entities).Time.hasDates() {
		t.Fatal("old uncertainty became a lifetime", changed, err)
	}
	raw, _ = changed.Marshal()
	if !strings.Contains(string(raw), "earliest:") {
		t.Fatal("old dates discarded", string(raw))
	}
}

func TestEntityLifetimeComponentsMergeWithinOneCapture(t *testing.T) {
	s, _ := Open(t.TempDir())
	ctx := context.Background()
	doc, err := s.Create(Entities, map[string]any{"name": "John"}, "Identification.")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.SaveMemory(ctx, MemoryInput{User: "reader", Text: textValue("John discussed the project."), Relations: []RelationInput{
		{Type: "related_to", Target: map[string]any{"id": doc.ID, "time": map[string]any{"start": "1990-04-22"}}},
		{Type: "related_to", Target: map[string]any{"id": doc.ID, "metadata": map[string]any{"time": map[string]any{"end": "2070-06-01"}}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	after, err := s.Read(doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	timing := after.Record(Entities).Time
	if dateValue(timing.Start) != "1990-04-22" || dateValue(timing.End) != "2070-06-01" || after.Body != doc.Body {
		t.Fatal("merged capture lost a lifetime component", timing, after.Body)
	}
}
