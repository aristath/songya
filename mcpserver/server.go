// Package mcpserver exposes the standalone memory engine over MCP stdio.
package mcpserver

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/aristath/songya"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const Guide = `Songya stores connected Markdown records. A record is one saved entry. A field is a named part such as title. An ID is the unique label a tool returns for a record. Copy IDs exactly; never invent them. Examples are instructions, not facts to save.

TYPES
Types belong to the selected user. Read type_configuration for that user before assigning a type unless the app already supplied the choices. Use only configured IDs and their definitions. Omit type if none fits. type_user selects whose classification changes; it does not change the record owner. Empty type clears only that user's assignment. Never invent types, change type settings, write a global metadata.type, or change another user's classifications. Shared identities can have different types for different users.

WHAT TO SAVE WHERE
- Memory: one event WITH its details, or a standalone fact or preference. All people, food, conversation, feelings, and quotes from one dinner belong together. A different dinner is a different memory. An unrelated home address or usual drink preference can be a standalone fact; do not attach it to the dinner. Include all supplied details. Say whose feelings or opinions they are. Do not invent missing details or shorten away important words. One detailed event can take several paragraphs; there is no fixed word limit.
- Entity: a person, place, animal, organization, or named thing. name names it; aliases are other names of that SAME thing; description is a short identification, usually one or two sentences. Example arguments for save_entity:
{"create":true,"name":"Jamie","description":"Sam's former partner. A hardware technician who rides motorcycles."}
Put experiences and detailed history in linked memories, not in this description. The same name alone does not prove two people are the same person. To create a separate entity, send create:true and name, without id; it never modifies a matching name. To update one, send its returned id and leave create out. Use name on an update only to rename it. Two people named Alex must have separate IDs; link by the correct ID.
- Topic: a subject such as Music or Relationships. save_topic also uses create:true and name for a new topic, or id to update one. name names it; description explains the subject, not someone's life. Playing piano supports Music; a breakup supports Relationships; being tired after work does NOT establish Burnout or Depression. Reuse an existing topic with that meaning. If no subject named by the person or activity described in the account supports a topic, leave the topic link out.
- Story: a larger account whose related_to links put memories in reading order. text can give a short introduction; do not copy all the memories into it. Create a story if asked, or if the person names a larger experience AND at least two memories describe separate events within it. Meeting a partner and their breakup can belong to one relationship story; two unrelated dinners do not need a story just because the same friend attended. Reuse a story for the same larger experience and owner. Use requested reading order, otherwise known event order; if unknown, use telling order without claiming it is time order.
Do not rewrite or reorganize existing records just to change their style or structure. Change an existing record's style or structure only when the person asks for that change.

OWNER
An owner is the user whose collection holds a memory/story, not everyone taking part. The selected user ID is a collection label, not automatically a person's name or entity ID. Use it for new memories/stories. Omit user on an update to keep the existing owner; change it only if asked.
Entities/topics are shared identities. Different owners can link to the same identity; do not copy it for each owner. You can find shared identities while searching the selected user's collection.
For owned searches, supply the selected user. Use user="all" only when the person explicitly asks to search everyone's collections. If no user is supplied by the client and the collection is unclear, ask which collection to use.

WORK IN THIS ORDER
1. For a question, read the evidence needed to answer; do not change records merely because someone asked about them. A missing search result does not prove an event never happened.
2. Before a save/correction, use prepare_memory to find possible matches. Compare the owner, event, people, place, and timing. A match is a suggestion, not an instruction to reuse it.
3. Same event plus a correction/extra detail: change that memory. Different event: create a new memory even with the same people. True then but changed later: record the new state separately without overwriting the true past. Already correct: do not save again. If you cannot tell whether accounts describe the same event, ask before changing them.
4. Before changing a record, call read_memory with its id and view="source". Read all pages. Separate the header from the body using the rules below.
5. Send only changed fields. Check the saved kind, ID, fields, and values against what you meant to save, using CHECK below. Do not claim done while a requested change is missing.

CHECK FACTS, INCLUDING NEW INFORMATION
Compare new facts AND corrections with related records. Read the relevant entity dates and event accounts; find a record that a date or age depends on if needed. Check dates, ages, event order, and stated causes. Do not search unrelated collections. Uncertain dates may allow both claims; do not invent an exact date or age.
A conflict alone does not tell you which claim is wrong. If the person clearly corrects an old claim, follow the correction rules. Otherwise leave the disputed old claim unchanged, save any clear new fact in its correct field/record, explain the conflict, and ask which claim needs correcting. If the new fact itself is unclear, ask before saving that part. Continue other clear work. Do not silently rewrite old accounts to make them agree.
Example: a saved meeting on June 1, 2010 says John was 25. The person now gives his birth date as April 22, 1990. Save the clear birth date in the entity time.start. These dates put him at 20 at the meeting; leave that old account unchanged and ask whether its age or date needs correcting.

READING AND PAGES
Use recall_memory to find a person/subject and connected accounts. Use search_memory to search/list; mode=semantic searches by meaning.
Tool reply data may be inside structuredContent.data. A positive number in the field next is the position of another page. Call the SAME tool with that number in offset and other arguments unchanged. Example continuation arguments for read_memory after a reply with {"next":16384}:
{"id":"RECORD_ID","view":"source","offset":16384}
Read ALL pages before replacing words, so unseen details are not deleted. If content is incomplete but no next position is returned, read the needed record by ID or report what you could not read. Do not invent missing words or a page position.

SOURCE VERSUS BODY
In view="source", the normal file header lies between the first two lines containing only ---. It holds fields such as title, user, time, and relations. The body is ALL writing AFTER the second --- line.
Only the body goes in text for memories/stories, or description for entities/topics. Never paste the header block, its field lines, or its --- divider lines into those arguments. Dates, names, and places still belong in ordinary body sentences when part of the account. Send a changed header field in its own argument too.
If the header is damaged or its end is unclear, read view="body" separately. Do not guess a replacement or delete unreadable words; report that record's problem and continue other clear work.

SAVES
Call one tool at a time, with JSON arguments. Do not write pretend calls or XML tags in reply text.
New memory: omit id; always send a short title and text. Send the selected user unless the client supplies user. New stories also need a title. Example: call save_memory with this JSON:
{"title":"Alex and Sam's favorite soup","text":"Alex and Sam enjoy the soup at Cedar Café.","relations":[{"type":"related_to","target":"Alex"},{"type":"related_to","target":"Sam"},{"type":"related_to","target":{"kind":"entities","name":"Cedar Café"}}]}
Change a memory/story: send id and the fields to change. text replaces ALL body words. For a correction, change only wrong claims and copy all other wording, details, names, and quotes exactly. append adds words about the SAME account after the old words; it must not add a different dated event or a conflicting claim.
Change entity/topic words: send id and description, NOT text. For dates only, send id and time; leave text and description out.
Omit means leave a field out: its old value stays. An empty text or description clears the words. metadata means extra named fields; it changes only supplied keys. IDs and save times are automatic.

CORRECTIONS AND QUOTES
Search for the corrected event, its people, and the old claim. Read matching accounts and links. Edit another record only if it repeats the wrong claim or a fact in it is shown wrong by the new information. A shared person/topic/link alone is not a reason to edit it.
A move corrected from 2020 to December 1, 2019 also changes an account saying "When Sam made that move in 2020". It does not change a later party date merely because Sam attended. If that date was worked out from the old move date, report that it needs checking; do not invent a replacement.
Your generated words are not the person's original words. The client attaches exact words separately. Do not set or change original_accounts.
If wrong words are a summary, correct only the wrong claim. If they are a real quote, do not change words inside it. In that SAME memory's body, label the quote as an earlier corrected account and add a separate sentence with the corrected fact. Example body: Earlier account, now corrected: Sam said, “We moved in 2020.” Correction: Sam says the move was on December 1, 2019. Put the corrected date in time too, not just in the chat reply.

DATES AND LINKS
Entity time has start and end: the start and end dates of the entity's existence. These could represent birth and death for a person, founding and closure for an organization, and so on. Apply this meaning to any user-configured entity type; it is not limited to these examples.
Save an entity's known start/end dates with save_entity, not as a separate memory containing only those dates. A narrated event or experience still belongs in a linked memory.
Example arguments for an existing entity:
{"id":"EXISTING_ENTITY_ID","time":{"start":"1990-04-22"}}
Omit a component to keep it; null clears it. time:null clears both. Each component is a real YYYY-MM-DD date or null. null means no date is recorded; it does not prove the entity still exists. Do not use record creation time, first mention, or a range of possible start dates as a lifetime.
Do not invent missing date parts. For a partial entity date, append the supplied words to the entity and leave the unknown date unset. Do not clear a known date because a new message is less precise.
For memories, date/earliest/latest describe event timing and uncertainty, not the entity's existence.
Exact day example arguments for save_memory:
{"id":"MOVE_ID","time":{"date":"2019-12-01","earliest":null,"latest":null}}
Month-only example arguments for save_memory:
{"id":"EVENT_ID","time":{"date":null,"earliest":"2019-11-01","latest":"2019-11-30"}}
For a one-time event, these bounds are first/last possible days, not known exact days. A range alone does not prove an event lasted for the whole range.
For an ongoing state SINCE July 2025, earliest is the start bound and latest is the end bound. Use an open end, not July 31. Example creation arguments for a NEW changed preference (replace SELECTED_USER with the selected user ID):
{"user":"SELECTED_USER","title":"Sam prefers tea since July 2025","text":"Since July 2025, Sam prefers tea to coffee.","time":{"date":null,"earliest":"2025-07-01","latest":null}}
Do not change the earlier true preference. If correcting the start of this same already-saved state, send its returned id and these time fields instead. The body says "Since July 2025"; July 1 is a bound, not an asserted exact starting day.
All three time fields accept ONLY real YYYY-MM-DD dates or null. Never put "ongoing", "since July", "unknown", "last summer", a bare month/year, timestamps, or empty strings there. Put these explanations in the body. For an event IN July, use July 1 through July 31 as possible bounds; for a state SINCE July, leave the end null.
A date bound worked out from known facts is an inferred bound; explain those facts in the memory body. Check ages and event order too. Do not guess an exact day or an age.
Omitted time parts stay the same. null clears a part; time:null clears all dates. Clear date when changing an exact day to bounds.
Use relations to link records. related_to links relevant people/places/topics/memories. after points from the later event to the earlier one. caused_by points from an event to its stated cause; order alone does not prove a cause. part_of points from a smaller event to its larger event. possibly_same_as marks uncertain identity without merging records.
Example link-list replacement arguments for save_memory:
{"id":"MEMORY_ID","relations":[{"type":"related_to","target":"EXISTING_ID"}]}
That example leaves only one link. Do not use that list if other links must remain; include those links too. A target can also be an entity name or {"kind":"entities","name":"Name","description":"Brief identification"}; use kind="topics" for a topic. Example ID labels are placeholders; substitute real tool-returned IDs.
Omitted relations keep old links. Supplied relations replace ALL old links unless append is nonempty, in which case they add links. [] clears all links. Include old links to keep when replacing a link list. Reverse links are automatic.

CHECK
For each fact, choose its record kind, existing ID or new record, field, and value using the rules above. Compare these choices with the actual save result, not just a successful tool call:
1. status must be committed to confirm a finished write. Report pending/blocked work as unfinished.
2. Check kind and id. An update must use the intended existing record. A new record does not correct the old one.
3. Check outcome, changed_fields, and the actual field values. A date only in text does not fill a date field. A birthday-only memory does not fill the entity time.start.
4. Check preserved words and links too. A new memory/story needs a nonempty title; add a missing title with an id-and-title update.
5. If needed values are missing or words are cut short, use read_memory with the returned id and view="source" and read the needed pages. Missing output is not proof that a field was saved.
created/updated means changes were made, not that they were correct. unchanged means nothing changed, not a rejected save. If the requested values were already correct, leave them alone and say so. Fix missing/wrong arguments; never repeat the same unchanged call. Report errors, missing requested changes, and unresolved conflicts without inventing a cause. Do not claim relevant records agree unless you checked them.`

// App keeps import access separate from record-store access. Import roots are
// supplied by the process owner, never accepted as arbitrary client paths.
type App struct {
	Store       *songya.Store
	ImportRoots map[string]string
}

type pageInput struct {
	Key     string `json:"key,omitempty"`
	Archive string `json:"archive,omitempty"`
	ID      string `json:"id,omitempty"`
	Source  string `json:"source,omitempty"`
	Root    string `json:"root,omitempty"`
	View    string `json:"view,omitempty"`
	Offset  int    `json:"offset,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}
type recordPage struct {
	User          string   `json:"user,omitempty"`
	UserTruncated bool     `json:"user_truncated,omitempty"`
	ID            string   `json:"id,omitempty"`
	Source        string   `json:"source,omitempty"`
	Pending       bool     `json:"pending,omitempty"`
	Unconfirmed   bool     `json:"unconfirmed,omitempty"`
	Text          string   `json:"text,omitempty"`
	Base64        string   `json:"base64,omitempty"`
	Offset        int      `json:"offset"`
	Next          int      `json:"next,omitempty"`
	Truncated     bool     `json:"truncated"`
	Diagnostics   []string `json:"diagnostics,omitempty"`
}

func normalize(value any, key string, kind songya.Destination) any {
	if key == "time" && value == nil {
		if kind == songya.Entities {
			return map[string]any{"start": nil, "end": nil}
		}
		return map[string]any{"date": nil, "earliest": nil, "latest": nil}
	}
	if key == "relations" {
		switch v := value.(type) {
		case string:
			value = []any{map[string]any{"type": "related_to", "target": v}}
		case map[string]any:
			value = []any{v}
		case []any:
			for i, item := range v {
				if text, ok := item.(string); ok {
					v[i] = map[string]any{"type": "related_to", "target": text}
				}
			}
		}
	}

	if key == "metadata" {
		if value == nil {
			return nil
		}
		if _, ok := value.(map[string]any); ok {
			return value
		}
		return map[string]any{"unparsed_metadata": value}
	}
	if number, ok := value.(json.Number); ok {
		value = number.String()
	}
	switch value := value.(type) {
	case map[string]any:
		if key == "target" || key == "" {
			id, _ := value["id"].(string)
			if value["kind"] == "entities" || songya.ValidID(id) && strings.HasPrefix(id, "e_") {
				kind = songya.Entities
			}
		}
		for name, item := range value {
			value[name] = normalize(item, name, kind)
		}
		return value
	case []any:
		for i, item := range value {
			value[i] = normalize(item, "", kind)
		}
		return value
	}
	switch key {
	case "offset", "limit", "depth", "start", "end":
		switch v := value.(type) {
		case string:
			text := strings.TrimSpace(v)
			if n, err := strconv.ParseInt(text, 10, 0); err == nil {
				return int(n)
			}
			n, err := strconv.ParseFloat(text, 64)
			maximum := int(^uint(0) >> 1)
			if key == "start" || key == "end" {
				// A reviewed source boundary cannot be guessed from a non-number, fraction,
				// or overflow. Leaving it unconverted produces an actionable decode error.
				if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n != math.Trunc(n) || n >= float64(maximum) || n < float64(-maximum-1) {
					return v
				}
				return int(n)
			}
			if math.IsInf(n, 1) {
				return maximum
			}
			if math.IsInf(n, -1) {
				return -maximum - 1
			}
			if err != nil || math.IsNaN(n) {
				return 0
			}
			if n >= float64(maximum) {
				return maximum
			}
			if n <= float64(-maximum-1) {
				return -maximum - 1
			}
			return int(n)
		}
	case "user", "key", "original_text", "name", "description", "title", "relation", "id", "kind", "ref", "body", "append", "story", "text", "direction", "type", "related_to", "action", "root", "path", "source", "view", "archive", "relation_type", "target", "mode":
		switch v := value.(type) {
		case bool:
			return strconv.FormatBool(v)
		}
	}
	return value
}
func decode(raw json.RawMessage, target any) error { return decodeRecord(raw, target, songya.Memories) }
func decodeRecord(raw json.RawMessage, target any, kind songya.Destination) error {
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	var input any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&input); err != nil {
		return err
	}
	data, err := json.Marshal(normalize(input, "", kind))
	if err != nil {
		return err
	}
	decoder = json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	return decoder.Decode(target)
}
func limit(value, maximum, defaultValue int) int {
	if value <= 0 {
		return defaultValue
	}
	if value > maximum {
		return maximum
	}
	return value
}
func schema(properties map[string]any, required ...string) map[string]any {
	value := map[string]any{"type": "object", "properties": properties}
	if len(required) > 0 {
		value["required"] = required
	}
	return value
}

func str(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}
func count() map[string]any {
	return map[string]any{"type": []string{"integer", "string"}, "description": "Number for page size, byte offset, or link depth. A numeric string also works."}
}
func object() map[string]any {
	return map[string]any{"type": "object", "additionalProperties": true, "description": "Extra fields to change. Omitted fields stay the same. Do not set IDs, save times, or original_accounts."}
}

func array(item any) map[string]any { return map[string]any{"type": "array", "items": item} }

func tool[T any](server *mcp.Server, name, description string, input any, readOnly bool, handler func(context.Context, T) (any, error)) {
	closed := false
	server.AddTool(&mcp.Tool{Name: name, Description: description, InputSchema: input, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readOnly, OpenWorldHint: &closed}}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if !readOnly && editsOriginalAccounts(request.Params.Arguments) {
			return result(nil, fmt.Errorf("original_accounts holds the person's exact words. Do not change it. Put corrected memory words in text, or entity/topic words in description. The app saves the original message separately")), nil
		}
		var value T
		kind := songya.Memories
		if name == "save_entity" {
			kind = songya.Entities
		}
		if err := decodeRecord(request.Params.Arguments, &value, kind); err != nil {
			return result(nil, fmt.Errorf("cannot interpret arguments: %w", err)), nil
		}
		if key, ok := request.Params.Meta["songya/request-key"].(string); ok && key != "" {
			ctx = context.WithValue(ctx, operationKey{}, key)
		}
		if text, ok := request.Params.Meta["songya/original-text"].(string); ok {
			ctx = songya.WithOriginalText(ctx, text)
		}
		var classification struct {
			User  string `json:"type_user"`
			Owner string `json:"user"`
		}
		_ = json.Unmarshal(request.Params.Arguments, &classification)
		if classification.User == "" && classification.Owner != "all" {
			classification.User = classification.Owner
		}
		if classification.User != "" && songya.TypeUser(ctx) == "" {
			ctx = songya.WithTypeUser(ctx, classification.User)
		}
		if user, ok := request.Params.Meta["songya/user"].(string); ok {
			ctx = songya.WithTypeUser(ctx, user)
		}
		output, err := handler(ctx, value)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return result(output, err), nil
	})
}

func result(value any, err error) *mcp.CallToolResult {
	summary := "ok"
	if saved, ok := value.(map[string]any); ok {
		if outcome, ok := saved["outcome"].(string); ok {
			summary = outcome
		}
	}
	if receipt, ok := value.(songya.Receipt); ok {
		if receipt.Status == "" {
			receipt.Status = "not_accepted"
		}
		summary = receipt.Status
		value = receiptPage(receipt, 0, 10)
	}
	envelope := map[string]any{"status": "ok", "data": value}
	if err != nil {
		code := "blocked"
		if errors.Is(err, songya.ErrConflict) {
			code = "conflict"
		}
		envelope["status"] = code
		envelope["error"] = clipError(err.Error())
	}
	payload, encodeErr := json.Marshal(envelope)
	if encodeErr != nil {
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "Result encoding failed; inspect the durable request receipt before retrying."}}}
	}
	var bounded map[string]any
	_ = json.Unmarshal(payload, &bounded)
	trimDiagnostics(bounded)
	payload, _ = json.Marshal(bounded)
	// Page by the encoded wire budget too: control characters may expand sixfold.
	// Keep both prose and its continuation instead of replacing a readable page.
	for len(payload) > 28*1024 {
		data, ok := bounded["data"].(map[string]any)
		if !ok {
			break
		}
		offset, ok := data["offset"].(float64)
		if !ok {
			break
		}
		text, _ := data["text"].(string)
		raw := []byte(text)
		if encoded, ok := data["base64"].(string); ok {
			raw, _ = base64.StdEncoding.DecodeString(encoded)
		}
		if len(raw) < 2 {
			break
		}
		part := bytePage(raw, 0, len(raw)/2)
		if !part.Truncated {
			break
		}
		delete(data, "text")
		delete(data, "base64")
		if part.Base64 != "" {
			data["base64"] = part.Base64
		} else {
			data["text"] = part.Text
		}
		data["next"] = offset + float64(part.Next)
		data["truncated"] = true
		payload, _ = json.Marshal(bounded)
	}
	if len(payload) > 28*1024 {
		bounded = map[string]any{"status": envelope["status"], "data": map[string]any{"truncated": true, "message": "Result exceeds the response budget. Use a smaller limit or read the receipt with view=source. The durable outcome is unchanged."}}
		if receipt, ok := value.(map[string]any); ok {
			data := bounded["data"].(map[string]any)
			for _, key := range []string{"status", "key", "key_hash", "key_truncated", "record_count", "batch_key", "id", "operation_id"} {
				if item, exists := receipt[key]; exists {
					data[key] = item
				}
			}
		}
		if err != nil {
			bounded["error"] = clipError(err.Error())
		}
	}
	if err != nil {
		summary = fmt.Sprint(envelope["status"])
	}
	return &mcp.CallToolResult{StructuredContent: bounded, IsError: err != nil, Content: []mcp.Content{&mcp.TextContent{Text: summary}}}
}
func clipError(value string) string {
	if len(value) > 1024 {
		return value[:1024]
	}
	return value
}
func trimDiagnostics(value any) {
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			if key == "diagnostics" {
				if items, ok := item.([]any); ok && len(items) > 4 {
					v[key] = items[:4]
					v["diagnostics_truncated"] = true
				}
			} else {
				trimDiagnostics(item)
			}
		}
	case []any:
		for _, item := range v {
			trimDiagnostics(item)
		}
	}
}

func bytePage(data []byte, offset, size int) recordPage {
	if offset < 0 {
		offset = 0
	}
	if offset > len(data) {
		offset = len(data)
	}
	size = limit(size, 12288, 8192)
	if utf8.Valid(data) {
		for offset > 0 && offset < len(data) && !utf8.RuneStart(data[offset]) {
			offset--
		}
	}
	end := offset + size
	if end > len(data) {
		end = len(data)
	}
	if utf8.Valid(data) && end < len(data) {
		for end > offset && !utf8.RuneStart(data[end]) {
			end--
		}
		if end == offset {
			_, n := utf8.DecodeRune(data[offset:])
			end = offset + n
		}
	}
	page := recordPage{Offset: offset, Truncated: end < len(data)}
	if utf8.Valid(data[offset:end]) {
		page.Text = string(data[offset:end])
	} else {
		page.Base64 = base64.StdEncoding.EncodeToString(data[offset:end])
	}
	if page.Truncated {
		page.Next = end
	}
	return page
}
func (a *App) read(ctx context.Context, input pageInput) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Canonical source paths use the same record view and pending status as IDs.
	// Damaged or misplaced filenames remain inspectable by their literal source.
	if input.ID == "" && input.Key == "" && input.Archive == "" && input.Root == "" {
		parts := strings.Split(input.Source, "/")
		if len(parts) == 2 && strings.HasSuffix(parts[1], ".md") {
			id := strings.TrimSuffix(parts[1], ".md")
			if songya.ValidID(id) && string(kindFor(id)) == parts[0] {
				input.ID = id
				input.View = "source"
			}
		}
	}
	var data []byte
	var err error
	user := ""
	pending := false
	unconfirmed := false
	var diagnostics []string
	if input.Key != "" {
		view := input.View
		if view == "original_text" {
			view = "account"
		}
		if view == "" {
			view = "proposed"
		}
		data, err = a.Store.BatchData(ctx, input.Key, input.ID, view)
	} else if input.Archive != "" {
		data, err = a.Store.ReadArchive(input.Archive)
	} else if input.Root != "" {
		root, ok := a.ImportRoots[input.Root]
		if !ok {
			return nil, fmt.Errorf("import root %q is not configured", input.Root)
		}
		data, _, err = a.Store.ImportSource(root, input.Source)
	} else if input.ID != "" {
		doc, readErr := a.Store.Read(input.ID)
		if readErr != nil {
			return nil, readErr
		}
		pending = doc.Pending
		unconfirmed = doc.Unconfirmed
		diagnostics = doc.Diagnostics()
		owned := kindFor(input.ID) == songya.Memories || kindFor(input.ID) == songya.Stories
		if owned {
			user = doc.Metadata().User
		}
		if input.View == "source" {
			source := string(kindFor(input.ID)) + "/" + input.ID + ".md"
			data, err = a.Store.ReadSource(source)
			if err == nil && owned {
				user = songya.Parse(data).Metadata().User
			}
		} else {
			data = []byte(doc.Body)
		}
	} else {
		data, err = a.Store.ReadSource(input.Source)
	}
	if err != nil {
		return nil, err
	}
	page := bytePage(data, input.Offset, input.Limit)
	page.UserTruncated = len(user) > 160
	if page.UserTruncated {
		user = user[:160]
	}
	page.User = user
	page.ID, page.Source, page.Pending, page.Unconfirmed = input.ID, input.Source, pending, unconfirmed
	for _, warning := range diagnostics {
		if len(page.Diagnostics) == 4 {
			break
		}
		page.Diagnostics = append(page.Diagnostics, clipError(warning))
	}
	return page, nil
}
func kindFor(id string) songya.Destination {
	switch id[:2] {
	case "m_":
		return songya.Memories
	case "e_":
		return songya.Entities
	case "t_":
		return songya.Topics
	default:
		return songya.Stories
	}
}

func New(lifetime context.Context, store *songya.Store, roots map[string]string) *mcp.Server {
	return NewForUser(lifetime, store, roots, "")
}
func NewForUser(lifetime context.Context, store *songya.Store, roots map[string]string, user string) *mcp.Server {
	cfg := songya.TypeConfiguration{User: user, Kinds: map[songya.Destination][]songya.TypeDefinition{}}
	instructions := Guide
	if user != "" {
		var err error
		cfg, err = store.TypeConfiguration(lifetime, user)
		if err != nil {
			instructions += "\nType settings could not be read. Omit classifications and report the problem: " + err.Error()
		} else {
			instructions += "\n" + songya.TypeInstructions(cfg)
		}
	}
	writeSchemas := WriteSchemas(cfg)
	app := &App{Store: store, ImportRoots: roots}
	server := mcp.NewServer(&mcp.Implementation{Name: "songya", Version: "0.1.0"}, &mcp.ServerOptions{Instructions: instructions})
	// The SDK keeps request contexts independent of Run's context. Propagate our
	// process lifetime explicitly so shutdown cancels unaccepted lock waiters.
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
			ctx, cancel := context.WithCancel(ctx)
			stop := context.AfterFunc(lifetime, cancel)
			defer stop()
			defer cancel()
			if err := lifetime.Err(); err != nil {
				return nil, err
			}
			if user != "" {
				ctx = songya.WithTypeUser(ctx, user)
			}
			return next(ctx, method, request)
		}
	})
	tool(server, "type_configuration", "Read the selected user's type choices and definitions before assigning types. Settings changes are made by the person in Songya, not by the model.", schema(map[string]any{"user": str("Selected user whose type choices to read"), "schemas": map[string]any{"type": "boolean", "description": "App integration flag. Omit when reading the choices normally"}}, "user"), true, func(ctx context.Context, input struct {
		User    string `json:"user"`
		Schemas bool   `json:"schemas"`
	}) (any, error) {
		cfg, err := store.TypeConfiguration(ctx, input.User)
		if err != nil {
			return cfg, err
		}
		value := map[string]any{"user": cfg.User, "kinds": cfg.Kinds, "version": cfg.Version, "diagnostics": cfg.Diagnostics, "instructions": songya.TypeInstructions(cfg)}
		if input.Schemas {
			value["write_schemas"] = WriteSchemas(cfg)
		}
		return value, nil
	})
	tool(server, "prepare_memory", "Find existing memories, entities and topics related to text.", schema(map[string]any{"text": str("The memory or subject to search"), "user": str("Selected user to search. Supply that user. Use all only when the person explicitly asks to search everyone. Omitted user searches everyone; do not omit it for a selected-user search. Shared entities/topics are still included."), "limit": count()}, "text"), true, func(ctx context.Context, input struct {
		Text  string `json:"text"`
		User  string `json:"user"`
		Limit int    `json:"limit"`
	}) (any, error) {
		prepared, err := store.Prepare(ctx, input.Text, input.User, limit(input.Limit, 8, 8))
		return map[string]any{"query": prepared.Query, "user": prepared.User, "matches": prepared.Matches, "ambiguities": prepared.Ambiguities, "possible_duplicates": prepared.PossibleDuplicates, "diagnostics": prepared.Diagnostics, "truncated": prepared.Truncated, "next": prepared.Next}, err
	})
	tool(server, "save_memory", memoryDescription, writeSchemas["save_memory"], false, func(ctx context.Context, input songya.MemoryInput) (any, error) {
		if input.ID == "" && (input.Text == nil || strings.TrimSpace(*input.Text) == "") && strings.TrimSpace(input.Append) == "" {
			return map[string]any{"status": "not_accepted"}, fmt.Errorf("provide the new memory's content in text; to modify an existing memory, supply id and the fields to change")
		}
		input.Key = requestKey(ctx)
		receipt, err := store.SaveMemory(ctx, input)
		return savedWrite(ctx, store, receipt, "memory", err)
	})
	for _, item := range []struct {
		name        string
		kind        songya.Destination
		description string
	}{{"save_entity", songya.Entities, "Save a named entity. time.start and time.end record the beginning and end of its existence, for any entity type. Send id to change an existing entity. Send description to change its words; text does not work here. Put events in linked memories. For a new record, send create:true and name, without id. For an update, send its id; a name alone never updates a record."}, {"save_topic", songya.Topics, "Save a subject. Send id to change an existing topic. Send description to change its words; text does not work here. For a new record, send create:true and name, without id. For an update, send its id; a name alone never updates a record."}} {
		inputSchema := writeSchemas[item.name]
		tool(server, item.name, item.description, inputSchema, false, func(ctx context.Context, input songya.NamedInput) (any, error) {
			input.Key = requestKey(ctx)
			receipt, err := store.SaveNamed(ctx, item.kind, input)
			return savedWrite(ctx, store, receipt, "record", err)
		})
	}
	tool(server, "save_story", "Save a story. Send id to change one. text replaces all story words. Put related_to links to its memories in reading order.", writeSchemas["save_story"], false, func(ctx context.Context, input songya.StoryInput) (any, error) {
		input.Key = requestKey(ctx)
		receipt, err := store.SaveStory(ctx, input)
		return savedWrite(ctx, store, receipt, "story", err)
	})
	querySchema := schema(map[string]any{"user": str("Selected user to search. Supply that user. Use all only when the person explicitly asks to search everyone. Omitted user searches everyone; do not omit it for a selected-user search. Shared entities/topics are still included."), "text": str("Search text; omit to list"), "kind": str("Record directory: memories, entities, topics or stories. Use entities for entity lookup."), "related_to": str("Optional outgoing target ID"), "relation_type": str("Optional relationship type"), "include_retired": map[string]any{"type": "boolean"}, "offset": count(), "limit": count()})
	searchProperties := maps.Clone(querySchema["properties"].(map[string]any))
	searchProperties["mode"] = str("lexical finds words (default). semantic finds similar meaning")
	searchSchema := schema(searchProperties)

	tool(server, "recall_memory", "Find a person, place, or subject by name or ID. Read its connected memories and stories. If next is present, send next as offset with the same text and user to read more.", schema(map[string]any{"text": str("Name, subject or record ID to recall"), "user": str("Selected user to search. Supply that user. Use all only when the person explicitly asks to search everyone. Omitted user searches everyone; do not omit it for a selected-user search. Shared entities/topics are still included."), "offset": count(), "limit": count()}, "text"), true, func(ctx context.Context, input songya.RecallQuery) (any, error) {
		page, err := store.Recall(ctx, input)

		return page, err
	})
	tool(server, "search_memory", "Find records by words. Use mode=semantic to search by meaning. To find a person, use kind=entities and their name in text. Omit text to list records. Follow next for more results.", searchSchema, true, func(ctx context.Context, input songya.Query) (any, error) {
		input.Limit = limit(input.Limit, 8, 8)
		return store.Search(ctx, input)
	})
	tool(server, "read_memory", "Read a record by id. Use view=source to read all fields and words before editing. If next is present, send it as offset with the same id and view to read more.", schema(map[string]any{"id": str("Record ID"), "source": str("Relative source path"), "root": str("Configured import root name; omitted for the store"), "key": str("Saved request key for proposed, before or original_text views"), "archive": str("Preserved import archive checksum"), "view": str("body (default) or source; with key use proposed, before or original_text"), "offset": count(), "limit": count()}), true, app.read)
	tool(server, "explore_memory", "Read links from or to a record. direction is outgoing, incoming, or both. depth is how many links to follow. If a record was split, check all replacements; do not guess one.", schema(map[string]any{"user": str("Selected user to search. Supply that user. Use all only when the person explicitly asks to search everyone. Omitted user searches everyone; do not omit it for a selected-user search. Shared entities/topics are still included."), "id": str("Starting ID; omit for paged edges"), "direction": str("incoming, outgoing or both"), "type": str("Optional edge type"), "depth": count(), "offset": count(), "limit": count()}), true, func(ctx context.Context, input songya.GraphQuery) (any, error) {
		input.Limit = limit(input.Limit, 20, 10)
		return store.Graph(ctx, input)
	})
	tool(server, "memory_timeline", "Read events in time order using dates and links. Check diagnostics for conflicts. Unknown dates do not become exact dates. The tool does not guess unknown order.", querySchema, true, func(ctx context.Context, input songya.Query) (any, error) {
		input.Limit = limit(input.Limit, 8, 8)
		return store.Timeline(ctx, input)
	})
	tool(server, "compose_story", "Read a story and its linked memories in reading order. Follow next as offset to read more. Missing records and loops are reported.", schema(map[string]any{"id": str("Story ID"), "offset": count(), "limit": count()}, "id"), true, func(ctx context.Context, input pageInput) (any, error) {
		return store.Compose(ctx, input.ID, input.Offset, limit(input.Limit, 12288, 8192))
	})
	type organization struct {
		Action   string             `json:"action"`
		Sources  []string           `json:"sources"`
		Source   string             `json:"source"`
		Kind     songya.Destination `json:"kind"`
		Metadata map[string]any     `json:"metadata"`
		Text     *string            `json:"text"`
		Parts    []struct {
			Kind     songya.Destination `json:"kind"`
			Metadata map[string]any     `json:"metadata"`
			Text     string             `json:"text"`
		} `json:"parts"`
	}
	part := schema(map[string]any{"kind": str("Record kind"), "metadata": object(), "text": str("Part narrative")}, "text")
	tool(server, "organize_memory", "Join records with action=merge and sources. Split one record with action=split, source, and parts. Read records first. Old words remain readable.", schema(map[string]any{"action": str("merge or split"), "sources": array(str("Source ID to merge")), "source": str("Source ID to split"), "kind": str("Kind of the merged record"), "metadata": object(), "text": str("Merged narrative"), "parts": array(part)}, "action"), false, func(ctx context.Context, input organization) (any, error) {
		key := requestKey(ctx)
		if key == "" {
			key = "organize_" + rand.Text()
		}
		var receipt songya.Receipt
		var err error
		switch input.Action {
		case "merge":
			sources := []songya.Version{}
			for _, id := range input.Sources {
				sources = append(sources, songya.Version{ID: id})
			}
			receipt, err = store.Merge(ctx, songya.MergeRequest{Key: key, Sources: sources, Kind: input.Kind, Fields: input.Metadata, Body: input.Text})
		case "split":
			parts := []songya.Part{}
			for _, part := range input.Parts {
				parts = append(parts, songya.Part{Kind: part.Kind, Fields: part.Metadata, Body: part.Text})
			}
			receipt, err = store.Split(ctx, songya.SplitRequest{Key: key, Source: songya.Version{ID: input.Source}, Parts: parts})
		default:
			return nil, fmt.Errorf("choose merge or split")
		}
		value := map[string]any{"status": receipt.Status, "operation_id": receipt.Key, "diagnostics": receipt.Diagnostics}
		if input.Action == "merge" {
			value["id"] = receipt.Refs["merged"]
		} else {
			ids := []string{}
			for i := range input.Parts {
				ids = append(ids, receipt.Refs[fmt.Sprintf("part_%d", i+1)])
			}
			value["ids"] = ids
		}
		return value, err
	})
	type reviewedPart struct {
		Start  *int               `json:"start"`
		End    *int               `json:"end"`
		Kind   songya.Destination `json:"kind"`
		Fields map[string]any     `json:"metadata"`
	}
	type importInput struct {
		User   string         `json:"user"`
		Action string         `json:"action"`
		Root   string         `json:"root"`
		Path   string         `json:"path"`
		Parts  []reviewedPart `json:"parts"`
		Offset int            `json:"offset"`
		Limit  int            `json:"limit"`
	}
	importPart := schema(map[string]any{"start": count(), "end": count(), "kind": str("Optional directory"), "metadata": object()}, "start", "end")
	tool(server, "import_memories", "Preview or apply imports from an explicitly configured root. Parts are exact reviewed byte ranges; omitting parts preserves a whole file. Originals, mappings and coverage are retained.", schema(map[string]any{"user": str("Default owner for imported memories/stories; part metadata.user can override it."), "action": str("preview or apply"), "root": str("Configured root name"), "path": str("Relative source file"), "parts": array(importPart), "offset": count(), "limit": count()}, "action", "root"), false, func(ctx context.Context, input importInput) (any, error) {
		root, ok := roots[input.Root]
		if !ok {
			return nil, fmt.Errorf("import root %q is not configured", input.Root)
		}
		switch input.Action {
		case "preview":
			return store.PreviewImports(ctx, root, input.Offset, limit(input.Limit, 8, 8))
		case "apply":
			parts := []songya.ImportPart{}
			for _, part := range input.Parts {
				if part.Start == nil || part.End == nil {
					return nil, fmt.Errorf("each reviewed import part needs start and end byte offsets; omit parts to import the whole source")
				}
				parts = append(parts, songya.ImportPart{Start: *part.Start, End: *part.End, Kind: part.Kind, Fields: part.Fields})
			}
			mapping, err := store.Import(ctx, root, songya.ImportRequest{User: input.User, Path: input.Path, Parts: parts})
			return importPage(mapping, input.Offset, limit(input.Limit, 20, 10)), err
		default:
			return nil, fmt.Errorf("choose preview or apply")
		}
	})
	tool(server, "memory_maintenance", "Check problems with action=status. Check a save with action=receipt and key. recover resumes saved work. rebuild refreshes the search index. embed refreshes vectors. abandon stops unfinished work. Do not repeat an accepted save with a new key.", schema(map[string]any{"action": str("status, receipt, recover, abandon, rebuild or embed"), "key": str("Request key for receipt or deliberate abandonment"), "view": str("source returns a byte page of the full receipt"), "offset": count(), "limit": count()}, "action"), false, func(ctx context.Context, input struct {
		Action string `json:"action"`
		View   string `json:"view"`
		Key    string `json:"key"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}) (any, error) {
		switch input.Action {
		case "status":
			return map[string]any{"diagnostics": store.Diagnostics(), "import_roots": rootNames(roots)}, nil
		case "receipt":
			receipt, err := store.BatchStatus(ctx, input.Key)
			if err != nil {
				return receipt, err
			}
			if input.View == "source" {
				data, err := json.Marshal(map[string]any{"operation_id": receipt.Key, "status": receipt.Status, "records": receipt.Records, "diagnostics": receipt.Diagnostics})
				if err != nil {
					return nil, err
				}
				page := bytePage(data, input.Offset, input.Limit)
				return page, nil
			}
			return receiptPage(receipt, input.Offset, limit(input.Limit, 20, 10)), nil
		case "abandon":
			return store.AbandonBatch(ctx, input.Key)
		case "recover":
			err := store.Recover()
			return map[string]any{"diagnostics": store.Diagnostics()}, err
		case "embed":
			return store.IndexEmbeddings(ctx)
		case "rebuild":
			catalog, err := store.Rebuild(ctx)
			return map[string]any{"records": len(catalog.Records), "diagnostics": catalog.Diagnostics}, err
		default:
			return nil, fmt.Errorf("choose status, receipt, recover, abandon, rebuild or embed")
		}
	})
	server.AddResource(&mcp.Resource{URI: "memory://guide", Name: "Memory engine guide", MIMEType: "text/markdown"}, func(_ context.Context, request *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: "memory://guide", MIMEType: "text/markdown", Text: Guide}}}, nil
	})
	server.AddResourceTemplate(&mcp.ResourceTemplate{URITemplate: "memory://records/{id}/{offset}", Name: "Paged canonical record", MIMEType: "application/json"}, func(ctx context.Context, request *mcp.ReadResourceRequest) (output *mcp.ReadResourceResult, err error) {
		defer func() {
			if err != nil {
				err = errors.New(clipError(err.Error()))
			}
		}()
		uri, err := url.Parse(request.Params.URI)
		if err != nil {
			return nil, err
		}
		parts := strings.Split(strings.Trim(uri.Path, "/"), "/")
		if uri.Host != "records" || len(parts) != 2 {
			return nil, fmt.Errorf("use memory://records/id/offset")
		}
		offset, err := strconv.Atoi(parts[1])
		if err != nil {
			return nil, err
		}
		value, err := app.read(ctx, pageInput{ID: parts[0], Offset: offset, View: "source"})
		if err != nil {
			return nil, err
		}
		page := value.(recordPage)
		// The source itself is available here; parsed diagnostics belong to the read
		// tool. Avoid duplicating them inside a second JSON-encoded resource envelope.
		page.Diagnostics = nil
		data, _ := json.Marshal(page)
		canonical := "memory://records/" + parts[0] + "/" + strconv.Itoa(page.Offset)
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: canonical, MIMEType: "application/json", Text: string(data)}}}, nil
	})
	return server
}
func rootNames(roots map[string]string) []string {
	names := []string{}
	for name := range roots {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
func receiptPage(receipt songya.Receipt, offset, size int) any {
	if offset < 0 {
		offset = 0
	}
	if offset > len(receipt.Records) {
		offset = len(receipt.Records)
	}
	size = limit(size, 20, 10)
	end := offset + min(size, len(receipt.Records)-offset)
	value := map[string]any{"status": receipt.Status, "records": receipt.Records[offset:end], "record_count": len(receipt.Records), "truncated": end < len(receipt.Records), "diagnostics": receipt.Diagnostics}
	if len(receipt.Key) <= 512 {
		value["key"] = receipt.Key
	} else {
		value["key_hash"] = memoryRevision([]byte(receipt.Key))
		value["key_truncated"] = true
	}
	if end < len(receipt.Records) {
		value["next"] = end
	}
	return value
}

func importPage(mapping songya.ImportMapping, offset, size int) any {
	if offset < 0 {
		offset = 0
	}
	if offset > len(mapping.Parts) {
		offset = len(mapping.Parts)
	}
	end := offset + min(size, len(mapping.Parts)-offset)
	value := map[string]any{"source": mapping.Source, "archive": mapping.Archive, "batch_key": mapping.BatchKey, "status": mapping.Status, "parts": mapping.Parts[offset:end], "part_count": len(mapping.Parts), "truncated": end < len(mapping.Parts), "coverage_complete": mapping.CoverageComplete, "uncovered": mapping.Uncovered[:min(20, len(mapping.Uncovered))], "uncovered_truncated": len(mapping.Uncovered) > 20, "diagnostics": mapping.Diagnostics}
	if end < len(mapping.Parts) {
		value["next"] = end
	}
	return value
}

func memoryRevision(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }
