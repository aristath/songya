package songya

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestColorsDefaultExistingChoicesAndPersistIndependently(t *testing.T) {
	store, _ := Open(t.TempDir())
	ctx := context.Background()
	cfg, _ := store.TypeConfiguration(ctx, "reader")
	raw, _ := os.ReadFile(filepath.Join(store.Directory(), typeConfigurationPath("reader")))
	var existing map[string]any
	_ = json.Unmarshal(raw, &existing)
	delete(existing, "kind_colors")
	for _, list := range existing["kinds"].(map[string]any) {
		for _, item := range list.([]any) {
			delete(item.(map[string]any), "color")
		}
	}
	legacy, _ := json.Marshal(existing)
	_ = fixtureWriteFile(filepath.Join(store.Directory(), typeConfigurationPath("reader")), legacy, 0600)
	cfg, err := store.TypeConfiguration(ctx, "reader")
	if err != nil || cfg.KindColor(Memories) != "peach" || cfg.Kinds[Entities][0].Color != "mauve" {
		t.Fatal(cfg, err)
	}
	afterRead, _ := os.ReadFile(filepath.Join(store.Directory(), typeConfigurationPath("reader")))
	if string(afterRead) != string(legacy) {
		t.Fatal("reading defaults rewrote saved settings")
	}
	doc, _ := store.Create(Entities, map[string]any{"name": "John", "types": map[string]string{"reader": "person", "other": "person"}}, "A short identification.")
	before, _ := store.Read(doc.ID)
	cfg.Kinds[Entities][0].Color = "#AbC"
	cfg.KindColors[Memories] = "blue"
	inputColor := cfg.Kinds[Entities][0].Color
	saved, err := store.SaveTypeConfiguration(ctx, cfg, cfg.Version, nil, "colors")
	if err != nil || saved.Status != "committed" {
		t.Fatal(saved, err)
	}
	if cfg.Kinds[Entities][0].Color != inputColor {
		t.Fatal("saving modified the caller's intent")
	}
	result, _ := store.TypeConfiguration(ctx, "reader")
	if result.Kinds[Entities][0].Color != "#aabbcc" || result.KindColor(Memories) != "blue" {
		t.Fatal(result)
	}
	other, _ := store.TypeConfiguration(ctx, "other")
	if other.Kinds[Entities][0].Color != "mauve" || other.KindColor(Memories) != "peach" {
		t.Fatal("colors leaked across users", other)
	}
	after, _ := store.Read(doc.ID)
	if before.Revision != after.Revision {
		t.Fatal("color changes rewrote a record")
	}
	if result.RecordColor(after.Record(Entities)) != "#aabbcc" || other.RecordColor(after.Record(Entities)) != "mauve" {
		t.Fatal("classification user's colors were not selected")
	}
	// Omitting colors during a definition edit keeps existing choices.
	for kind := range result.Kinds {
		for i := range result.Kinds[kind] {
			result.Kinds[kind][i].Color = ""
		}
	}
	result.KindColors = nil
	result.Kinds[Entities][0].Description = "A named individual."
	if _, err = store.SaveTypeConfiguration(ctx, result, result.Version, nil, "words-only"); err != nil {
		t.Fatal(err)
	}
	latest, _ := store.TypeConfiguration(ctx, "reader")
	if latest.Kinds[Entities][0].Color != "#aabbcc" || latest.KindColor(Memories) != "blue" {
		t.Fatal("omission cleared colors", latest)
	}
	if receipt, err := store.SaveTypeConfiguration(ctx, cfg, cfg.Version, nil, "colors"); err != nil || receipt.Status != "committed" {
		t.Fatal("accepted retry was lost", receipt, err)
	}
	latest, _ = store.TypeConfiguration(ctx, "reader")
	if latest.Kinds[Entities][0].Description != "A named individual." {
		t.Fatal("retry overwrote newer settings")
	}
}

func TestColorValidationAndDamagedColorsLeaveTypesUsable(t *testing.T) {
	store, _ := Open(t.TempDir())
	ctx := context.Background()
	cfg, _ := store.TypeConfiguration(ctx, "reader")
	for _, value := range []string{"crust", "mantle", "base", "javascript:red", "blue;display:none", "#gggggg"} {
		copy, _ := store.TypeConfiguration(ctx, "reader")
		copy.Kinds[Entities][0].Color = value
		if _, err := store.SaveTypeConfiguration(ctx, copy, copy.Version, nil, "bad-"+value); err == nil {
			t.Fatal("invalid color accepted", value)
		}
	}
	if NormalizeColor(" #ABC ") != "#aabbcc" || NormalizeColor("BLUE") != "blue" {
		t.Fatal("equivalent colors did not normalize")
	}
	raw, _ := json.Marshal(cfg)
	var data map[string]any
	_ = json.Unmarshal(raw, &data)
	defs := data["kinds"].(map[string]any)["entities"].([]any)
	defs[0].(map[string]any)["color"] = map[string]any{"unreadable": true}
	defs[1].(map[string]any)["color"] = "not-a-color"
	data["kind_colors"].(map[string]any)["stories"] = false
	damaged, _ := json.Marshal(data)
	_ = fixtureWriteFile(filepath.Join(store.Directory(), typeConfigurationPath("reader")), damaged, 0600)
	readable, err := store.TypeConfiguration(ctx, "reader")
	if err != nil || len(readable.Kinds[Entities]) != 12 || len(readable.Diagnostics) < 3 || readable.Kinds[Entities][0].Color != "mauve" {
		t.Fatal("a damaged color hid healthy types", readable, err)
	}
	readable.Kinds[Entities][0].Description = "A clear identification."
	if _, err = store.SaveTypeConfiguration(ctx, readable, readable.Version, nil, "healthy-definition"); err != nil {
		t.Fatal("an unrelated edit was blocked", err)
	}
	data["kind_colors"] = "damaged"
	damaged, _ = json.Marshal(data)
	_ = fixtureWriteFile(filepath.Join(store.Directory(), typeConfigurationPath("reader")), damaged, 0600)
	readable, err = store.TypeConfiguration(ctx, "reader")
	if err != nil || len(readable.Kinds[Entities]) != 12 || readable.KindColor(Memories) != "peach" {
		t.Fatal("damaged record colors hid healthy types", readable, err)
	}
}
