package web

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aristath/songya"
)

func TestGraphWeightQueueRateTTLAndImmediateEdits(t *testing.T) {
	store, _ := songya.Open(t.TempDir())
	a := &chatApp{store: store}
	w := newGraphWeights(a)
	now := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	e := weightRecord(songya.Entities, 1)
	m := datedWeightMemory(1, "2026-11-09")
	other := weightRecord(songya.Topics, 1)
	e.Relations = []songya.Relation{{Type: "related_to", Target: m.ID}}
	catalog := songya.Catalog{Records: []songya.Record{e, m, other}}
	g := newWeightGraph(&catalog, now)
	w.update(g, now, true)
	ready := func() int {
		n := 0
		for _, v := range w.cache.Entries {
			if !v.CalculatedAt.IsZero() {
				n++
			}
		}
		return n
	}
	if ready() != 1 {
		t.Fatal("Cold start must calculate only one record", ready())
	}
	w.update(g, now.Add(500*time.Millisecond), true)
	if ready() != 1 {
		t.Fatal("Exceeded one background calculation per second")
	}
	// New records join the queue rather than bypassing the rate limit.
	fresh := weightRecord(songya.Topics, 2)
	extended := songya.Catalog{Records: append(append([]songya.Record{}, catalog.Records...), fresh)}
	w.update(newWeightGraph(&extended, now), now.Add(600*time.Millisecond), true)
	if !w.cache.Entries[fresh.ID].CalculatedAt.IsZero() {
		t.Fatal("New record bypassed background rate limit")
	}
	// Keep the rest of this fixture at three records.
	delete(w.cache.Entries, fresh.ID)
	w.update(g, now.Add(time.Second), true)
	w.update(g, now.Add(2*time.Second), true)
	if ready() != 3 {
		t.Fatal("Never-calculated work was skipped")
	}
	before := w.cache.Entries[e.ID]
	w.update(g, now.Add(23*time.Hour), true)
	if !w.cache.Entries[e.ID].CalculatedAt.Equal(before.CalculatedAt) {
		t.Fatal("Fresh cache was recalculated")
	}
	// An external memory edit bypasses a just-used background slot; its neighbor is queued.
	w.cache.LastBackground = now.Add(23 * time.Hour)
	catalog.Records[1].Revision = "edited"
	oldDate := "1990-01-01"
	catalog.Records[1].Time.Date = &oldDate
	g = newWeightGraph(&catalog, now)
	editAt := now.Add(23*time.Hour + 100*time.Millisecond)
	w.update(g, editAt, true)
	if !w.cache.Entries[m.ID].CalculatedAt.Equal(editAt) || !w.cache.Entries[e.ID].Pending {
		t.Fatal("Edit did not recalculate immediately and queue dependants", w.cache)
	}
	// Restart must retain pending work and the background rate limit.
	data, _ := json.Marshal(w.cache)
	if err := os.WriteFile(filepath.Join(store.Directory(), graphWeightFile), data, 0600); err != nil {
		t.Fatal(err)
	}
	w = newGraphWeights(a)
	w.update(g, editAt.Add(100*time.Millisecond), true)
	if !w.cache.Entries[e.ID].Pending {
		t.Fatal("Restart bypassed the background rate limit")
	}
	w.update(g, editAt.Add(time.Second), true)
	if w.cache.Entries[e.ID].Pending || w.cache.Entries[e.ID].Weight >= before.Weight {
		t.Fatal("Affected record did not reflect the older event date", w.cache.Entries[e.ID])
	}
	w.update(g, now.Add(49*time.Hour), true)
	if !w.cache.Entries[other.ID].CalculatedAt.After(now.Add(48 * time.Hour)) {
		t.Fatal("Oldest expired record was not refreshed", w.cache)
	}
}

func TestGraphWeightRemovedLinksInvalidateOldNeighbors(t *testing.T) {
	store, _ := songya.Open(t.TempDir())
	w := newGraphWeights(&chatApp{store: store})
	now := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	m := datedWeightMemory(1, "2026-11-09")
	e := weightRecord(songya.Entities, 1, songya.Relation{Type: "related_to", Target: m.ID})
	c := songya.Catalog{Records: []songya.Record{e, m}}
	w.update(newWeightGraph(&c, now), now, true)
	w.update(newWeightGraph(&c, now), now.Add(time.Second), true)
	// Delete the memory. Its former neighbor must not keep an unmarked cached weight.
	c.Records = c.Records[:1]
	w.update(newWeightGraph(&c, now), now.Add(1100*time.Millisecond), true)
	if _, ok := w.cache.Entries[m.ID]; ok {
		t.Fatal("Deleted record retained")
	}
	if !w.cache.Entries[e.ID].Pending {
		t.Fatal("Former neighbor was not invalidated")
	}
	w.update(newWeightGraph(&c, now), now.Add(2*time.Second), true)
	near(t, w.cache.Entries[e.ID].Weight, 0)
}

func TestGraphWeightCacheWriteFailureRecoversWithoutBlockingRecords(t *testing.T) {
	store, _ := songya.Open(t.TempDir())
	record, err := store.Create(songya.Memories, map[string]any{"user": "reader", "title": "Mary's afternoon"}, "Keep these words.")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.Directory(), graphWeightFile)
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	app := Handler(store)
	defer app.Close()
	wait := func(check func() bool) {
		t.Helper()
		until := time.Now().Add(5 * time.Second)
		for time.Now().Before(until) {
			if check() {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("Weight worker did not recover")
	}
	wait(func() bool {
		_, status := app.chat.weights.snapshot()
		return status["status"] == "Graph weights could not be saved; retrying."
	})
	code, opened := call(t, app, "GET", "/api/records/"+record.ID, nil)
	if code != 200 || opened["source"] == "" {
		t.Fatal("Cache failure blocked record access")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	wait(func() bool {
		b, e := os.ReadFile(path)
		var c graphWeightCache
		return e == nil && json.Unmarshal(b, &c) == nil && !c.Entries[record.ID].CalculatedAt.IsZero()
	})
	app.Close()
	loaded := newGraphWeights(&chatApp{store: store})
	if loaded.cache.Entries[record.ID].CalculatedAt.IsZero() {
		t.Fatal("Restart lost calculated weights")
	}
	// One damaged entry must not discard the other entries.
	b, _ := json.Marshal(loaded.cache.Entries[record.ID])
	bad := []byte(`{"version":1,"records":{"` + record.ID + `":` + string(b) + `,"e_000000000001":{"weight":"broken"}}}`)
	if err := os.WriteFile(path, bad, 0600); err != nil {
		t.Fatal(err)
	}
	loaded = newGraphWeights(&chatApp{store: store})
	if len(loaded.cache.Entries) != 1 || loaded.cache.Entries[record.ID].CalculatedAt.IsZero() {
		t.Fatal("Bad cache entry discarded healthy data")
	}
}

func TestGraphWeightsObserveExternalClientEdits(t *testing.T) {
	store, _ := songya.Open(t.TempDir())
	entity, err := store.Create(songya.Entities, map[string]any{"name": "John"}, "")
	if err != nil {
		t.Fatal(err)
	}
	memory, err := store.Create(songya.Memories, map[string]any{"user": "reader", "title": "A recent walk", "time": map[string]any{"date": time.Now().UTC().Format(time.DateOnly)}, "relations": []songya.Relation{{Type: "related_to", Target: entity.ID}}}, "Keep the account.")
	if err != nil {
		t.Fatal(err)
	}
	app := Handler(store)
	defer app.Close()
	wait := func(check func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if check() {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("External change did not reach graph weights")
	}
	wait(func() bool { entries, _ := app.chat.weights.snapshot(); return entries[entity.ID].Weight == .5 })
	before, _ := app.chat.weights.snapshot()
	// Separate Store instance, without an HTTP notification, as with an MCP client.
	client, _ := songya.Open(store.Directory())
	_, err = client.Update(memory.ID, memory.Revision, map[string]any{"time": map[string]any{"date": "1990-01-01"}}, "Keep the account.")
	if err != nil {
		t.Fatal(err)
	}
	wait(func() bool {
		entries, _ := app.chat.weights.snapshot()
		return entries[entity.ID].Weight > 0 && entries[entity.ID].Weight < .2 && entries[memory.ID].CalculatedAt.After(before[memory.ID].CalculatedAt)
	})
}
