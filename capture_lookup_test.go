package songya

import (
	"context"
	"fmt"
	"os"
	"testing"
)

func TestNamedRelationLookupUsesTheOwnedWriteSnapshot(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	entity, err := s.SaveNamed(ctx, Entities, NamedInput{Key: "person", Create: true, Name: "Alex Morgan", Aliases: []string{"Alex"}, Description: textValue("A person")})
	if err != nil {
		t.Fatal(err)
	}
	id := entity.Refs["record"]
	for _, target := range []string{id, "Alex Morgan", "Alex"} {
		receipt, err := s.SaveMemory(ctx, MemoryInput{Key: "lookup-" + target, User: "ari", Text: textValue("Met Alex"), Relations: []RelationInput{{Type: "related_to", Target: target}}})
		if err != nil || receipt.Status != "committed" {
			t.Fatal(receipt, err)
		}
		doc, err := s.Read(receipt.Refs["memory"])
		if err != nil || len(doc.Metadata().Relations) != 1 || doc.Metadata().Relations[0].Target != id {
			t.Fatal(doc, err)
		}
	}
	_, err = s.SaveNamed(ctx, Entities, NamedInput{Key: "other", Create: true, Name: "Alex Morgan", Description: textValue("Another person")})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := s.SaveMemory(ctx, MemoryInput{Key: "ambiguous", User: "ari", Text: textValue("Met someone"), Relations: []RelationInput{{Type: "related_to", Target: "Alex Morgan"}}})
	if err != nil || receipt.Status != "committed" {
		t.Fatal("ambiguous account was lost", receipt, err)
	}
	doc, _ := s.Read(receipt.Refs["memory"])
	if len(doc.Metadata().Relations) != 0 || len(doc.Diagnostics()) == 0 {
		t.Fatal("ambiguous identity resolved arbitrarily", doc)
	}
}
func BenchmarkNamedLookupWithWriteHistory(b *testing.B) {
	s, err := Open(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	for i := range 32 {
		if _, err := s.SaveNamed(ctx, Entities, NamedInput{Key: fmt.Sprint("person", i), Create: true, Name: fmt.Sprint("Person ", i), Description: textValue("A person")}); err != nil {
			b.Fatal(err)
		}
	}
	for i := range 128 {
		if _, err := s.SaveMemory(ctx, MemoryInput{Key: fmt.Sprint("history", i), User: "ari", Text: textValue("A memory")}); err != nil {
			b.Fatal(err)
		}
	}
	b.ResetTimer()
	for range b.N {
		err := s.lockedContext(ctx, func(root *os.Root) error {
			doc, err := findNamed(ctx, root, Entities, NamedInput{Name: "Person 16", User: "ari"})
			if err == nil && doc == nil {
				return fmt.Errorf("identity missing")
			}
			return err
		})
		if err != nil {
			b.Fatal(err)
		}
	}
}
