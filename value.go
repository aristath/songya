package songya

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

const issueTag = "!songya/issue"

func issueValue(message string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: issueTag, Value: message}
}

// Normalize data, never arbitrary Go encoding callbacks. An unsupported leaf is
// kept as a tagged diagnostic while its understandable neighbours survive.
func encodeValue(value any) (*yaml.Node, []string) {
	type reference struct {
		typeOf  reflect.Type
		pointer uintptr
		length  int
	}
	references := map[reference]*yaml.Node{} // nil marks a reference still being encoded.
	var walk func(reflect.Value) *yaml.Node
	walk = func(v reflect.Value) (node *yaml.Node) {
		if !v.IsValid() || (v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface) && v.IsNil() {
			return yamlValue(nil)
		}
		if v.Kind() == reflect.Interface {
			return walk(v.Elem())
		}
		switch v.Kind() {
		case reflect.Pointer, reflect.Map, reflect.Slice:
			if !v.IsNil() {
				key := reference{typeOf: v.Type(), pointer: uintptr(v.UnsafePointer())}
				if v.Kind() == reflect.Slice {
					key.length = v.Len()
				}
				if previous, found := references[key]; found {
					if previous == nil {
						return issueValue("cyclic Go reference: " + v.Type().String())
					}
					return previous
				}
				references[key] = nil
				defer func() { references[key] = node }()
			}
		}
		if v.CanInterface() {
			var source *yaml.Node
			switch typed := v.Interface().(type) {
			case yaml.Node:
				source = &typed
			case *yaml.Node:
				source = typed
			case json.Number:
				if _, err := json.Marshal(typed); err != nil {
					return issueValue(err.Error())
				}
				tag := "!!int"
				if strings.ContainsAny(string(typed), ".eE") {
					tag = "!!float"
				}
				return &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: string(typed)}
			case time.Time, time.Duration, Relation, Metadata, RecordTime:
				return yamlValue(typed)
			}
			if source != nil {
				return source
			}
		}
		switch v.Kind() {
		case reflect.Pointer:
			return walk(v.Elem())
		case reflect.Map:
			type pair struct{ key, value *yaml.Node }
			entries := make([]pair, 0, v.Len())
			iterator := v.MapRange()
			for iterator.Next() {
				entries = append(entries, pair{walk(iterator.Key()), walk(iterator.Value())})
			}
			order := func(node *yaml.Node) string {
				normalized, _ := normalizeNode(node)
				data, _ := yaml.Marshal(normalized)
				return string(data)
			}
			sort.SliceStable(entries, func(i, j int) bool {
				a, b := entries[i].key, entries[j].key
				if a.Value != b.Value {
					return a.Value < b.Value
				}
				if a.Tag != b.Tag {
					return a.Tag < b.Tag
				}
				if left, right := order(a), order(b); left != right {
					return left < right
				}
				return order(entries[i].value) < order(entries[j].value)
			})
			node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			for _, entry := range entries {
				node.Content = append(node.Content, entry.key, entry.value)
			}
			return node
		case reflect.Slice, reflect.Array:
			node := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
			for i := 0; i < v.Len(); i++ {
				node.Content = append(node.Content, walk(v.Index(i)))
			}
			return node
		case reflect.String:
			return yamlValue(v.String())
		case reflect.Bool:
			return yamlValue(v.Bool())
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			return yamlValue(v.Int())
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
			return yamlValue(v.Uint())
		case reflect.Float32, reflect.Float64:
			return yamlValue(v.Float())
		default:
			return issueValue("unsupported Go value: " + v.Type().String())
		}
	}
	return normalizeNode(walk(reflect.ValueOf(value)))
}

func valueIssues(node *yaml.Node, path string) []string {
	var issues []string
	seen := map[*yaml.Node]bool{}
	var visit func(*yaml.Node, string)
	visit = func(node *yaml.Node, path string) {
		if node == nil || seen[node] {
			return
		}
		seen[node] = true
		if node.Tag == issueTag {
			issues = append(issues, path+": "+node.Value)
		}
		for i, child := range node.Content {
			name := fmt.Sprintf("%s[%d]", path, i)
			if node.Kind == yaml.MappingNode {
				if i%2 == 0 {
					name = path + "[key]"
				} else if key, ok := fieldName(node.Content[i-1]); ok {
					name = path + "." + key
				} else {
					name = path + "[unrecognized key]"
				}
			}
			visit(child, name)
		}
	}
	visit(node, path)
	return issues
}
