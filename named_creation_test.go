package songya

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestNamedCreationNeverChangesAnExistingSameNameRecord(t *testing.T) {
	for _, kind := range []Destination{Entities, Topics} {
		t.Run(string(kind), func(t *testing.T) {
			store, _ := Open(t.TempDir())
			ctx := context.Background()
			input := NamedInput{Key: "first", Create: true, Name: "Alex", Description: textValue("Sam's cousin.")}
			first, err := store.SaveNamed(ctx, kind, input)
			if err != nil {
				t.Fatal(err)
			}
			id := first.Refs["record"]
			file := filepath.Join(store.path, string(kind), id+"/record.md")
			before, _ := os.ReadFile(file)
			secondInput := NamedInput{Key: "second", Create: true, Name: "Alex", Description: textValue("Sam's coworker, a different person.")}
			second, err := store.SaveNamed(ctx, kind, secondInput)
			if err != nil || second.Refs["record"] == id {
				t.Fatal("distinct creation reused a name", second, err)
			}
			after, _ := os.ReadFile(file)
			if string(after) != string(before) {
				t.Fatal("new creation changed an existing record")
			}
			retry, err := store.SaveNamed(ctx, kind, secondInput)
			if err != nil || retry.Refs["record"] != second.Refs["record"] {
				t.Fatal("retry created another identity", retry, err)
			}
			for _, bad := range []NamedInput{{Name: "Alex", Description: textValue("Wrong overwrite")}, {Create: true, ID: id, Name: "Alex"}} {
				if _, err := store.SaveNamed(ctx, kind, bad); err == nil {
					t.Fatal("ambiguous create/update was accepted", bad)
				}
			}
			update, err := store.SaveNamed(ctx, kind, NamedInput{ID: id, Description: textValue("Sam's cousin, corrected by ID.")})
			if err != nil || update.Refs["record"] != id {
				t.Fatal(update, err)
			}
			other, _ := store.Read(second.Refs["record"])
			if other.Body != *secondInput.Description {
				t.Fatal("ID update changed a different person", other.Body)
			}
		})
	}
}

func TestInlineExplicitCreationAndIDUpdatesProtectExistingIdentity(t *testing.T) {
	store, _ := Open(t.TempDir())
	ctx := context.Background()
	first, err := store.SaveNamed(ctx, Entities, NamedInput{Create: true, Name: "Alex", Description: textValue("Sam's cousin.")})
	if err != nil {
		t.Fatal(err)
	}
	id := first.Refs["record"]
	nameOnly, err := store.SaveMemory(ctx, MemoryInput{User: "sam", Text: textValue("A useful account."), Relations: []RelationInput{{Type: "related_to", Target: map[string]any{"name": "Alex", "description": "Must not replace or extend the cousin by name"}}}})
	if err != nil {
		t.Fatal(err)
	}
	unresolved, _ := store.Read(nameOnly.Refs["memory"])
	if len(unresolved.Diagnostics()) == 0 {
		t.Fatal("unsafe name-only enrichment was not reported")
	}
	unchanged, _ := store.Read(id)
	if unchanged.Body != "Sam's cousin." {
		t.Fatal("unique-name lookup mutated the cousin", unchanged.Body)
	}
	created, err := store.SaveMemory(ctx, MemoryInput{User: "sam", Text: textValue("Lunch with coworker Alex"), Relations: []RelationInput{{Type: "related_to", Target: map[string]any{"kind": "entities", "create": true, "name": "Alex", "description": "Sam's coworker."}}}})
	if err != nil {
		t.Fatal(err)
	}
	memory, _ := store.Read(created.Refs["memory"])
	if len(memory.Metadata().Relations) != 1 || memory.Metadata().Relations[0].Target == id {
		t.Fatal("inline create reused a same-name entity", memory.Metadata())
	}
	before, _ := store.Read(id)
	if before.Body != "Sam's cousin." {
		t.Fatal("inline creation overwrote the cousin", before.Body)
	}
	// A name-only attempt to enrich an identity must retain the account without
	// choosing which Alex to change. An explicit ID can make that update safely.
	_, err = store.SaveMemory(ctx, MemoryInput{User: "sam", Text: textValue("A clear account remains useful."), Relations: []RelationInput{{Type: "related_to", Target: map[string]any{"name": "Alex", "description": "Wrong automatic enrichment"}}}})
	if err != nil {
		t.Fatal(err)
	}
	after, _ := store.Read(id)
	if after.Body != before.Body {
		t.Fatal("name-only update changed an identity")
	}
}
