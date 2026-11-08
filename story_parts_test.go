package songya

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
)

func TestComposedStoryPartsRetainRecordsAndOccurrencesAcrossPages(t *testing.T) {
	store, _ := Open(t.TempDir())
	create := func(kind Destination, fields map[string]any, body string) *Document {
		t.Helper()
		doc, err := store.Create(kind, fields, body)
		if err != nil {
			t.Fatal(err)
		}
		return doc
	}
	first := create(Memories, map[string]any{"user": "reader", "title": "First"}, strings.Repeat("Mary remembers αβ. ", 12))
	second := create(Memories, map[string]any{"user": "reader", "title": "Second"}, "John remembers the next day.")
	empty := create(Memories, map[string]any{"user": "reader", "title": "Empty account"}, "")
	entity := create(Entities, map[string]any{"name": "Mary"}, "This entity description is not a story passage.")
	nested := create(Stories, map[string]any{"user": "reader", "title": "Nested", "relations": []Relation{{"related_to", second.ID}, {"related_to", first.ID}}}, "Nested introduction.")
	missing := "m_000000000000"
	root := create(Stories, map[string]any{"user": "reader", "title": "A trip", "relations": []Relation{{"related_to", entity.ID}, {"related_to", first.ID}, {"related_to", empty.ID}, {"related_to", nested.ID}, {"related_to", missing}}}, "Trip introduction.")
	wantIDs := []string{root.ID, first.ID, empty.ID, nested.ID, second.ID, first.ID, missing}
	wantText := []string{root.Body, first.Body, empty.Body, nested.Body, second.Body, first.Body, "[[" + missing + "]] [missing record]\n"}
	for _, limit := range []int{1, 7, 97, 16384} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			var ids, texts []string
			var starts []int
			for offset := 0; ; {
				flat, err := store.Compose(context.Background(), root.ID, offset, limit)
				if err != nil {
					t.Fatal(err)
				}
				page, err := store.ComposeParts(context.Background(), root.ID, offset, limit)
				if err != nil {
					t.Fatal(err)
				}
				if flat.Offset != page.Offset || flat.Next != page.Next || flat.Truncated != page.Truncated {
					t.Fatal("reader changed composition paging", flat, page)
				}
				if page.Text != "" || page.Base64 != "" {
					t.Fatal("reader duplicated flat prose")
				}
				for _, part := range page.Parts {
					text := part.Text
					if part.Base64 != "" {
						b, err := base64.StdEncoding.DecodeString(part.Base64)
						if err != nil {
							t.Fatal(err)
						}
						text = string(b)
					}
					i := len(ids) - 1
					if i < 0 || ids[i] != part.ID || starts[i] != part.Start {
						ids = append(ids, part.ID)
						starts = append(starts, part.Start)
						texts = append(texts, "")
						i++
					}
					texts[i] += text
				}
				if !page.Truncated {
					break
				}
				if page.Next <= offset {
					t.Fatal("paging made no progress")
				}
				offset = page.Next
			}
			if strings.Join(ids, ",") != strings.Join(wantIDs, ",") {
				t.Fatal("record order/identity lost", ids, wantIDs)
			}
			for i := range texts {
				if texts[i] != wantText[i] {
					t.Fatalf("part %d changed: %q != %q", i, texts[i], wantText[i])
				}
			}
			if starts[1] == starts[5] {
				t.Fatal("repeated memory occurrences collapsed")
			}
		})
	}
	read, _ := store.Read(root.ID)
	if read.Revision != root.Revision {
		t.Fatal("composition rewrote the story")
	}
}

func TestStoryPartsKeepDamagedBytesAndHealthyAccountsBesideACycle(t *testing.T) {
	store, _ := Open(t.TempDir())
	damaged := createRecord(t, store, Memories, nil, "Mary remembers \xff and λ.")
	healthy := createRecord(t, store, Memories, nil, "John remembers the next morning.")
	root := createRecord(t, store, Stories, nil, "")
	_, err := store.Update(root.ID, root.Revision, map[string]any{"relations": []Relation{{"related_to", damaged.ID}, {"related_to", root.ID}, {"related_to", healthy.ID}}}, root.Body)
	if err != nil {
		t.Fatal(err)
	}
	bodies := map[string]string{}
	var diagnostics []Diagnostic
	for offset := 0; ; {
		page, err := store.ComposeParts(context.Background(), root.ID, offset, 4)
		if err != nil {
			t.Fatal(err)
		}
		diagnostics = append(diagnostics, page.Diagnostics...)
		for _, part := range page.Parts {
			text := part.Text
			if part.Base64 != "" {
				bytes, err := base64.StdEncoding.DecodeString(part.Base64)
				if err != nil {
					t.Fatal(err)
				}
				text = string(bytes)
			}
			bodies[part.ID] += text
		}
		if !page.Truncated {
			break
		}
		offset = page.Next
	}
	if bodies[damaged.ID] != damaged.Body || bodies[healthy.ID] != healthy.Body || !strings.Contains(bodies[root.ID], "recursive reference") || len(diagnostics) == 0 {
		t.Fatal("a damaged member or loop hid another account", bodies, diagnostics)
	}
}
