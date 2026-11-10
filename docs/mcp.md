# MCP guide

[Getting started](../README.md) · [Engine reference](engine.md)

`songya-mcp` serves MCP over stdio. Initialization and `memory://guide` provide compact agent instructions. Tool discovery describes the arguments.

`memory_maintenance` with `action=status` reports the running Songya version,
process ID, start time, and attachment capabilities. Check this through the
client being diagnosed: replacing an executable does not update existing stdio
processes. Restart or reconnect their owner after deployment. Clara retains its
MCP processes in `clara-execution.service`, independently of `clara.service`.
An older status reply without `runtime` cannot confirm image support.

## Records and capture

- **Memory:** an event, experience, fact or preference, understandable on its own.
- **Entity:** a particular person, place, organization, object or other thing, with a brief identifying description. Its experiences belong in linked memories.
- **Topic:** a subject connecting memories.
- **Story:** an ordered narrative composition of connected memories.

Before every save, call `prepare_memory` with the information to keep. Review its bounded shortlist, then save once, including any new connected entities/topics. Reuse existing records. Save snippets as information arrives; enrich an existing memory for additional information, correct its prose when facts change, and create connected memories for distinct events. Do not invent facts, identities, dates or causes.

Compare new facts as well as corrections with relevant entity dates and event
accounts. Check ages, dates, event order and stated causes. A conflict does not
identify which claim is wrong: save independently clear new facts in the proper
fields, leave disputed earlier claims unchanged, explain the conflict and ask
which claim needs correcting. An explicit correction follows the correction
rules. Uncertainty must not be silently replaced with a guessed date or age.

```json
{"name":"prepare_memory","arguments":{"text":"Alex and Sam enjoy the soup at Cedar Café.","user":"example_user"}}
{"name":"save_memory","arguments":{"text":"Alex and Sam enjoy the soup at Cedar Café.","user":"example_user","relations":[{"type":"related_to","target":"Alex"},{"type":"related_to","target":"Sam"},{"type":"related_to","target":{"kind":"entities","name":"Cedar Café","type":"place"}}]}}
```

A target is an existing record ID, an entity name, or a named object with `kind` (`entities` or `topics`), `name`, and optional `type`, `description`, `aliases`. Unique exact names/aliases reuse existing identities; matching is case-insensitive. New IDs are assigned by the engine. Ambiguous or unusable connections remain in `unresolved_connections` with diagnostics; healthy connections and prose still save.

Memories and stories require an owning `user`; Clara supplies the current user automatically. Use the selected collection's user ID for its accounts. Ownership selects a collection; entity connections identify participants. Reflecting on another person's experience does not make the writer a participant. Entities and topics are shared. Selection is unrestricted; `user:"all"` selects everyone. Updating without a user preserves ownership.

## Writes and connections

| Tool | Main arguments |
| --- | --- |
| `save_memory` | `text`, optional `title`, `time`, `user`, `metadata`, `relations` |
| `type_configuration` | `user`; optional app flag `schemas`; reads allowed IDs and definitions |
| `save_entity` | `create:true` and `name` for creation; `id` for updates; optional `type`, `aliases`, `description`, `metadata`, `relations` |
| `save_topic` | `create:true` and `name` for creation; `id` for updates; optional `aliases`, `description`, `metadata`, `relations` |
| `save_story` | `title`, optional `text`, `user`, `metadata`, ordered `relations` |

All four accept an existing `id` to update, and `append` to enrich prose. For `save_memory`:

- **Create:** omit `id`, supply standalone prose in `text` and an owner (or let the integration supply it). Empty content cannot create a memory. Equivalent content supplied through `append` is retained rather than rejected.
- **Append:** supply `id` and `append`. This literally adds a blank line and the new prose, preserving existing content.
- **Update:** supply `id` and the fields to change. `text` replaces the full prose, including an empty string to clear it. When both `text` and `append` are supplied, replacement happens first, then append. Date-only, connection-only, title-only, ownership-only and metadata-only updates are supported.

Preserve the account's meaning, attribution and meaningful quotations; prose must stand on its own without inventing facts. Omitted fields preserve existing information. `title:""` clears the memory title. Custom metadata patches supplied top-level keys, including explicit null values; omitted keys and `metadata:{}` preserve existing values. Supplied relations replace the list; with nonempty `append`, they add connections. `[]` always clears the list. For a connection-only addition, read the old connections and supply every connection to keep plus the new one. Inline identity enrichment preserves existing connections.

Entity/topic `description` is their Markdown body, also used by search and the library table. Do not duplicate it in metadata.

Named-record creation is explicit: send `create:true` and `name`, without `id`.
This always creates a separate record, even if its name matches an existing one.
To update, send the existing record's `id`; a name alone never updates it.
Supplying both `create:true` and `id` is rejected. Search first and choose the
identity from the returned descriptions/IDs. For example, when the two people
are confirmed distinct:

```json
{"name":"save_entity","arguments":{"create":true,"name":"Alex","description":"Sam's coworker, a different person from Sam's cousin Alex."}}
{"name":"save_entity","arguments":{"id":"COWORKER_ENTITY_ID","description":"Sam's coworker, who repairs bicycles."}}
```

Inline named targets also accept `create:true` for a separate new identity.
Name-only links can locate existing identities, but enriching an existing inline
identity requires its ID. Unsafe or ambiguous enrichment remains unresolved with
diagnostics while the memory's prose and healthy connections still save.

All authoritative connections use `relations: [{type, target}]`. Store one connection and let Songya derive its reverse:

| Type | Meaning of A → B | Derived reverse |
| --- | --- | --- |
| `after` | A happened after B | `before` |
| `caused_by` | B caused A | `causes` |
| `part_of` | A is a component or sub-event of B | `contains` |
| `related_to` | General association, including entities and topics | `related_to` |
| `possibly_same_as` | Possible identity match, without merging | `possibly_same_as` |

A story's `related_to` links to memories/stories define reading order. Other links associate identities or subjects. Story prose is introductory text; narrative order does not assert chronology.

```json
{"name":"save_story","arguments":{"title":"Visits with Sam","user":"example_user","relations":[{"type":"related_to","target":"FIRST_MEMORY_ID"},{"type":"related_to","target":"SECOND_MEMORY_ID"}]}}
{"name":"save_memory","arguments":{"id":"MEMORY_ID","append":"They also enjoy the salad."}}
```

## Event time

New date values must be real calendar dates in `YYYY-MM-DD` format or `null`.
Freeform timing words, bare months/years, timestamps, empty strings, impossible
days, and non-string values are not accepted as new date-field values. Put timing
explanations in the body instead. Existing damaged source remains readable with
diagnostics and may receive unrelated edits without losing its original data.

For an ongoing state "since July 2025", use
`{"date":null,"earliest":"2025-07-01","latest":null}`. The start is a bound,
not an assertion that it began on July 1; the end is unknown/open. For a one-time
event *in* July, use July 1–31 as its possible date bounds instead.

`time` describes when the event happened, separately from automatic creation/update timestamps. Record supported dates in these structured fields as well as prose. Use an exact `date` or inclusive range bounds. Combine all known timing, including related events and relative expressions, rather than defaulting to the beginning of a known year. Preserve the original temporal wording and explain the basis of inferred bounds in prose; never invent an exact date.

For memory updates, omitted `time` preserves it. Supplied components patch only those components; component `null` explicitly clears it. `time:null` clears all three. When changing an exact date into a range, explicitly supply `date:null`. Switching to an exact date should clear any obsolete bounds. Unreadable values remain available with diagnostics and in retained write snapshots.

Keep separately dated events in separate, connected memories. For example:

```json
{"name":"save_entity","arguments":{"id":"JOHN_ENTITY_ID","time":{"start":"1990-04-22"}}}
{"name":"save_memory","arguments":{"title":"John moved to Lakeside","text":"John moved to Lakeside in May 2024. The exact day is unknown.","user":"example_user","time":{"date":null,"earliest":"2024-05-01","latest":"2024-05-31"},"relations":[{"type":"related_to","target":"JOHN_ENTITY_ID"}]}}
{"name":"save_memory","arguments":{"id":"MEMORY_ID","time":{"latest":"2024-05-20"}}}
```

Replace example IDs with IDs returned by the store. The birth date is an entity start date; the move has its own event bounds. The date-only update preserves the earlier bound, prose and connections.

```json
{"date":"2024-12-31","earliest":null,"latest":null}
{"date":null,"earliest":"2024-12-01","latest":"2024-12-31"}
{"date":null,"earliest":"2024-01-01","latest":null}
{"date":null,"earliest":null,"latest":"2024-12-31"}
```

The third example is the agreed “after 2024” convention. `after` orders events without dates; `caused_by` also orders cause before consequence. `part_of` groups events. Contradictions produce diagnostics while independent records remain usable.

## Retrieval

Tool results are in `structuredContent.data`. A save returns publication `status`, ID, record `kind`, operation ID, `outcome`, `changed_fields`, resulting prose/time/relations and diagnostics. `status:"committed"` confirms durability, not that new facts were added or placed correctly. For a committed operation, `outcome` is `created`, `updated`, or `unchanged`; `changed_fields` lists changed data fields, excluding automatic timestamps and source bookkeeping. Unfinished writes retain their pending/blocked outcome and recovery receipt; they are never described as completed updates.

Before reporting success, compare each requested fact with the saved kind, ID,
field and value. Verify fields meant to remain unchanged too. A date in prose
does not fill a date field; a new memory does not correct an existing record.
Read back the source when the result omits needed values or truncates words.

An ID-only call or an update supplying identical values returns `unchanged`, an empty `changed_fields`, and actionable guidance; it does not rewrite the memory or advance its timestamp. Compare the returned content and time with the facts intended for saving. Do not claim new facts were saved or repeat the same unchanged call. Previews are bounded; read the record for full content. Retrying an accepted request key returns the original saved snapshot and outcome, even after later edits, without replaying the write.

```json
{"name":"recall_memory","arguments":{"text":"Sam","user":"example_user"}}
{"name":"search_memory","arguments":{"text":"Sam","kind":"entities"}}
{"name":"search_memory","arguments":{"kind":"topics"}}
{"name":"search_memory","arguments":{"kind":"memories","related_to":"ENTITY_ID"}}
{"name":"search_memory","arguments":{"mode":"semantic","text":"orchestral performer"}}
{"name":"read_memory","arguments":{"id":"RECORD_ID","view":"source"}}
{"name":"explore_memory","arguments":{"id":"RECORD_ID","direction":"both"}}
{"name":"memory_timeline","arguments":{"user":"example_user"}}
{"name":"compose_story","arguments":{"id":"STORY_ID"}}
```

Recall returns connected accounts and story context. Semantic recall adds a bounded story introduction and neighboring events. Direct edits refresh automatically. Search without text lists records. Configure embeddings in the store's `config.json`; saving never waits for embeddings. `memory_maintenance(action="embed")` refreshes missing vectors.

Follow `next` as `offset` with the same query/user when `truncated` is true. Recall/text/source/story offsets count bytes; search/graph/timeline offsets count items. Numeric strings work. Recall caps at 16 KiB; MCP reads/composition default to 8 KiB and cap at 12 KiB. Missing data produces diagnostics while healthy work continues.

## Organization, imports and recovery

`organize_memory(action="merge", sources=[...])` combines reviewed records; `action="split"` takes a source and reviewed parts. Retired records remain readable and redirects preserve references. Splits inherit ownership; merges inherit a common owner, or require explicit `metadata.user` across owners.

Configure import roots with `--import-root notes=/absolute/path`. `import_memories(action="preview", root="notes")` lists sources; `action="apply"` takes a path and user. Optional reviewed parts use exact byte ranges (`start` inclusive, `end` exclusive), kind and metadata. Originals are retained with a checksum and coverage mapping.

Clients can attach a stable `songya/request-key` in MCP `_meta` to writes. Retry identical arguments with that key. Clara supplies its durable invocation ID. Without client metadata, Songya assigns an independent operation ID. Identical prose in separate operations can describe separate memories.

`memory_maintenance(action="receipt", key=...)` inspects an outcome; `recover` resumes pending work; `status` reports diagnostics; `rebuild` refreshes the disposable index. `abandon` withdraws uncommitted work while retaining proposed data. `read_memory` with `key`, `id`, and `view:"proposed"`, `"before"` or `"original_text"` reads retained work. Ordinary saves handle this bookkeeping automatically.

Clients can attach the person's exact message in MCP request metadata
`songya/original-text`. This is client data, not a tool argument for the model.
Songya retains it as the write's original account. Model-written `text` and
`description` are stored as proposed/committed record bytes, not original words.
Without original-text metadata, an ordinary model save does not invent an original
account. Model tools cannot replace `original_accounts` through metadata.
Retries keep the source captured by the accepted write and still reject different
record changes under the same request key.

## User-specific type choices

Call `type_configuration` with `user` before assigning types, unless the app
already supplied the current choices. Its reply contains the allowed IDs,
definitions, and short instructions for all four record kinds. The tool is
read-only: a model cannot change these settings. Only use configured IDs. Omit
`type` when none fits; an empty string clears this user's assignment.

The four save tools accept `type` and `type_user`. The latter selects whose
classification changes, independently of the `user` record owner. Clients should
supply the selected person rather than ask the model to guess. Trusted client
metadata `songya/user` takes precedence over model arguments. Another user's
assignments are preserved, including on updates that omit owner.

Clients can request `type_configuration({user, schemas:true})` to receive
`write_schemas`, keyed by save tool name. Use these authoritative schemas in
model-facing definitions; they include allowed enums and definitions. The CLI's
`--user` option also scopes discovery. Engine validation always uses current
settings, even when a client's previously discovered schema is stale.


## Entity start and end dates

`save_entity.time` has `start` and `end`, not date/earliest/latest. They are the
start and end dates of the entity's existence. They could represent birth and
death for a person, founding and closure for an organization, and so on. Use this
meaning for every user-configured entity type; no particular type ID is required.

Send the existing ID and only the components to change. Omission preserves a
component; null clears it; `time:null` clears both. Both values must be real
`YYYY-MM-DD` dates or null. A missing end does not establish continued existence.
Never substitute record creation, first mention, or uncertain date bounds.
Date-only updates leave the description unchanged, and save feedback returns
both lifetime components. A narrated event belongs in a linked memory with
its own event timing. An entity start/end date alone does not need a separate
memory record.


## Files

- `read_memory(id)` includes up to eight current attachments on the first page, with a total count. Use `list_files` to continue. For an entity it also reports its selected `profile_picture`, with the actual owning `record` ID and `filename`, including pictures saved on linked memories.
- `list_files(id)` returns filenames, formats, original MCP resource URIs, and the current record revision. Its content also includes native MCP resource links.
- `read_file(id, filename, offset)` returns actual MCP image content for photos, or at most 16384 bytes of plain text. Continue a positive `next` as `offset` for text. Other formats return original resource references without pretending to interpret them.
- `attach_file(id, revision, upload, filename?)` attaches a completed upload supplied by the app, returning the actual filename and durability status.
- `replace_file(id, revision, filename, upload)` explicitly replaces one file's bytes.
- `remove_file(id, revision, filename)` explicitly removes one file.
- `file_status(key)` checks the original file operation after an uncertain reply, without replaying it.

Use record IDs and returned filenames, never model-invented disk paths. Uploads
travel over the application's streaming HTTP endpoint, not binary/base64 text
arguments. The embedded chat supplies completed upload receipts alongside each
message. Vision-capable clients receive photos as native MCP image blocks, not base64 prose.
JPEG/PNG images within 1600 pixels can be read directly; other images, including
HEIC/HEIF, use the same bounded decoder as the web gallery. Animated previews show
one frame. Songya uses its decoder in `~/.local/lib/songya/bin/ffmpeg` when installed,
otherwise FFmpeg on PATH. Original bytes remain unchanged.

`memory://files/{id}/{filename}` exposes the original file bytes independently of
the web server. Use the exact escaped URI returned by a tool. Originals over
32 MiB use `pages_uri`: `memory://files/{id}/{filename}/pages/{offset}` returns up
to 1 MiB with `next_uri`, byte offsets, total size and original MIME type in
resource metadata. Clients can assemble these pages without sending base64 through
model text arguments. This exposes originals of any format; it does not provide
audio transcription, video analysis or document extraction. An unavailable image
preview returns an error and retains its original resource link.

The embedded chat retains returned pixels separately from tool prose and supplies
them as image inputs after the complete adjacent tool-result block. This preserves
provider tool-call pairing and the original conversation. Selecting a vision-capable
model remains necessary to interpret photos.

Only `status:"committed"` confirms an attachment change. An upload alone is not an
attachment. Clients supply stable `songya/request-key` metadata on mutations; a
retry must keep that key and its original arguments. `memory_maintenance` recovery
also resumes accepted file changes. Record-file changes update `updated_at` and
the text revision without changing the prose or unrelated field values.
