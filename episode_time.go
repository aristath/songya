package songya

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// RecordTime holds lifetime dates for entities and event dates for other records.
// Lifetime is internal projection state; it is retained by the derived gob cache.
type RecordTime struct {
	Date     *string `json:"date" yaml:"date"`
	Earliest *string `json:"earliest" yaml:"earliest"`
	Latest   *string `json:"latest" yaml:"latest"`
	Start    *string `json:"start" yaml:"start"`
	End      *string `json:"end" yaml:"end"`
	Lifetime bool    `json:"-" yaml:"-"`
}

func (t RecordTime) fields() map[string]*string {
	if t.Lifetime || t.Start != nil || t.End != nil {
		fields := map[string]*string{"start": t.Start, "end": t.End}
		// Preserve mixed caller input so write validation can reject it explicitly.
		for key, value := range map[string]*string{"date": t.Date, "earliest": t.Earliest, "latest": t.Latest} {
			if value != nil {
				fields[key] = value
			}
		}
		return fields
	}
	return map[string]*string{"date": t.Date, "earliest": t.Earliest, "latest": t.Latest}
}

func (t RecordTime) MarshalJSON() ([]byte, error) { return json.Marshal(t.fields()) }
func (t *RecordTime) UnmarshalJSON(raw []byte) error {
	var fields map[string]*string
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	_, start := fields["start"]
	_, end := fields["end"]
	*t = RecordTime{Date: fields["date"], Earliest: fields["earliest"], Latest: fields["latest"], Start: fields["start"], End: fields["end"], Lifetime: start || end}
	return nil
}
func (t RecordTime) MarshalYAML() (any, error) { return t.fields(), nil }
func (t RecordTime) hasDates() bool {
	return t.Date != nil || t.Earliest != nil || t.Latest != nil || t.Start != nil || t.End != nil
}
func timeKeys(kind Destination) []string {
	if kind == Entities {
		return []string{"start", "end"}
	}
	return []string{"date", "earliest", "latest"}
}
func timeFieldNames(kind Destination) string { return strings.Join(timeKeys(kind), ", ") }
func (d *Document) timeKind() Destination {
	if d.Kind != "" {
		return d.Kind
	}
	if ValidID(d.ID) {
		return kindOf(d.ID)
	}
	node := metadataField(&d.Frontmatter, "time")
	if metadataField(node, "start") != nil || metadataField(node, "end") != nil {
		return Entities
	}
	return Memories
}

func calendarDate(node *yaml.Node) bool {
	node = dereference(node)
	if node == nil || node.Kind != yaml.ScalarNode || (node.ShortTag() != "!!str" && node.ShortTag() != "!!timestamp") {
		return false
	}
	_, err := time.Parse(time.DateOnly, strings.TrimSpace(node.Value))
	return err == nil
}

func validateTimeInput(fields map[string]any, kind Destination) error {
	if value, supplied := fields["time"]; supplied {
		node, _ := encodeValue(value)
		return validateTimeChange(nil, node, kind)
	}
	return nil
}

// Validate newly supplied values without making existing damaged metadata block
// an unrelated edit. Raw damaged values remain in source/journals for repair.
func validateTimeChange(previous, next *yaml.Node, kind Destination) error {
	before, beforeErr := yaml.Marshal(previous)
	after, afterErr := yaml.Marshal(next)
	if beforeErr == nil && afterErr == nil && bytes.Equal(before, after) {
		return nil
	}
	next = dereference(next)
	if next == nil || next.ShortTag() == "!!null" {
		return nil
	}
	if kind != Entities && next.Kind == yaml.ScalarNode && calendarDate(next) {
		return nil // An unambiguous date normalizes to the canonical object.
	}
	if next.Kind != yaml.MappingNode {
		return fmt.Errorf("time needs %s fields containing YYYY-MM-DD dates or null; put explanations in the body", timeFieldNames(kind))
	}
	for i := 0; i+1 < len(next.Content); i += 2 {
		key, _ := fieldName(next.Content[i])
		value := dereference(next.Content[i+1])
		if sameFieldValue(metadataField(previous, key), value) {
			continue
		}
		if !slices.Contains(timeKeys(kind), key) {
			if key == "unparsed_previous_time" && previous != nil && sameFieldValue(previous, value) {
				continue // Existing recovery preserves the old raw value here.
			}
			return fmt.Errorf("time.%s is not a date field for %s; use %s", key, kind, timeFieldNames(kind))
		}
		if value != nil && (value.ShortTag() == "!!null" || calendarDate(value)) {
			continue
		}
		return fmt.Errorf("time.%s must be a real YYYY-MM-DD date or null; put date explanations in the body", key)
	}
	if kind == Entities {
		t := recordTime(next, kind)
		if t.Start != nil && t.End != nil && *t.End < *t.Start {
			return fmt.Errorf("time.end cannot be before time.start")
		}
	}
	return nil
}

// A supplied component patches the existing event time; omission preserves it.
// Malformed values follow the ordinary retention path instead of losing data.
func patchRecordTime(previous *yaml.Node, value any, kind Destination) any {
	patch, _ := encodeValue(value)
	if patch.Kind != yaml.MappingNode || len(duplicateFields(patch)) > 0 {
		return value
	}
	base, issue := recordTimeNode(previous, kind)
	if issue != "" {
		// Preserve the old value and any independently readable components.
		// A repaired start must not hide a healthy end (or vice versa).
		raw := dereference(previous)
		if raw != nil && raw.Kind == yaml.MappingNode && len(duplicateFields(raw)) == 0 {
			base, _ = normalizeNode(raw)
			saved := &Document{Frontmatter: *base}
			_ = saved.Set("unparsed_previous_time", previous)
			base = &saved.Frontmatter
		} else {
			base = yamlValue(map[string]any{"unparsed_previous_time": previous})
		}
	}
	doc := &Document{Frontmatter: *base}
	for i := 0; i+1 < len(patch.Content); i += 2 {
		name, ok := fieldName(patch.Content[i])
		if !ok {
			return value
		}
		_ = doc.Set(name, patch.Content[i+1])
	}
	return &doc.Frontmatter
}

func dateValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func recordTime(node *yaml.Node, kind Destination) RecordTime {
	if node != nil && len(duplicateFields(dereference(node))) > 0 {
		return RecordTime{Lifetime: kind == Entities}
	}
	read := func(name string) *string {
		entry := dereference(metadataField(node, name))
		if entry == nil || entry.Kind != yaml.ScalarNode || entry.ShortTag() == "!!null" {
			return nil
		}
		value := strings.TrimSpace(entry.Value)
		if !calendarDate(entry) {
			return nil
		}
		return &value
	}
	if kind == Entities {
		return RecordTime{Start: read("start"), End: read("end"), Lifetime: true}
	}
	return RecordTime{Date: read("date"), Earliest: read("earliest"), Latest: read("latest")}
}

func (t RecordTime) bounds() (string, string) {
	if t.Lifetime {
		return dateValue(t.Start), dateValue(t.End)
	}
	if t.Date != nil {
		if t.Earliest != nil && *t.Earliest != *t.Date || t.Latest != nil && *t.Latest != *t.Date {
			return "", ""
		}
		return *t.Date, *t.Date
	}
	return dateValue(t.Earliest), dateValue(t.Latest)
}

// Unknown fields remain intact for repair rather than losing the account's data.
func recordTimeNode(node *yaml.Node, kind Destination) (*yaml.Node, string) {
	node = dereference(node)
	if node == nil || node.ShortTag() == "!!null" {
		empty := map[string]any{}
		for _, key := range timeKeys(kind) {
			empty[key] = nil
		}
		return yamlValue(empty), ""
	}
	if node.Kind == yaml.ScalarNode {
		if kind != Entities && calendarDate(node) {
			return yamlValue(map[string]any{"date": strings.TrimSpace(node.Value), "earliest": nil, "latest": nil}), ""
		}
		return node, fmt.Sprintf("time needs %s; original value retained", timeFieldNames(kind))
	}
	if node.Kind != yaml.MappingNode {
		return node, "time needs a date object; original value retained"
	}
	if len(duplicateFields(node)) > 0 {
		return node, "duplicate time fields retained for repair"
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, _ := fieldName(node.Content[i])
		if !slices.Contains(timeKeys(kind), key) {
			return node, fmt.Sprintf("unrecognized time field %q retained for repair", key)
		}
		value := dereference(node.Content[i+1])
		if value != nil && value.Kind != yaml.ScalarNode {
			return node, fmt.Sprintf("unreadable time %s retained for repair", key)
		}
		if value != nil && value.ShortTag() != "!!null" && !calendarDate(value) {
			return node, fmt.Sprintf("time %s needs a real YYYY-MM-DD date or null; original value retained for repair", key)
		}
	}
	t := recordTime(node, kind)
	if t.Date != nil {
		if t.Earliest != nil && *t.Earliest != *t.Date || t.Latest != nil && *t.Latest != *t.Date {
			return node, "exact event date conflicts with its range; original values retained"
		}
		t.Earliest, t.Latest = nil, nil
	}
	if kind == Entities && t.Start != nil && t.End != nil && *t.End < *t.Start {
		return node, "time.end is before time.start; original values retained"
	}
	// Keep field comments and annotations while filling the canonical keys.
	result, _ := normalizeNode(node)
	values := t.fields()
	for _, key := range timeKeys(kind) {
		value := values[key]
		previous := field(result, key)
		replacement := yamlValue(nil)
		if value != nil {
			replacement = yamlValue(*value)
		}
		if previous == nil {
			result.Content = append(result.Content, yamlValue(key), replacement)
		} else if value == nil && previous.ShortTag() != "!!null" || value != nil && previous.Value != *value {
			mergeComments(replacement, previous)
			replacement.Anchor = previous.Anchor
			assignNode(previous, replacement)
		}
	}
	return result, ""
}
