package songya

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"testing"

	"github.com/gofrs/flock"
	"go.yaml.in/yaml/v3"
)

type selfReturning int

func (value selfReturning) MarshalYAML() (any, error) { return value, nil }

type scalarEncoder int

func (value scalarEncoder) MarshalYAML() (any, error) { return int(value), nil }

type embeddedCycle struct{ Next *embeddedPayload }
type embeddedPayload struct {
	embeddedCycle `yaml:",inline"`
	Name          string
}

type encodedMap map[string]any

func (value encodedMap) MarshalYAML() (any, error) { return map[string]any(value), nil }

type failingMap map[string]any

func (failingMap) MarshalYAML() (any, error) { return nil, errors.New("custom encoder failed") }

type omittedCycle struct {
	Next  *omittedCycle
	Calls int
}

func (value *omittedCycle) IsZero() bool { value.Calls++; return true }

type countedEncoder struct{ Calls int }

func (value *countedEncoder) MarshalYAML() (any, error) {
	value.Calls++
	if value.Calls != 1 {
		return nil, errors.New("custom encoder ran more than once")
	}
	return map[string]any{"value": "Keep"}, nil
}

func TestCyclicMetadataStaysIsolated(t *testing.T) {
	if output, err := helperProcess(t, "cyclic-metadata", t.TempDir()).CombinedOutput(); err != nil {
		if len(output) > 1000 {
			output = output[:1000]
		}
		t.Fatalf("cyclic metadata killed the writer: %v\n%s", err, output)
	}
}

func testCyclicMetadata(t *testing.T, path string) {
	debug.SetMaxStack(512 << 10) // An unbounded encoder must only kill this child.
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	cycle := map[string]any{"preserved_elsewhere": "Value"}
	cycle["self"] = cycle
	slice := make([]any, 1)
	slice[0] = slice
	type recursive struct{ Next *recursive }
	pointer := &recursive{}
	pointer.Next = pointer
	embedded := &embeddedPayload{Name: "Healthy"}
	embedded.Next = embedded
	customMap := encodedMap{"healthy": "Keep"}
	customMap["self"] = customMap
	failed := failingMap{}
	failed["self"] = failed
	for _, value := range []any{cycle, slice, pointer, embedded, customMap, failed} {
		doc, err := store.Create(Memories, testUserFields(Memories, map[string]any{"title": 3, "cyclic": value}), "Healthy body")
		if err != nil {
			t.Fatal(err)
		}
		read, err := store.Read(doc.ID)
		if err != nil || read.Body != "Healthy body" || read.Metadata().Title != "3" {
			t.Fatalf("cyclic field disabled healthy content: %v", err)
		}
		if len(doc.Diagnostics()) == 0 || field(&read.Frontmatter, "cyclic") == nil {
			t.Fatal("cyclic field was silently lost")
		}
	}
	// Sharing a value is not a cycle. Shorter slices can share the same start.
	shared := map[string]any{"value": "Keep"}
	slices := []any{"Keep", nil}
	slices[1] = slices[:1]
	doc, err := store.Create(Topics, testUserFields(Topics, map[string]any{
		"shared": []any{shared, shared}, "slices": slices,
		"scalars": []any{scalarEncoder(3), selfReturning(3)},
	}), "Still working")
	if err != nil || len(doc.Diagnostics()) != 0 {
		t.Fatalf("ordinary shared values were rejected: %v", err)
	}
	parsed := Parse([]byte("---\nvalue: &value [*value]\n---\nBody"))
	if err := parsed.Set("copy", *field(&parsed.Frontmatter, "value")); err != nil {
		t.Fatalf("valid YAML recursion was rejected: %v", err)
	}
}

func TestGoConversionStaysOutsideCore(t *testing.T) {
	optional := &omittedCycle{}
	optional.Next = optional
	custom := &countedEncoder{}
	value := struct {
		Name     string
		Optional *omittedCycle `yaml:"optional,omitempty"`
		Custom   *countedEncoder
	}{Name: "Healthy name", Optional: optional, Custom: custom}
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := store.Create(Memories, testUserFields(Memories, map[string]any{"payload": value, "fact": "Keep"}), "Healthy body")
	if err != nil || len(raw.Diagnostics()) == 0 || optional.Calls != 0 || custom.Calls != 0 {
		t.Fatalf("core invoked Go callbacks or hid an unsupported object: %v", err)
	}
	var converted yaml.Node
	if err := converted.Encode(value); err != nil {
		t.Fatal(err)
	}
	doc, err := store.Create(Memories, testUserFields(Memories, map[string]any{"payload": converted}), "Healthy body")
	if err != nil || len(doc.Diagnostics()) != 0 {
		t.Fatalf("YAML's omitted field disabled healthy metadata: %v, %v", err, doc.Diagnostics())
	}
	read, err := store.Read(doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	var fields struct {
		Payload struct {
			Name     string
			Optional any
			Custom   map[string]string
		}
	}
	if err := read.Frontmatter.Decode(&fields); err != nil {
		t.Fatal(err)
	}
	if fields.Payload.Name != value.Name || fields.Payload.Optional != nil || fields.Payload.Custom["value"] != "Keep" {
		t.Fatalf("healthy fields were replaced: %+v", fields.Payload)
	}
	if optional.Calls != 1 || custom.Calls != 1 {
		t.Fatalf("encoding methods were invoked repeatedly: IsZero=%d, MarshalYAML=%d", optional.Calls, custom.Calls)
	}
}

func TestUnfinishedStagingReservesItsID(t *testing.T) {
	if output, err := helperProcess(t, "orphan-collision", t.TempDir()).CombinedOutput(); err != nil {
		t.Fatalf("staging collision: %v\n%s", err, output)
	}
}

func testOrphanCollision(t *testing.T, path string) {
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	id := "m_000000000000"
	name := filepath.Join("memories", id+"/record.md")
	temporary := temporaryName(name, "", []byte(sample))
	if err := fixtureRootMkdir(root, pendingName(name), 0700); err != nil {
		t.Fatal(err)
	}
	if err := fixtureRootWriteFile(root, temporary, []byte(sample), 0600); err != nil {
		t.Fatal(err)
	}
	// Force an initial collision, then provide different random bytes. Keep the
	// process-global reader replacement confined to this subprocess.
	previous := rand.Reader
	rand.Reader = bytes.NewReader(append(make([]byte, 24), bytes.Repeat([]byte{1}, 512)...))
	defer func() { rand.Reader = previous }()
	doc, err := store.Create(Memories, testUserFields(Memories, nil), "New body")
	if err != nil || doc.ID == id {
		t.Fatalf("new save reused interrupted memory %s: %v", id, err)
	}
	read, err := store.Read(id)
	if err != nil || read.Body != Parse([]byte(sample)).Body || read.Pending {
		t.Fatalf("original memory did not recover under its own ID: %v", err)
	}
}

func TestRemovedArtifactsClearOnlyObsoleteDiagnostics(t *testing.T) {
	path := t.TempDir()
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	first := temporaryName("memories/m_000000000000/record.md", "", []byte(sample))
	second := temporaryName("entities/e_000000000000/record.md", "", []byte(sample))
	for _, name := range []string{first, second} {
		if err := root.MkdirAll(filepath.Dir(name), 0700); err != nil {
			t.Fatal(err)
		}
		if err := fixtureRootWriteFile(root, name, []byte("Partial data"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Recover(); err != nil || len(store.Diagnostics()) != 2 {
		t.Fatalf("damaged artifacts were not reported: %v, %v", err, store.Diagnostics())
	}
	if err := root.Remove(first); err != nil {
		t.Fatal(err)
	}
	if err := root.Remove(filepath.Dir(first)); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() != 0 {
		// An unreadable directory must keep its earlier file diagnostics.
		if err := root.Chmod("entities", 0111); err != nil {
			t.Fatal(err)
		}
		defer root.Chmod("entities", 0700)
		if err := store.Recover(); err != nil {
			t.Fatal(err)
		}
		if issues := strings.Join(store.Diagnostics(), "\n"); !strings.Contains(issues, "e_000000000000") || strings.Contains(issues, "m_000000000000") {
			t.Fatalf("failed scan hid retained damage or kept removed damage: %s", issues)
		}
		if err := root.Chmod("entities", 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Recover(); err != nil {
		t.Fatal(err)
	}
	issues := store.Diagnostics()
	if len(issues) != 1 || !strings.Contains(issues[0], "e_000000000000") {
		t.Fatalf("removed artifact stayed reported, or retained damage was hidden: %v", issues)
	}
	if err := root.Remove(second); err != nil {
		t.Fatal(err)
	}
	if err := root.Remove(filepath.Dir(second)); err != nil {
		t.Fatal(err)
	}
	if err := store.Recover(); err != nil || len(store.Diagnostics()) != 0 {
		t.Fatalf("obsolete diagnostics survived recovery: %v, %v", err, store.Diagnostics())
	}
}

func TestReadsSurviveConcurrentPublication(t *testing.T) {
	path := t.TempDir()
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	lock := flock.New(filepath.Join(path, ".lock"))
	if err := lock.Lock(); err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	for iteration := range 150 {
		id := fmt.Sprintf("m_%012d", iteration)
		name := filepath.Join("memories", id+"/record.md")
		if ready, err := stage(root, name, "", []byte(sample)); !ready || err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		results := make(chan error, 128)
		var readers sync.WaitGroup
		for range 128 {
			readers.Go(func() {
				<-start
				doc, err := store.Read(id)
				if err == nil && doc.Body != Parse([]byte(sample)).Body {
					err = errors.New("publication changed the readable body")
				}
				results <- err
			})
		}
		close(start)
		for range 8 {
			runtime.Gosched()
		}
		if published, err := store.complete(root, name); !published || err != nil {
			t.Fatalf("publication failed: %v", err)
		}
		readers.Wait()
		close(results)
		for err := range results {
			if err != nil {
				t.Fatalf("readable data disappeared during publication: %v", err)
			}
		}
		if _, err := root.Lstat(pendingName(name)); !errors.Is(err, fs.ErrNotExist) {
			t.Fatal("publication did not clean up the pending marker")
		}
	}
}
