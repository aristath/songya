package web

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aristath/songya"
)

func TestGraphWeightBatchTTLAndEdits(t *testing.T) {
	store, _ := songya.Open(t.TempDir())
	app := &chatApp{store: store}
	w := newGraphWeights(app)
	ctx := context.Background()
	now := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	entity := weightRecord(songya.Entities, 1)
	memory := datedWeightMemory(1, "2026-11-09")
	other := weightRecord(songya.Topics, 1)
	entity.Relations = []songya.Relation{{Type: "related_to", Target: memory.ID}}
	catalog := songya.Catalog{Records: []songya.Record{entity, memory, other}}
	g := newWeightGraph(&catalog, now)
	w.update(ctx, g, now, true)
	for id, entry := range w.cache.Entries {
		if !entry.CalculatedAt.Equal(now) || entry.Pending {
			t.Fatalf("Cold start did not drain %s: %+v", id, entry)
		}
	}
	if w.update(ctx, g, now.Add(500*time.Millisecond), true) {
		t.Fatal("Fresh weights were recalculated")
	}
	fresh := weightRecord(songya.Topics, 2)
	catalog.Records = append(catalog.Records, fresh)
	addedAt := now.Add(600 * time.Millisecond)
	w.update(ctx, newWeightGraph(&catalog, now), addedAt, true)
	if !w.cache.Entries[fresh.ID].CalculatedAt.Equal(addedAt) {
		t.Fatal("New record waited for a rate-limit slot")
	}
	before := w.cache.Entries[entity.ID]
	w.update(ctx, newWeightGraph(&catalog, now), now.Add(23*time.Hour), true)
	if !w.cache.Entries[entity.ID].CalculatedAt.Equal(before.CalculatedAt) {
		t.Fatal("Fresh cache was recalculated")
	}
	catalog.Records[1].Revision = "edited"
	oldDate := "1990-01-01"
	catalog.Records[1].Time.Date = &oldDate
	g = newWeightGraph(&catalog, now)
	editAt := now.Add(23*time.Hour + 100*time.Millisecond)
	w.update(ctx, g, editAt, true)
	for _, id := range []string{entity.ID, memory.ID} {
		entry := w.cache.Entries[id]
		if !entry.CalculatedAt.Equal(editAt) || entry.Pending {
			t.Fatalf("Edit did not recalculate %s in the same pass", id)
		}
	}
	if w.cache.Entries[entity.ID].Weight >= before.Weight {
		t.Fatal("Dependent weight ignored the changed event date")
	}
	if !w.cache.Entries[other.ID].CalculatedAt.Equal(now) {
		t.Fatal("Unrelated fresh weight was recalculated")
	}
	// A saved unfinished pass resumes without a per-second delay after restart.
	entry := w.cache.Entries[entity.ID]
	entry.Pending = true
	w.cache.Entries[entity.ID] = entry
	data, _ := json.Marshal(w.cache)
	if err := os.WriteFile(filepath.Join(store.Directory(), graphWeightFile), data, 0600); err != nil {
		t.Fatal(err)
	}
	w = newGraphWeights(app)
	resumedAt := editAt.Add(100 * time.Millisecond)
	w.update(ctx, g, resumedAt, true)
	if w.cache.Entries[entity.ID].Pending || !w.cache.Entries[entity.ID].CalculatedAt.Equal(resumedAt) {
		t.Fatal("Restart did not finish pending work")
	}
	expiredAt := now.Add(49 * time.Hour)
	w.update(ctx, g, expiredAt, true)
	for id, entry := range w.cache.Entries {
		if !entry.CalculatedAt.Equal(expiredAt) || entry.Pending {
			t.Fatalf("Expired weight %s did not refresh", id)
		}
	}
}

func TestGraphWeightCancelledPassCanResume(t *testing.T) {
	store, _ := songya.Open(t.TempDir())
	w := newGraphWeights(&chatApp{store: store})
	now := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	records := []songya.Record{weightRecord(songya.Entities, 1), datedWeightMemory(1, "2026-11-09")}
	g := newWeightGraph(&songya.Catalog{Records: records}, now)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if w.update(ctx, g, now, true) {
		t.Fatal("Cancelled work was reported as calculated")
	}
	if len(w.cache.Entries) != len(records) {
		t.Fatal("Cancellation discarded queued records")
	}
	w.update(context.Background(), g, now, true)
	for id, entry := range w.cache.Entries {
		if entry.CalculatedAt.IsZero() || entry.Pending {
			t.Fatal("Resumed pass skipped", id)
		}
	}
}

func TestGraphWeightRemovedLinksInvalidateOldNeighbors(t *testing.T) {
	store, _ := songya.Open(t.TempDir())
	w := newGraphWeights(&chatApp{store: store})
	now := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	m := datedWeightMemory(1, "2026-11-09")
	e := weightRecord(songya.Entities, 1, songya.Relation{Type: "related_to", Target: m.ID})
	c := songya.Catalog{Records: []songya.Record{e, m}}
	w.update(context.Background(), newWeightGraph(&c, now), now, true)
	// Delete the memory. Its former neighbor must not keep an unmarked cached weight.
	c.Records = c.Records[:1]
	w.update(context.Background(), newWeightGraph(&c, now), now.Add(1100*time.Millisecond), true)
	if _, ok := w.cache.Entries[m.ID]; ok {
		t.Fatal("Deleted record retained")
	}
	if w.cache.Entries[e.ID].Pending || !w.cache.Entries[e.ID].CalculatedAt.Equal(now.Add(1100*time.Millisecond)) {
		t.Fatal("Former neighbor was not recalculated in the same pass")
	}
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
