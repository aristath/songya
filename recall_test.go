package songya

import (
	"context"
	"encoding/base64"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRecallConnectedNarratives(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	create := func(kind Destination, fields map[string]any, body string) *Document {
		t.Helper()
		d, err := s.Create(kind, fields, body)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	e := create(Entities, map[string]any{"name": "Dimitra Sayas", "aliases": []string{"Dimitra"}}, "A musician.")
	earlier := create(Memories, map[string]any{"user": "ari", "title": "Earlier context"}, "Ari first tried to reach Athanasia.")
	a := create(Memories, map[string]any{"user": "ari", "title": "How they met", "time": map[string]any{"earliest": "2000-01-01", "latest": "2004-12-31"}, "emotions": []string{"hope with uncertainty"}, "relations": []Relation{{Type: "related_to", Target: e.ID}}}, "In the early 2000s, he messaged Dimitra instead; he hoped they would stay together.")
	story := create(Stories, map[string]any{"user": "ari", "title": "Their relationship", "relations": []Relation{{"related_to", earlier.ID}, {"related_to", a.ID}}}, "Before and after")
	b := create(Memories, map[string]any{"user": "ari", "title": "Later reflection", "relations": []Relation{{Type: "related_to", Target: e.ID}}}, "Years later the breakup still hurt him.")
	create(Stories, map[string]any{"user": "ari", "title": "Later life", "relations": []Relation{{"related_to", b.ID}}}, "")
	create(Stories, map[string]any{"user": "ari", "title": "A wider narrative", "relations": []Relation{{"related_to", story.ID}}}, "Outer context")
	other := create(Memories, map[string]any{"user": "clara", "relations": []Relation{{Type: "related_to", Target: e.ID}}}, "Clara has her own perspective.")
	// Intact prose remains useful even when frontmatter needs repair.
	damaged := create(Memories, map[string]any{"user": "ari", "title": "Dimitra damaged account"}, "Retained uncertain account.")
	name := filepath.Join(s.path, "memories", damaged.ID+"/record.md")
	if err := fixtureWriteFile(name, []byte("---\nuser: ari\ntitle: Dimitra damaged account\nbroken: [\n---\nRetained uncertain account."), 0600); err != nil {
		t.Fatal(err)
	}
	q := RecallQuery{Text: "Who was Dimitra?", User: "ari"}
	page, err := s.Recall(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{e.ID, earlier.ID, story.ID, a.ID, b.ID, "early 2000s", "hope with uncertainty", "Athanasia", "breakup still hurt", "Outer context"} {
		if !strings.Contains(page.Text, want) {
			t.Fatalf("missing %q: %s", want, page.Text)
		}
	}
	if page.Truncated || strings.Contains(page.Text, other.Body) {
		t.Fatal("unexpected scope or truncation", page)
	}
	q.User = "all"
	all, err := s.Recall(context.Background(), q)
	if err != nil || !strings.Contains(all.Text, other.Body) {
		t.Fatal("all lost another user's memory", err, all)
	}
	// Subject fallback reaches malformed metadata and reports it rather than hiding prose.
	p, err := s.Recall(context.Background(), RecallQuery{Text: "Retained uncertain", User: "all"})
	if err != nil || !strings.Contains(p.Text, "Retained uncertain account.") || len(p.Diagnostics) == 0 {
		t.Fatal("damaged account hidden", err, p)
	}
}

func TestRecallPaginationPreservesBytes(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{strings.Repeat("α😀 quoted \"line\"\n", 5000), "prefix" + string([]byte{0xff, 0xfe}) + "suffix"} {
		d, err := s.Create(Memories, map[string]any{"user": "ari", "title": "Paged account"}, body)
		if err != nil {
			t.Fatal(err)
		}
		text := ""
		offset := 0
		for calls := 0; calls < 5000; calls++ {
			p, err := s.Recall(context.Background(), RecallQuery{Text: d.ID, Limit: 113, Offset: offset})
			if err != nil {
				t.Fatal(err)
			}
			if p.Base64 != "" {
				b, err := base64.StdEncoding.DecodeString(p.Base64)
				if err != nil {
					t.Fatal(err)
				}
				text += string(b)
			} else {
				if !utf8.ValidString(p.Text) {
					t.Fatal("broken UTF-8")
				}
				text += p.Text
			}
			if !p.Truncated {
				break
			}
			if p.Next <= offset {
				t.Fatal("continuation did not advance", p)
			}
			offset = p.Next
		}
		if !strings.HasSuffix(text, body) {
			t.Fatal("paging lost or duplicated prose", len(text), len(body))
		}
	}
}

func TestRecallAmbiguityMissingReferencesAndRetiredMembers(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	create := func(kind Destination, fields map[string]any, body string) *Document {
		t.Helper()
		d, err := s.Create(kind, fields, body)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	for _, description := range []string{"A cellist.", "A neighbour."} {
		e := create(Entities, map[string]any{"name": "Dimitra"}, description)
		create(Memories, map[string]any{"user": "ari", "relations": []Relation{{Type: "related_to", Target: e.ID}}}, description+" Separate account.")
	}
	replacement := create(Memories, map[string]any{"user": "ari"}, "The corrected account.")
	retired := create(Memories, map[string]any{"user": "ari", "redirects": []string{replacement.ID}}, "The original account.")
	story := create(Stories, map[string]any{"user": "ari", "relations": []Relation{{"related_to", retired.ID}, {"related_to", "m_000000000000"}}}, "Healthy story")
	for text, wants := range map[string][]string{"Dimitra": {"A cellist.", "A neighbour."}, story.ID: {retired.ID, replacement.ID, "The corrected account.", "record unavailable"}} {
		page, err := s.Recall(context.Background(), RecallQuery{Text: text, User: "ari"})
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range wants {
			if !strings.Contains(page.Text, want) {
				t.Fatalf("missing %q: %s", want, page.Text)
			}
		}
		if text == story.ID && len(page.Diagnostics) == 0 {
			t.Fatal("missing reference was not diagnosed")
		}
	}
}

func TestRecallKeepsMemoryAndSemanticMetadataWithoutDuplicatingStorageAccounts(t *testing.T) {
	store, _ := Open(t.TempDir())
	memory := createRecord(t, store, Memories, map[string]any{"emotions": []string{"hope"}, "original_accounts": []string{"RAW_STORAGE_ACCOUNT"}}, "The full memory.")
	page, err := store.Recall(context.Background(), RecallQuery{Text: memory.ID})
	if err != nil || !strings.Contains(page.Text, "The full memory.") || !strings.Contains(page.Text, "hope") || strings.Contains(page.Text, "RAW_STORAGE_ACCOUNT") {
		t.Fatal("recall omitted meaning or included storage provenance", page, err)
	}
	doc, _ := store.Read(memory.ID)
	if len(nodeStrings("original_accounts", doc)) != 1 {
		t.Fatal("projection changed stored account")
	}
}
