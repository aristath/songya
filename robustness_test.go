package songya

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestRawYAMLRecoveryKeepsOtherWorkUsable(t *testing.T) {
	if output, err := helperProcess(t, "raw-yaml-recovery", t.TempDir()).CombinedOutput(); err != nil {
		t.Fatalf("raw YAML stalled or damaged the writer: %v\n%s", err, output)
	}
}

func testRawYAMLRecovery(t *testing.T, path string) {
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	self := &yaml.Node{Kind: yaml.AliasNode, Anchor: "self", Value: "self"}
	self.Alias = self
	for name, node := range map[string]*yaml.Node{
		"tag":             {Kind: yaml.ScalarNode, Tag: string([]byte{255, 254}), Value: "Keep value"},
		"binary":          {Kind: yaml.ScalarNode, Tag: "!!str", Value: string([]byte{255, 254})},
		"missing kind":    {Value: "Keep value"},
		"self alias":      self,
		"scalar children": {Kind: yaml.ScalarNode, Tag: "!!str", Value: "Keep value", Content: []*yaml.Node{yamlValue("Keep child")}},
		"documents":       {Kind: yaml.DocumentNode, Content: []*yaml.Node{yamlValue("Keep value"), yamlValue("Keep child")}},
	} {
		doc, err := store.Create(Memories, testUserFields(Memories, map[string]any{"title": "Healthy", "payload": node}), "Original body")
		if err != nil {
			t.Fatalf("%s prevented saving healthy content: %v", name, err)
		}
		read, err := store.Read(doc.ID)
		if err != nil || read.Body != "Original body" || read.Metadata().Title != "Healthy" || read.Metadata().CreatedAt.IsZero() {
			t.Fatalf("%s damaged the record: %v", name, err)
		}
		data, err := read.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]any
		if err := read.Frontmatter.Decode(&fields); err != nil {
			t.Fatalf("%s wrote unreadable metadata: %v", name, err)
		}
		if name == "binary" && fields["payload"] != string([]byte{255, 254}) {
			t.Fatal("binary bytes were discarded")
		}
		if name == "missing kind" && fields["payload"] != "Keep value" {
			t.Fatal("a known value became null")
		}
		if name == "tag" || name == "scalar children" || name == "documents" {
			if len(read.Diagnostics()) == 0 || !bytes.Contains(data, []byte("Keep value")) {
				t.Fatalf("%s was discarded without explanation", name)
			}
		}
		if (name == "scalar children" || name == "documents") && !bytes.Contains(data, []byte("Keep child")) {
			t.Fatalf("%s discarded its children", name)
		}
		if _, err := store.Create(Topics, testUserFields(Topics, nil), "Other work continues"); err != nil {
			t.Fatal(err)
		}
	}
	testRawProperties(t)
	testSharedGoData(t, store)
	testDirectRawReads(t)
}

func testRawProperties(t *testing.T) {
	for _, kind := range []yaml.Kind{yaml.ScalarNode, yaml.MappingNode, yaml.SequenceNode} {
		for _, property := range []string{"tag", "value", "anchor", "head", "line", "foot"} {
			for _, input := range []string{"\x00", "\x01", "\x7f", "\u0081", "\u0085", "\u2028", "\u2029", "\ufffe", "\uffff", string([]byte{255}), "α", "a\nb", "a\rb", "!<tag>", "%", " ", "a:b"} {
				node := &yaml.Node{Kind: kind, Tag: "!!str", Value: "Keep"}
				if kind != yaml.ScalarNode {
					node.Value = ""
					node.Content = []*yaml.Node{yamlValue("Keep child")}
					if kind == yaml.MappingNode {
						node.Content = append([]*yaml.Node{yamlValue("key")}, node.Content...)
					}
				}
				switch property {
				case "tag":
					node.Tag = input
				case "value":
					node.Value = input
				case "anchor":
					node.Anchor = input
				case "head":
					node.HeadComment = input
				case "line":
					node.LineComment = input
				case "foot":
					node.FootComment = input
				}
				doc := Parse([]byte(sample))
				if err := doc.Set("payload", node); err != nil {
					t.Fatal(err)
				}
				data, err := doc.Marshal()
				if err != nil {
					t.Fatal(err)
				}
				read := Parse(data)
				if read.Metadata().Title != "Example" || read.Body != doc.Body || field(&read.Frontmatter, "unparsed_frontmatter") != nil {
					t.Fatalf("kind %d, %s %q damaged healthy data: %s", kind, property, input, data)
				}
			}
		}
	}
}

func testSharedGoData(t *testing.T, store *Store) {
	var value any = "Keep"
	const depth = 14 // An accidental expansion remains bounded while exposing the bug.
	for range depth {
		value = map[string]any{"a": value, "b": value}
	}
	doc, err := store.Create(Topics, testUserFields(Topics, map[string]any{"title": "Healthy", "payload": value}), "Keep body")
	if err != nil {
		t.Fatal(err)
	}
	read, err := store.Read(doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := read.Marshal()
	if err != nil || len(data) > 10000 || read.Body != "Keep body" || read.Metadata().Title != "Healthy" || len(read.Diagnostics()) != 0 {
		t.Fatalf("shared data expanded or hid healthy work: bytes=%d, error=%v, diagnostics=%v", len(data), err, read.Diagnostics())
	}
	node := field(&read.Frontmatter, "payload")
	for range depth {
		a, b := dereference(field(node, "a")), dereference(field(node, "b"))
		if a != b && a.Kind != yaml.ScalarNode {
			t.Fatal("shared data lost its identity")
		}
		node = a
	}
	if node.Value != "Keep" {
		t.Fatal("shared data lost its value")
	}
}

func testDirectRawReads(t *testing.T) {
	doc := &Document{Frontmatter: yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{nil, nil}}, Body: "Keep"}
	// The public tree can contain empty children or direct structural cycles.
	doc.Frontmatter.Content = append(doc.Frontmatter.Content, yamlValue("cycle"), &doc.Frontmatter)
	_ = doc.Metadata()
	_ = doc.Diagnostics()
	if err := doc.Set("title", "Healthy"); err != nil {
		t.Fatal(err)
	}
	data, err := doc.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if read := Parse(data); read.Body != "Keep" || read.Metadata().Title != "Healthy" {
		t.Fatal("repair discarded healthy content")
	}
	for _, original := range []*yaml.Node{yamlValue("Keep scalar"), {Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{yamlValue("title"), yamlValue("Keep item")}}} {
		malformed := &Document{Frontmatter: *original, Body: "Keep"}
		if err := malformed.Set("title", "Healthy"); err != nil {
			t.Fatal(err)
		}
		data, err := malformed.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		read := Parse(data)
		var want, got any
		if err := original.Decode(&want); err != nil {
			t.Fatal(err)
		}
		if err := field(&read.Frontmatter, "unparsed_frontmatter").Decode(&got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(want, got) || read.Body != "Keep" || read.Metadata().Title != "Healthy" {
			t.Fatal("editing a non-mapping root discarded data or hid the edit")
		}
	}

	for _, root := range []yaml.Node{
		{Kind: yaml.MappingNode, Tag: "!!map", Value: "Keep value", Content: []*yaml.Node{yamlValue("fact"), yamlValue("Keep child")}},
		{Kind: yaml.MappingNode, Tag: string([]byte{255}), Content: []*yaml.Node{yamlValue("fact"), yamlValue("Keep child")}},
	} {
		damaged := &Document{Frontmatter: root, Body: "Keep"}
		if err := damaged.Set("title", "Healthy"); err != nil {
			t.Fatal(err)
		}
		data, err := damaged.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		read := Parse(data)
		if read.Metadata().Title != "Healthy" || read.Body != "Keep" || !bytes.Contains(data, []byte("Keep child")) || len(read.Diagnostics()) == 0 {
			t.Fatal("a malformed root hid the edit or discarded original data")
		}
	}

	for _, children := range [][]*yaml.Node{{yamlValue("title"), nil}, {yamlValue("title")}} {
		incomplete := &Document{Frontmatter: yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: children}, Body: "Keep"}
		if err := incomplete.Set("title", "Healthy"); err != nil {
			t.Fatal(err)
		}
		data, err := incomplete.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		if read := Parse(data); read.Metadata().Title != "Healthy" || read.Body != "Keep" || len(read.Frontmatter.Content) != 2 {
			t.Fatalf("editing an incomplete mapping hid the edit: %s", data)
		}
	}

}

func TestRawMapAliasesKeepSuppliedIdentity(t *testing.T) {
	var parsed yaml.Node
	if err := yaml.Unmarshal([]byte("a_definition: &x A\nz_reference: *x\nb_definition: &x B\n"), &parsed); err != nil {
		t.Fatal(err)
	}
	var nodes map[string]yaml.Node
	var want map[string]any
	if err := parsed.Decode(&nodes); err != nil {
		t.Fatal(err)
	}
	if err := parsed.Decode(&want); err != nil {
		t.Fatal(err)
	}
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	doc, err := store.Create(Memories, testUserFields(Memories, map[string]any{"payload": nodes}), "Body")
	if err != nil {
		t.Fatal(err)
	}
	read, err := store.Read(doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := read.Frontmatter.Decode(&fields); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fields["payload"], want) || len(read.Diagnostics()) != 0 {
		t.Fatalf("sorting rebound a supplied alias: %v, %v", fields, read.Diagnostics())
	}
	definition := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Anchor: "v", Value: "Keep"}
	if err := read.Set("forward", map[string]*yaml.Node{"z_definition": definition, "a_reference": {Kind: yaml.AliasNode, Value: "v", Alias: definition}}); err != nil {
		t.Fatal(err)
	}
	data, err := read.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := Parse(data).Frontmatter.Decode(&fields); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fields["forward"], map[string]any{"a_reference": "Keep", "z_definition": "Keep"}) {
		t.Fatal("a forward reference lost its target")
	}
}

func TestInitializationRetriesAfterUnknownOrObstructedLock(t *testing.T) {
	for _, obstruction := range []string{"permissions", "directory"} {
		t.Run(obstruction, func(t *testing.T) {
			if obstruction == "permissions" && os.Geteuid() == 0 {
				t.Skip("root bypasses permissions")
			}
			path := t.TempDir()
			if obstruction == "permissions" {
				if err := os.Chmod(path, 0600); err != nil {
					t.Fatal(err)
				}
				defer os.Chmod(path, 0700)
			} else if err := os.Mkdir(filepath.Join(path, ".lock"), 0700); err != nil {
				t.Fatal(err)
			}
			store, err := Open(path)
			if err != nil || len(store.Diagnostics()) == 0 {
				t.Fatalf("initialization did not defer: %v", err)
			}
			if obstruction == "permissions" {
				err = os.Chmod(path, 0700)
			} else {
				err = os.Remove(filepath.Join(path, ".lock"))
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Recover(); err != nil || len(store.Diagnostics()) != 0 {
				t.Fatalf("original handle remained blocked: %v, %v", err, store.Diagnostics())
			}
			if _, err := store.Create(Memories, testUserFields(Memories, nil), "Recovered"); err != nil {
				t.Fatal(err)
			}
		})
	}
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(store.path, ".lock")); err != nil {
		t.Fatal(err)
	}
	if err := store.Recover(); err == nil || len(store.Diagnostics()) == 0 {
		t.Fatal("an established handle replaced its missing lock or hid the obstruction")
	}
}

func TestMetadataRepairsRemainIndependent(t *testing.T) {
	doc := Parse([]byte("---\ntitle: Healthy\ndate: &date '2026-11-03 10:00:00'\ncreated_at: *date\nupdated_at: *date\nrelations:\n - type: after\n   target: m_000000000000\n   note: First\n   note: Second\n---\nBody"))
	m := doc.Metadata()
	if m.CreatedAt.IsZero() || !m.CreatedAt.Equal(m.UpdatedAt) || !reflect.DeepEqual(m.Relations, []Relation{{Type: "after", Target: "m_000000000000"}}) {
		t.Fatalf("an unrelated field hid healthy metadata: %+v", m)
	}
	if !strings.Contains(strings.Join(doc.Diagnostics(), "\n"), "Duplicate field note") {
		t.Fatal("duplicate data was not reported")
	}
	if err := doc.Set("relations", nil); err != nil {
		t.Fatal(err)
	}
	if len(doc.Diagnostics()) != 0 {
		t.Fatalf("empty optional relations were marked broken: %v", doc.Diagnostics())
	}
	doc = Parse([]byte("---\nkey: &name actual_field\n*name: !songya/issue Keep\n---\nBody"))
	if !strings.Contains(strings.Join(doc.Diagnostics(), "\n"), "metadata.actual_field: Keep") {
		t.Fatal("issue path used the anchor name")
	}
}

func TestEditsKeepIncomingCommentsAndClearResolvedObstructions(t *testing.T) {
	doc := Parse([]byte("---\npayload: Old # Old comment\n---\nBody"))
	if err := doc.Set("payload", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "New", HeadComment: "New comment"}); err != nil {
		t.Fatal(err)
	}
	data, err := doc.Marshal()
	if err != nil || !bytes.Contains(data, []byte("Old comment")) || !bytes.Contains(data, []byte("New comment")) {
		t.Fatal("editing discarded a comment")
	}
	for range 6 {
		if err := doc.Set("payload", *field(&doc.Frontmatter, "payload")); err != nil {
			t.Fatal(err)
		}
	}
	data, err = doc.Marshal()
	if err != nil || bytes.Count(data, []byte("Old comment")) != 1 || bytes.Count(data, []byte("New comment")) != 1 {
		t.Fatal("repeated no-op edits duplicated comments")
	}

	path := t.TempDir()
	if err := fixtureWriteFile(filepath.Join(path, "memories"), []byte("Obstruction"), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(path, "memories")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(Memories, testUserFields(Memories, nil), "Healthy"); err != nil || len(store.Diagnostics()) != 0 {
		t.Fatalf("resolved obstruction stayed reported: %v, %v", err, store.Diagnostics())
	}
}

func TestFailedCollisionDoesNotReturnAnotherMemoryID(t *testing.T) {
	if output, err := helperProcess(t, "failed-collision", t.TempDir()).CombinedOutput(); err != nil {
		t.Fatalf("failed save claimed another ID: %v\n%s", err, output)
	}
}

func testFailedCollision(t *testing.T, path string) {
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	id := "m_000000000000"
	if err := os.MkdirAll(filepath.Join(path, pendingName(filepath.Join("memories", id+"/record.md"))), 0700); err != nil {
		t.Fatal(err)
	}
	previous := rand.Reader
	rand.Reader = bytes.NewReader(make([]byte, IDSuffixLength))
	defer func() { rand.Reader = previous }()
	doc, err := store.Create(Memories, testUserFields(Memories, nil), "Unsaved body")
	if err == nil || doc.ID != "" || doc.Pending || doc.Body != "Unsaved body" {
		t.Fatalf("failed save identified foreign work: %+v, %v", doc, err)
	}
}
