package mcpserver

import (
	"context"
	"encoding/json"
	"unicode/utf8"

	"github.com/aristath/songya"
)

type operationKey struct{}

func editsOriginalAccounts(raw json.RawMessage) bool {
	var input map[string]any
	if json.Unmarshal(raw, &input) != nil {
		return false
	}
	var contains func(any) bool
	contains = func(value any) bool {
		switch v := value.(type) {
		case map[string]any:
			if _, exists := v["original_accounts"]; exists {
				return true
			}
			for _, child := range v {
				if contains(child) {
					return true
				}
			}
		case []any:
			for _, child := range v {
				if contains(child) {
					return true
				}
			}
		}
		return false
	}
	return contains(input)
}

func requestKey(ctx context.Context) string { key, _ := ctx.Value(operationKey{}).(string); return key }

// Present the actual accepted records, including reused identities, without
// exposing temporary batch references or asking the model to coordinate writes.
func savedWrite(ctx context.Context, store *songya.Store, receipt songya.Receipt, main string, writeErr error) (any, error) {
	status := receipt.Status
	if status == "" {
		status = "not_accepted"
	}
	value := map[string]any{"status": status, "operation_id": receipt.Key, "diagnostics": receipt.Diagnostics}
	id := receipt.Refs[main]
	value["id"] = id
	diagnostics := append([]songya.Diagnostic{}, receipt.Diagnostics...)
	records := map[string]songya.Record{}
	for _, saved := range receipt.Records {
		if saved.ID == id {
			fields := append([]string{}, saved.ChangedFields...)
			if kindFor(id) == songya.Entities || kindFor(id) == songya.Topics {
				for i, field := range fields {
					if field == "text" {
						fields[i] = "description"
					}
				}
			}
			value["outcome"], value["changed_fields"] = saved.Outcome, fields
			if saved.Outcome == "unchanged" {
				prose := "text"
				if kindFor(id) == songya.Entities || kindFor(id) == songya.Topics {
					prose = "description"
				}
				diagnostics = append(diagnostics, songya.Diagnostic{Path: id, Message: "Nothing changed. To change the words, send " + prose + " with the new words. To add words, send append. To change another field, send that field with its new value. Do not send the same call again. This is not a rejected save."})
			}
		}
		data, err := store.BatchData(ctx, receipt.Key, saved.ID, "proposed")
		if err != nil {
			diagnostics = append(diagnostics, songya.Diagnostic{Path: saved.ID, Message: "Saved snapshot unavailable; read the record by ID: " + err.Error()})
			continue
		}
		doc := songya.Parse(data)
		doc.ID = saved.ID
		records[saved.ID] = doc.Record(kindFor(saved.ID))
	}
	if record, ok := records[id]; ok {
		value["kind"] = record.Kind
		value["type"] = record.Types[songya.TypeUser(ctx)]
		value["type_user"] = songya.TypeUser(ctx)
		if record.Kind == songya.Entities {
			value["time"] = record.Time
		}
		if record.Kind == songya.Entities || record.Kind == songya.Topics {
			value["name"], value["description"], value["aliases"] = record.Title, clipText(record.Body, 2048), record.Aliases
			value["description_truncated"] = len(record.Body) > 2048
		} else {
			value["text"], value["title"], value["text_truncated"] = clipText(record.Body, 2048), record.Title, len(record.Body) > 2048
			if record.User != "" {
				value["user"] = record.User
			}
			if record.Kind == songya.Memories {
				value["time"] = record.Time
			}
		}
		connections := []any{}
		for _, edge := range record.Relations {
			connection := map[string]any{"type": edge.Type, "target": edge.Target}
			target, ok := records[edge.Target]
			if !ok && songya.ValidID(edge.Target) {
				if doc, err := store.Read(edge.Target); err == nil {
					target, ok = doc.Record(kindFor(edge.Target)), true
				}
			}
			if ok {
				connection["name"], connection["kind"] = target.Title, target.Kind
			} else {
				diagnostics = append(diagnostics, songya.Diagnostic{Path: edge.Target, Message: "Connection target unavailable; read the record to repair it"})
			}
			connections = append(connections, connection)
		}
		value["relations"] = connections
	}
	value["diagnostics"] = diagnostics
	return value, writeErr
}
func clipText(text string, n int) string {
	if len(text) <= n {
		return text
	}
	for n > 0 && !utf8.ValidString(text[:n]) {
		n--
	}
	return text[:n] + "…"
}

func memorySchema() map[string]any {
	timing := schema(map[string]any{
		"date":     nullableDate("Exact known day. Use null if the day is not known"),
		"earliest": nullableDate("First possible day or start bound for a lasting state. For since July 2025 use 2025-07-01, with latest:null"),
		"latest":   nullableDate("End bound; null for an ongoing state or unknown end"),
	})
	timing["type"] = []string{"object", "null"}
	timing["description"] = "When the event happened. Omit to keep old dates. Send only the parts to change. null clears a part; time:null clears all dates. Send date:null when replacing an exact day with bounds. For ongoing states, earliest is the start bound and latest is null until an end is known. Use real YYYY-MM-DD dates or null only. Explain uncertainty in text."
	value := schema(map[string]any{
		"id":     str("ID of the memory to change. Omit id for a new memory"),
		"user":   str("Owner of a new memory, unless the app supplies it. Omit when changing an existing memory to keep its owner"),
		"title":  str("Short name for the event or fact. Always send for a NEW memory. Omit on UPDATE to keep the old title"),
		"text":   str("All words for the memory. With id, this replaces ALL old words. Change only the wrong claims. Do not change other wording, details, or quotes. Omit to keep old words. An empty string clears them. Do not put original_accounts here or in metadata"),
		"append": str("Words to add after the old words. Send id too. Do not send text unless you also want to replace the old words"),
		"time":   timing, "metadata": object(), "relations": relationsSchema(),
	})
	// Creation, append and field-only updates share these fields. Describe the
	// modes plainly and enforce them in the handler instead of conditional schemas.
	value["examples"] = []any{
		map[string]any{"title": "John and Mary celebrated a birthday", "text": "John and Mary celebrated Mary's birthday at a restaurant on April 22, 2024.", "user": "example_user", "time": map[string]any{"date": "2024-04-22"}},
		map[string]any{"title": "John moved to Lakeside", "text": "John moved to Lakeside in May 1990. The exact day is unknown.", "user": "example_user", "time": map[string]any{"date": nil, "earliest": "1990-05-01", "latest": "1990-05-31"}, "relations": []any{map[string]any{"type": "related_to", "target": "JOHN_ENTITY_ID"}}},
		map[string]any{"id": "EXISTING_DINNER_ID", "append": "At that same dinner, Jamie also gave Sam a book about trains."},
		map[string]any{"id": "EXISTING_DINNER_ID", "text": "On April 2, 2025, Sam had dinner with Jamie in Lyon. Jamie gave Sam a book about trains."},
		map[string]any{"id": "EXISTING_MEMORY_ID", "time": map[string]any{"date": nil, "earliest": "1990-05-01", "latest": "1990-12-31"}},
		map[string]any{"id": "EXISTING_MEMORY_ID", "relations": []any{map[string]any{"type": "related_to", "target": "EXISTING_ENTITY_ID"}, map[string]any{"type": "related_to", "target": "NEW_ENTITY_ID"}}},
	}
	return value
}

const memoryDescription = `Save a new memory or change an existing memory. First use prepare_memory and read the record you will change.
NEW: omit id; always send a short title and text. Send the selected user unless the app supplies user.
CHANGE: send id and the fields to change. Omitted fields stay the same.
text replaces ALL body words, not the source header. For a correction, change only the wrong claims. Copy all other wording, details, and quotes exactly. append adds details of the SAME event after the old words; a different dated event needs a new memory.
DATE ONLY: send id and time. Omit text to keep the words. Null clears a date part.
LINKS: supplied relations replace old links; with append they add links. [] clears all links.
Keep original quotes exact. Do not edit original_accounts. Check dates, ages, and event order after corrections.
Read outcome and changed_fields. unchanged means nothing changed. Fix missing or wrong arguments; do not repeat the same call. Say done only after checking the saved values.`

func namedSchema(entity bool) map[string]any {
	fields := map[string]any{"create": map[string]any{"type": "boolean", "description": "Set true to create a separate new record, even if its name already exists. Do not supply id with create:true"}, "id": str("Existing record ID required for an update. A name is not an update selector"), "name": str("Name, e.g. Alex Morgan or Cedar Café"), "aliases": array(str("Other known name")), "description": str("Short text saying who or what this is. This replaces ALL old description words. Put events and experiences in linked memories"), "append": str("Words to add after the old description"), "metadata": object(), "relations": relationsSchema()}
	if entity {
		fields["type"] = str("Type from the selected user's configured choices")
		timing := schema(map[string]any{"start": nullableDate("The start date of the entity's existence"), "end": nullableDate("The end date of the entity's existence. null means no date is recorded; it does not prove continued existence")})
		timing["type"] = []string{"object", "null"}
		timing["description"] = "The start and end dates of the entity's existence. These could represent birth and death for a person, founding and closure for an organization, and so on. Apply this meaning to any user-configured entity type. Omit to keep old dates; supplied parts patch only those parts. null clears a part; time:null clears both. Do not use creation timestamps, first mention, or uncertainty bounds as lifetime dates."
		fields["time"] = timing
	}
	value := schema(fields)
	// Keep the model-facing field list simple; the engine enforces the
	// create-vs-update rules before accepting any write.
	value["examples"] = []any{
		map[string]any{"create": true, "name": "Alex", "description": "Sam's coworker, a different person from Sam's cousin Alex."},
		map[string]any{"id": "EXISTING_RECORD_ID", "description": "The corrected short description."},
	}
	if entity {
		value["examples"] = append(value["examples"].([]any), map[string]any{"id": "EXISTING_PERSON_ID", "time": map[string]any{"start": "1990-04-22"}})
	}
	return value
}

func nullableDate(description string) map[string]any {
	// Calendar validation belongs to the engine; keep provider-facing fields
	// simple while stating the exact accepted representation.
	return map[string]any{"type": []string{"string", "null"}, "description": description + "; real YYYY-MM-DD date or null only. No words, months alone, timestamps, or empty strings"}
}

func relationsSchema() map[string]any {
	named := schema(map[string]any{
		"kind":   map[string]any{"type": "string", "enum": []string{"entities", "topics"}},
		"create": map[string]any{"type": "boolean", "description": "true creates a separate new identity and never modifies a same-name record; otherwise use id to update an identity"},
		"name":   str("Name to create or reuse"), "type": str("Optional entity type"),
		"description": str("Brief identity or subject description"), "aliases": array(str("Equivalent name")),
	}, "name")
	value := array(schema(map[string]any{
		"type":   map[string]any{"type": "string", "enum": songya.RelationTypes[:]},
		"target": map[string]any{"anyOf": []any{str("Existing record ID or entity name"), named}},
	}, "type", "target"))
	value["description"] = "List of links. Omit to keep links. With append, links are added. Otherwise this list replaces ALL links. [] clears links. Put story links in reading order."
	return value
}
