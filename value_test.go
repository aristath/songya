package songya

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

func TestValueEncodingPreservesYAMLSemantics(t *testing.T) {
	var source yaml.Node
	if err := yaml.Unmarshal([]byte("tagged: !custom preserved\nvalue: &v [Keep]\ncopy: *v\n"), &source); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	for name, value := range map[string]any{
		"nil":          nil,
		"nil map":      map[string]any(nil),
		"nil list":     []string(nil),
		"scalars":      []any{"000123", "yes", true, 3, 1.5, nil},
		"map keys":     map[any]any{3: "Three", "other": []string{"Keep"}},
		"string keys":  map[string]any{"key10": "Ten", "key2": "Two", "other": []int{1, 2}},
		"binary keys":  map[string]string{"\xff": "First", "\xfe": "Second"},
		"time pointer": &now,
		"times":        map[string]any{"time": now, "duration": time.Second},
		"relations":    []Relation{{Type: "after", Target: "m_7k3m9x2q6v4c"}},
		"YAML node":    *source.Content[0],
	} {
		t.Run(name, func(t *testing.T) {
			expected, err := yaml.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			encoded, _ := encodeValue(value)
			actual, err := yaml.Marshal(encoded)
			if err != nil {
				t.Fatal(err)
			}
			var want, got any
			if err := yaml.Unmarshal(expected, &want); err != nil {
				t.Fatal(err)
			}
			if err := yaml.Unmarshal(actual, &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(want, got) {
				t.Fatalf("YAML values changed:\nwant:\n%sgot:\n%s", expected, actual)
			}
			if name == "string keys" && strings.Index(string(actual), "key10:") > strings.Index(string(actual), "key2:") {
				t.Fatalf("new maps are not ordered lexically: %s", actual)
			}
			if name == "YAML node" && !strings.Contains(string(actual), "!custom") {
				t.Fatalf("custom tag lost: %s", actual)
			}
		})
	}
}

func TestNodeContainersKeepAliases(t *testing.T) {
	for _, kind := range []string{"pointer list", "value list", "map"} {
		t.Run(kind, func(t *testing.T) {
			definition := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "Keep", Anchor: "v", LineComment: "Keep value comment"}
			reference := &yaml.Node{Kind: yaml.AliasNode, Value: "v", Alias: definition, LineComment: "Keep alias comment"}
			var value any
			switch kind {
			case "pointer list":
				value = []*yaml.Node{definition, reference}
			case "value list":
				value = []yaml.Node{*definition, *reference}
			case "map":
				value = map[string]*yaml.Node{"definition": definition, "reference": reference}
			}
			direct, err := yaml.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			var want any
			if err := yaml.Unmarshal(direct, &want); err != nil {
				t.Fatal(err)
			}
			store, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			doc, err := store.Create(Memories, map[string]any{"payload": value}, "Body")
			if err != nil || len(doc.Diagnostics()) != 0 {
				t.Fatalf("valid YAML aliases were replaced: %v, %v", err, doc.Diagnostics())
			}
			before, err := doc.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			definition.Value, reference.Value = "Changed by caller", "broken"
			after, err := doc.Marshal()
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("encoding retained mutable references to the caller's nodes")
			}
			read, err := store.Read(doc.ID)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]any
			if err := read.Frontmatter.Decode(&fields); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(fields["payload"], want) || read.Body != "Body" {
				t.Fatalf("saved values changed: %v", fields["payload"])
			}
			if !bytes.Contains(before, []byte("*v")) || !bytes.Contains(before, []byte("Keep value comment")) || !bytes.Contains(before, []byte("Keep alias comment")) {
				t.Fatalf("alias representation or comment was lost:\n%s\nOriginal:\n%s", before, direct)
			}
		})
	}
}
