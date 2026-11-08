package songya

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestGeneratedUpdateSequencesPreserveIndependentData(t *testing.T) {
	random := rand.New(rand.NewSource(734))
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string]any{"user": "test", "title": "Start", "untouched": map[string]any{"literal": "000123"}, "time": map[string]any{"date": nil, "earliest": nil, "latest": nil}}
	doc, err := store.Create(Memories, testUserFields(Memories, expected), "Start body")
	if err != nil {
		t.Fatal(err)
	}
	created := doc.Metadata().CreatedAt
	name, _ := recordName(doc.ID)
	for iteration := 0; iteration < 250; iteration++ {
		previous := doc.Revision
		key := fmt.Sprintf("field%d", random.Intn(8))
		value := []any{nil, random.Intn(100), fmt.Sprintf("000%d", iteration), []any{"Keep", iteration}, map[string]any{"n": iteration, "enabled": false}}[random.Intn(5)]
		patch := map[string]any{key: value}
		body := make([]byte, random.Intn(512))
		_, _ = random.Read(body)
		updated, err := store.Update(doc.ID, doc.Revision, patch, string(body))
		if err != nil {
			t.Fatalf("step %d: %v", iteration, err)
		}
		expected[key] = value
		// The YAML library is an independent reference for ordinary data values.
		encoded, err := yaml.Marshal(expected)
		if err != nil {
			t.Fatal(err)
		}
		var want, actual map[string]any
		if err := yaml.Unmarshal(encoded, &want); err != nil {
			t.Fatal(err)
		}
		read, err := store.Read(doc.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := read.Frontmatter.Decode(&actual); err != nil {
			t.Fatal(err)
		}
		delete(actual, "created_at")
		delete(actual, "updated_at")
		if !reflect.DeepEqual(actual, want) || read.Body != string(body) || !read.Metadata().CreatedAt.Equal(created) || read.Revision != updated.Revision || read.ID != doc.ID {
			t.Fatalf("step %d changed independent content: want=%#v got=%#v", iteration, want, actual)
		}
		disk, err := os.ReadFile(filepath.Join(store.path, name))
		if err != nil {
			t.Fatal(err)
		}
		if updated.Revision != dataRevision(disk) {
			t.Fatal("returned revision does not identify committed bytes")
		}
		if _, err := store.Update(doc.ID, previous, nil, "Stale retry"); !errors.Is(err, ErrConflict) {
			t.Fatal("stale retry succeeded")
		}
		if iteration%13 == 0 {
			edited := append(append([]byte(nil), disk...), []byte("User edit")...)
			if err := fixtureWriteFile(filepath.Join(store.path, name), edited, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Update(doc.ID, read.Revision, nil, "Stale edit"); !errors.Is(err, ErrConflict) {
				t.Fatal("external edit was overwritten")
			}
			after, err := os.ReadFile(filepath.Join(store.path, name))
			if err != nil || !bytes.Equal(after, edited) {
				t.Fatal("conflict changed the current file")
			}
			read, err = store.Read(doc.ID)
			if err != nil {
				t.Fatal(err)
			}
		}
		doc = read
		if iteration%17 == 0 {
			store, err = Open(store.path)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestUpdatesRetainUnreadableHeadersAndBodies(t *testing.T) {
	for _, source := range []string{"---\ncreated_at: [unfinished\n---\nBody\r\n", "---\nUnclosed header\r\n", "---\ncreated_at: !!timestamp unknown\ncustom: Keep\n---\nBody"} {
		store, err := Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		id := "m_000000000000"
		name, _ := recordName(id)
		if err := fixtureWriteFile(filepath.Join(store.path, name), []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
		original, err := store.Read(id)
		if err != nil {
			t.Fatal(err)
		}
		updated, err := store.Update(id, original.Revision, map[string]any{"user": "test", "title": 3}, original.Body)
		if err != nil {
			t.Fatal(err)
		}
		read, err := store.Read(id)
		if err != nil {
			t.Fatal(err)
		}
		if read.Body != original.Body || read.Metadata().Title != "3" || read.Revision != updated.Revision || len(read.Diagnostics()) == 0 {
			t.Fatal("damaged header disabled useful edits")
		}
		if before := field(&original.Frontmatter, "unparsed_frontmatter"); before != nil {
			var want, got string
			if err := before.Decode(&want); err != nil {
				t.Fatal(err)
			}
			if err := field(&read.Frontmatter, "unparsed_frontmatter").Decode(&got); err != nil || got != want {
				t.Fatal("damaged frontmatter was lost")
			}
		}
		if before := field(&original.Frontmatter, "created_at"); before != nil {
			after := field(&read.Frontmatter, "created_at")
			if after == nil || after.Value != before.Value || after.Tag != before.Tag || after.Style != before.Style {
				t.Fatal("damaged creation representation changed")
			}
		}
	}
}
