package songya

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestInvalidAnchorsPreserveValuesAndReferences(t *testing.T) {
	for _, kind := range []yaml.Kind{yaml.ScalarNode, yaml.MappingNode} {
		path := t.TempDir()
		store, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		definition := &yaml.Node{Kind: kind, Tag: "!!str", Anchor: "bad anchor", Value: "Keep value"}
		var want any = "Keep value"
		if kind == yaml.MappingNode {
			definition.Tag, definition.Value = "!!map", ""
			definition.Content = []*yaml.Node{
				{Kind: yaml.ScalarNode, Tag: "!!str", Value: "fact"},
				{Kind: yaml.ScalarNode, Tag: "!!str", Value: "Keep value"},
			}
			want = map[string]any{"fact": "Keep value"}
		}
		reference := &yaml.Node{Kind: yaml.AliasNode, Value: "bad anchor", Alias: definition}
		reserved := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Anchor: "bad_anchor", Value: "Keep reserved"}
		doc, err := store.Create(Memories, map[string]any{
			"title": "Healthy title",
			"details": map[string]any{
				"fact":   "Healthy fact",
				"values": []*yaml.Node{definition, reference, reserved},
			},
		}, "Healthy body")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(strings.Join(doc.Diagnostics(), "\n"), "repaired") {
			t.Fatal("anchor repair was not reported")
		}
		if definition.Anchor != "bad anchor" || reference.Value != "bad anchor" {
			t.Fatal("repair modified the caller's nodes")
		}
		store, err = Open(path)
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
		details := fields["details"].(map[string]any)
		if details["fact"] != "Healthy fact" || !reflect.DeepEqual(details["values"], []any{want, want, "Keep reserved"}) || read.Body != "Healthy body" || read.Metadata().Title != "Healthy title" || len(read.Diagnostics()) != 0 {
			t.Fatalf("repair lost content or left broken references: %v, %v", fields, read.Diagnostics())
		}
	}
}

const mergeDefaults = `base: &base
  title: Inherited title
  created_at: 2026-11-03T10:00:00Z
  updated_at: 2026-11-03T10:00:00Z
  relations:
    - type: motivated_by
      target: m_b8r5n2w9d4hx
other: &other
  title: Other title
  created_at: 2020-01-01T00:00:00Z
`

func TestMetadataUsesYAMLMergePrecedence(t *testing.T) {
	for name, fields := range map[string]string{
		"mapping":  "<<: *base\n",
		"sequence": "<<: [*other, *base]\n",
		"nested":   "nested: &nested\n  <<: *base\n  title: Nested title\n<<: *nested\n",
		"explicit": "title: Explicit title\n<<: [*base, *other]\n",
		"null":     "<<: *base\ntitle: null\n",
		"literal":  "'<<': *base\n",
	} {
		t.Run(name, func(t *testing.T) {
			doc := Parse([]byte("---\n" + mergeDefaults + fields + "---\nBody"))
			var want Metadata
			if err := doc.Frontmatter.Decode(&want); err != nil {
				t.Fatal(err)
			}
			before, err := doc.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			if got := doc.Metadata(); !reflect.DeepEqual(got, want) {
				t.Fatalf("merge semantics changed: got %+v, want %+v", got, want)
			}
			unchanged, err := doc.Marshal()
			if err != nil || !bytes.Equal(before, unchanged) {
				t.Fatal("reading metadata changed the source tree")
			}
			if err := doc.Set("title", "Edited title"); err != nil {
				t.Fatal(err)
			}
			after, err := doc.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			read := Parse(after)
			base := field(&read.Frontmatter, "base")
			if read.Metadata().Title != "Edited title" || field(base, "title").Value != "Inherited title" || read.Body != "Body" {
				t.Fatal("reading or editing inherited metadata changed its source")
			}
		})
	}
}

func TestDamagedMergeSourcesKeepHealthyMetadataAvailable(t *testing.T) {
	for _, fields := range []string{
		"<<: [broken, *base]\n",
		"loop: &loop\n  <<: *loop\n<<: [*loop, *base]\n",
		"<<: *base\ncreated_at: unknown\ntitle: First title\ntitle: Second title\n",
	} {
		doc := Parse([]byte("---\n" + mergeDefaults + fields + "---\nBody"))
		m := doc.Metadata()
		if m.Title == "" || m.UpdatedAt.IsZero() || len(m.Relations) != 1 || doc.Body != "Body" {
			t.Fatalf("one damaged source hid healthy fields: %+v", m)
		}
		if strings.Contains(fields, "unknown") && (m.Title != "First title" || !m.CreatedAt.IsZero() || len(doc.Diagnostics()) == 0) {
			t.Fatal("damaged explicit fields were hidden by inherited defaults")
		}
		encoded, err := doc.Marshal()
		if err != nil || Parse(encoded).Body != "Body" {
			t.Fatalf("damaged merge prevented serialization: %v", err)
		}
	}
}

func TestAliasKeysPreserveFieldIdentity(t *testing.T) {
	for _, fields := range []string{
		"key: &title unrelated\n*title: Keep unrelated value\ntitle: Actual title\n",
		"key: &label title\n*label: Actual title\nunrelated: Keep unrelated value\n",
	} {
		doc := Parse([]byte("---\n" + fields + "created_at: 2026-11-03T00:00:00Z\nupdated_at: 2026-11-03T00:00:00Z\n---\nBody"))
		if doc.Metadata().Title != "Actual title" || len(doc.Diagnostics()) != 0 {
			t.Fatalf("alias name was mistaken for its key: %+v, %v", doc.Metadata(), doc.Diagnostics())
		}
		if err := doc.Set("title", "New title"); err != nil {
			t.Fatal(err)
		}
		encoded, err := doc.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		read := Parse(encoded)
		var values map[string]any
		if err := read.Frontmatter.Decode(&values); err != nil {
			t.Fatal(err)
		}
		if values["title"] != "New title" || values["unrelated"] != "Keep unrelated value" || read.Metadata().Title != "New title" || read.Body != "Body" || len(read.Diagnostics()) != 0 {
			t.Fatalf("editing title changed another field: %v, %v", values, read.Diagnostics())
		}
		if !bytes.Contains(encoded, []byte("*")) {
			t.Fatal("editing a value flattened its alias key")
		}
	}
}

func TestDuplicateAliasesUseResolvedKeys(t *testing.T) {
	for _, key := range []string{"title", "3"} {
		doc := Parse([]byte("---\nkey: &label " + key + "\n*label: First\n'" + key + "': Second\n---\nBody"))
		if !strings.Contains(strings.Join(doc.Diagnostics(), "\n"), "Duplicate field "+key+" retained") || field(&doc.Frontmatter, key).Value != "First" {
			t.Fatalf("a duplicate resolved key was missed: %v", doc.Diagnostics())
		}
		if err := doc.Set(key, "Edited first"); err != nil {
			t.Fatal(err)
		}
		encoded, err := doc.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		read := Parse(encoded)
		if field(&read.Frontmatter, key).Value != "Edited first" || read.Frontmatter.Content[5].Value != "Second" || read.Body != "Body" {
			t.Fatal("editing a duplicate discarded a retained value")
		}
	}
}

func TestSharedMergeReferencesRemainResponsive(t *testing.T) {
	if output, err := helperProcess(t, "shared-merges", "").CombinedOutput(); err != nil {
		t.Fatalf("shared merge references blocked metadata: %v\n%s", err, output)
	}
}

func testSharedMergeReferences(t *testing.T) {
	var source strings.Builder
	source.WriteString("---\ntitle: Healthy\ncreated_at: 2026-11-03T00:00:00Z\nupdated_at: 2026-11-03T00:00:00Z\nbase: &m0 {note: Keep}\n")
	for i := 1; i <= 40; i++ {
		fmt.Fprintf(&source, "m%d: &m%d {<<: [*m%d, *m%d]}\n", i, i, i-1, i-1)
	}
	for _, fallback := range []bool{false, true} {
		merge, relations := "<<: *m40\n", 0
		if fallback {
			merge, relations = "<<: [*m40, {relations: [{type: after, target: m_b8r5n2w9d4hx}]}]\n", 1
		}
		doc := Parse([]byte(source.String() + "? *m40\n: Compound key\n" + merge + "---\nBody"))
		m := doc.Metadata()
		if m.Title != "Healthy" || m.CreatedAt.IsZero() || m.UpdatedAt.IsZero() || len(m.Relations) != relations || len(doc.Diagnostics()) != 0 {
			t.Fatalf("shared sources hid healthy metadata: %+v, %v", m, doc.Diagnostics())
		}
		if err := doc.Set("title", "Edited"); err != nil {
			t.Fatal(err)
		}
		encoded, err := doc.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		read := Parse(encoded)
		if read.Metadata().Title != "Edited" || len(read.Metadata().Relations) != relations || read.Body != "Body" || len(read.Diagnostics()) != 0 {
			t.Fatal("shared sources prevented editing or rereading")
		}
	}
}
