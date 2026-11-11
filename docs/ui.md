# Standalone UI development

The `songya-web` command serves Go templates, CSS and an Alpine.js component over
Go's standard HTTP server. `web.Handler` is also available to Go applications.
The library and centralized recall endpoint call the Songya engine directly. Chat connects to the same Songya MCP
server through an in-process MCP client session.

Chat is the main interface in the left column. The right column has a bounded
2D graph above the record library, sharing the right column equally. CSS Grid
fits two columns when each can be at least `32rem` wide; otherwise they stack as
chat, graph, library. A container narrower than `32rem` stays within its available
width. This uses intrinsic sizing without media/container queries or breakpoints.
Chat history and the library scroll independently, and the composer stays inside
the conversation pane. The graph uses the active palette’s Crust background. Its 2D / 3D toggle
switches between locally bundled [3d-force-graph](https://github.com/vasturiano/3d-force-graph)
and [force-graph](https://github.com/vasturiano/force-graph). Node colors come from
the selected classification user's type colors, with record-kind colors for
unclassified records. **Colors for** selects that user and stays synchronized
with the library's **Types for** selector; the owner filter is independent.
Nodes grow with their incoming and outgoing connection counts. Hover shows the name/title, and clicking opens
read-only inspection. The top-right owner dropdown defaults to **Any**, showing
the whole store. Selecting an owner shows their memories and stories plus directly
connected shared entities/topics, including connections in either direction.
Unrelated shared records and other owners' accounts are omitted. Graph search
uses that same selection, which survives view switching and expansion. The graph
filter is independent of the library filters and refreshes after saves and
deletions. No CDN is required.
The legend checkboxes select which record kinds appear as points. Hidden records
still carry connections between visible points, including chains of hidden kinds.
These projected connections have no direction arrows (and are dashed in 2D);
hover identifies them as connections through hidden records. Longer paths and
broadly shared records contribute less layout strength; independent paths add
support. The view keeps the strongest few indirect neighbours plus a connecting
backbone, avoiding a dense clique without disconnecting the remaining points.
Owner selection is applied before this projection, so excluded accounts
cannot introduce connections. Showing all kinds restores the saved direct graph.
Projection changes only the display, not stored relationships or point weights.
Its search matches names/titles, aliases and IDs, highlighting matches while
dimming other points. Select a result or press Enter to move the camera to it;
click the point to inspect. Clear the search or press Escape to restore the overview.
Search is retained when switching views. The 2D view works without WebGL;
the table and editors also remain usable if graph rendering is unavailable.
The 2D layout uses collision boundaries matched to each node's drawn radius and
spaces links according to their endpoints, keeping large hubs and smaller nodes
apart. The top-right expand button fills the browser viewport while keeping the
browser's own controls visible. Search and view switching remain available. Use
the same button or Escape to restore the normal graph; Escape closes an open
record first. Background controls are inert while the graph is expanded.

Below the graph, the **Library** holds the sortable, filterable table with
title, description, record
kind, metadata type, aliases, owner and row actions. The Go catalogue supplies
metadata and the exact revision used for row deletion. **Last updated** shows
its saved update time in the browser's local time zone; newest updates sort first
by default. Unknown timestamps remain at the bottom. **Filters** is collapsed
initially and retains its values when closed and reopened. Full-content search and
ownership selection use engine queries; column filters and sorting refine the
returned rows without changing the records.
Entity and topic descriptions come from their Markdown bodies, so edits update
the table and description filter directly.

The **List / Timeline** switch keeps the graph and chat in place. Timeline shows
memories using event dates and usable before/after relationships, never creation
or update timestamps. Exact dates, closed/open ranges and undated accounts retain
their saved precision. Overlapping periods do not establish a complete order.
An undated memory linked to dated events can appear among them with **Date unknown**
and its explicit links; the view does not fill in a date. Independent relative
sequences, unplaced memories and unusable timing remain visible in named sections.

Each timeline entry shows its ID, owner, configured color, excerpt and timing
links. **Edit dates** and **Edit order** open the existing record editor; adding an
`after` link uses Connections, while missing date controls can be added with
**Add event dates**. Changes preserve the record's ID, body and other metadata.
Saved edits and chat/tool writes refresh the timeline through the existing event
stream. A temporary refresh failure retains readable entries and retries.

The library's search, owner and column filters still apply. Timeline adds
**Related to** and **Timing** inside the collapsed **Filters** panel to find one
subject, unknown dates, ranges or conflicts. The record-kind filter is reserved for List; returning to List
restores its previous selection. The layout remains intrinsic without breakpoints.
`GET /api/timeline` returns a snapshot with all matching memories and explicit
ordering constraints, so links cannot disappear between separately fetched pages.

## Chat

Choose **Talking as** to set the owner for a conversation. Existing owners and
conversation users appear in the dropdown; **Add user** accepts a new owner name.
Shared entities and topics keep their normal Songya meaning. User selection is
an ownership label, not authentication, and is independent of graph/list filters.
Each user can keep multiple conversations. The history rail shows titles, recent
message previews, dates and unfinished-work status. Search filters titles and
previews; **New chat** starts a separate conversation. The last selection and
separate unsent drafts for each conversation (including a new conversation) are
remembered in the browser. A reply continues on the server when another chat is
opened. History refreshes while conversations are working.

The history rail sits beside the conversation when its intrinsic reading and
navigation columns fit. Otherwise **Conversations** opens the same rail as a
native modal drawer, with native keyboard focus and Escape handling. CSS flex
wrapping determines fit; no media/container queries or viewport breakpoints are
used. **Expand chat** fills the browser area; Escape restores the normal view
after closing any open modal first.

**Conversation actions** offers rename and archive/restore. Archiving retains
all history and requires the saved turn and queue to finish first. Archived chats
are under **Archived** and must be restored before accepting another message.
User messages have an **Edit** button. **Save and resend** keeps the earlier
conversation, replaces that message, removes its later replies and queued turns,
and starts the corrected turn. Its attachments are retained. Editing a queued
message keeps work before it and replaces that queue entry and everything after
it. Saved memories and record attachments are not undone. Interrupted sends retry
the same request without rewinding twice.

**Conversation actions → Delete conversation** permanently removes the chat after
confirmation, including its queued messages. Active work stops before removal.
Saved records and their files remain. `DELETE /api/chat/sessions/{id}` is retryable.

`PATCH /api/chat/sessions/{id}` accepts `title` and/or `archived`, using the same
locked, synced conversation publication path as message changes. Renaming and
archiving do not modify memory records, messages, original request identities or
saved tool results.

The top-right header theme selector offers **Device** (default), **Light**, and
**Dark**. Native `color-scheme` and `light-dark()` follow device changes without
layout queries; an explicit selection is saved locally and applied before styles
load to avoid a flash of the wrong theme. The choice covers chat, library,
dialogs and editor surfaces. Source editing uses a dedicated YAML/Markdown
syntax palette for each theme, including selection, matching text, line numbers,
cursor and search controls. Device-theme changes update these colors without
rebuilding the editor or replacing its draft, selection or undo history.
The entire interface uses the official [Catppuccin palettes](https://catppuccin.com/palette/):
Mocha for dark mode and Latte for light mode. Crust is the page, graph and source
background; Base and Mantle provide panel and control surfaces. Palette-derived
syntax colors maintain contrast in the source editor, including in Latte.

**Chat settings** saves an OpenAI-compatible API base URL, optional API key and
model name in `chat-settings.json` at the store root. Use the API base (for example
`http://inference-host:8080/v1`), not a browser chat URL. **Test connection and load
models** saves the settings and queries the provider's models endpoint. Providers
without model listing can still use a manually entered model. A blank key keeps
the saved key; the checkbox explicitly clears it. Credentials are kept on the
server in a file with mode `0600`; browser responses report only whether a key is
set. No process-environment configuration is used.

**Retrieval settings** in the library heading controls the shared recall service and provides a saved-policy tester. See [the recall API and policy contract](recall.md). Songya Chat automatically retrieves evidence through this engine before calling the model; deliberate MCP tools remain available.

The client is the official `openai-go/v3` SDK, using streamed Chat Completions and
function calling. The provider/model must support both. Songya supplies the actual
MCP tool schemas and initialization guide, executes completed tool calls through
MCP, and returns their real results to the model. The editable Markdown system
prompt is `chat-system.md` at the store root, seeded from `web/chat-system.md` when
missing. Changes apply to newly accepted turns; recovery uses the prompt captured
with the accepted turn.

Chat settings include **Temperature**, from 0 to 2, default **0.15**. Zero is
allowed and remains zero after saving/restarting. Each accepted turn keeps its
temperature across retries; a settings change applies to the next turn. Lower
temperature reduces variation, not factual or tool errors. Requests ask the
provider to send one tool call at a time.

The app attaches the person's exact accepted message as `songya/original-text`
MCP request metadata. This is separate from model-written `text` or `description`.
Those generated words remain in the write journal's before/after snapshots; they
are not labeled as original testimony. The model cannot edit `original_accounts`
through MCP metadata. Existing stored accounts are not rewritten by this change.

Malformed tool tags remain in the saved chat for inspection. The app does not
execute any calls from that malformed reply or guess its missing fields. It asks
the model to resend valid JSON. A second malformed reply pauses work with a
recoverable error. Identical consecutive no-op saves are not executed again;
repeated failures stop the turn with its accepted message and results intact.
Identical failed saves, including empty argument objects, are bounded by that
same retry guard rather than being repeated until the general tool limit.
Retry continues from that saved state. Tool feedback uses `description` for
entity/topic words and `text` for memory/story words.

When releasing updated prompts, update the store's effective `chat-system.md`
alongside the code after reviewing any local customizations. Updating the embedded
file alone does not replace an existing store prompt.

Messages and partial replies are saved in `.chats/<conversation-id>.json` with
mode `0600`. A turn is accepted only after its message and request identity are
synced. Browser disconnects do not cancel accepted work. Reopening the page loads
saved history and reconnects to streaming progress; interrupted pending work
resumes after service restart. Stable MCP request keys recover original write
receipts if a memory was saved before its tool result could be recorded.

Live progress distinguishes waiting for the model, thinking, writing, and tool work,
with elapsed time. Provider reasoning streams in an open **Thinking…** section.
When reasoning ends, that section collapses and the answer or next tool step appears.
You can expand **Reasoning** afterward, including after reopening the conversation.
Reasoning is stored separately from the reply and never becomes original testimony,
memory prose, or part of subsequent model requests. Streaming uses small progress
events, with reasoning checkpoints at most once every five seconds, rather than
rewriting the full transcript for every reasoning token. Progress keeps the connection alive while
unchanged conversation files are not repeatedly parsed or broadcast. If live events
fail or become silent, the browser checks saved progress and reconnects without
resending accepted messages or clearing a newer draft.

**Pause** retains the conversation, partial reply and completed memory operations;
it does not undo writes. **Continue saved turn** resumes paused or failed work.
The message input stays usable during replies and sends. Sending clears it and
immediately returns focus so the next draft can be written. Messages submitted
during a reply are saved in a first-in, first-out conversation queue, shown as
**Queued**, and answered one at a time after the current reply finishes. Each
queued turn retains the prompt, temperature and retrieval policy captured when
accepted. Paused or failed turns hold the queue until continued. Queued messages
survive browser disconnects and server restarts; they are not included in the
model's current turn before their own turn starts.
An incomplete model stream never executes a partial tool call. Lost message
acknowledgements are retried with the same identity. Local unsent drafts are
retained where browser storage is available. Messages awaiting acknowledgement
also remain in the browser, including further sends made while the request is
pending. **Retry pending messages** resends these without submitting the draft.
An acknowledgement never clears a newer draft. Replies scroll automatically while
following the latest text; scrolling up pauses following, and **Jump to latest**
restores it. The graph and library subscribe to record-change events. Each completed write
in any embedded chat refreshes both views, even while the reply continues or a
different conversation is selected. Web-editor saves and deletions also notify
open views. Reconnecting refreshes the saved state, and failed refresh requests
retry automatically. Refreshes preserve the chosen filters, sort and editor drafts.
Damaged conversation files are retained and reported independently, so other
conversations and the memory library remain available.

Applications embedding `web.Handler` should call its returned application's
`Close` method during shutdown to release background recovery workers.

Titles open read-only inspection; Edit and New record open a native modal dialog.
The browser keeps focus inside it. Close, Escape or clicking the backdrop retains
unsaved drafts locally without a server request; reopening a record restores them.
The background table retains its filters and sorting while a modal is open.
Table deletion uses the same engine checks as the editor; blocked deletion opens
the record with navigable referring records.

The compiled executable embeds `web/assets`, including the generated JavaScript,
editor styles and third-party license notices. Builds and tests use no live store.

## Build and check

Go alone builds the application from the checked-in assets:

```sh
go build -o songya-web ./cmd/songya-web
./songya-web --store /path/to/store --listen 127.0.0.1:8664
```

To change frontend sources, install Node and npm and work in `web`:

```sh
cd web
npm ci
npm run build
npm run format:check
npx playwright install chromium
npm test
```

`npm test` rebuilds the bundle, compiles the standalone Go executable into a
temporary directory, starts it against a disposable store, and uses real Chromium
for desktop and phone workflows. It removes its store and stops the process on
completion. Screenshots are written to `/tmp/songya-ui-review`.

Run the complete Go/MCP suite from the repository root:

```sh
go test -race -count=1 ./...
go vet ./...
```

Commit generated `web/assets/app.js`, `editors.css`, `NOTICE.txt`, and any emitted
legal-comment file along with frontend source changes. The asset builder uses
esbuild; Alpine, CodeMirror and ProseMirror are direct, framework-independent
libraries. Package versions and the lockfile are committed.

## Editing and durability

The browser sends source drafts to Go for parsing and metadata/relationship edits.
It does not maintain another memory format or its own relationship graph.
Unchanged YAML values retain their nodes, including aliases and custom values.
Common scalar controls are conveniences; custom and nested values remain editable.
Event time has exact-date and earliest/latest date controls; blank values stay null.
Date fields and their calendars display `YYYY-MM-DD` in every browser locale.
Calendar headings use `YYYY-MM`, weeks start on Monday, and keyboard arrows move
between days; Page Up/Down changes the month (Shift changes the year). Year and
month controls support jumping directly to older dates. Escape closes the calendar
before the record editor. Impossible dates remain in the draft for correction;
equivalent year-first input such as `19810501` normalizes to `1981-05-01`.
Library update times use local `YYYY-MM-DD HH:mm:ss`, and conversation dates use
local `YYYY-MM-DD`; stored UTC timestamps and event dates remain unchanged.
Changing an exact date clears the range, and changing a bound clears the exact date.
Unrecognized time data stays editable as YAML. Connection controls offer the five
structural types. A story’s outgoing relations can be moved up/down to set reading
order; incoming links show their derived reverse labels.

Opening a saved story displays its composed account, with each memory's ID in a
column to the left of its title and text. The introduction and linked memories
come directly from their saved records, including nested stories and repeated
memories in reading order. Entity and topic associations are not story passages.
Long stories load automatically in pages; a failed page retains everything already
read, retries temporary failures, and offers a retry button. Missing references
and unfinished records remain visible.
The reader uses `GET /api/stories/{id}/compose?offset=…`; offsets are the composer's
byte positions, not memory indexes. It joins page fragments before rendering
Markdown.
The default reading view hides the raw metadata dump. Source, Connections and Edit
retain the underlying record controls; editing a story changes its introduction
and links, never a copy of its linked memories.

`Store.SaveSource` uses the existing batch journal and a stable write key. It
replaces the full document while preserving the engine's identity/clock rules.
The read snapshot's revision prevents silent overwrites by concurrent writers.
Retries return the original committed proposal, not an unrelated later snapshot.
The UI resolves a lost acknowledgement before publishing newer draft edits. A
request rejected before persistence can be corrected and submitted again.

`Store.Delete` checks current and pending references under the engine's exclusive
cross-process writer lock. It removes only the requested, unchanged record folder and its files, with a
durable deletion intent and directory syncing before reporting success. It refuses an incomplete incoming
link check and unfinished writes that might republish the record. Completed batch
receipts never republish deleted records during recovery. It adds no user-visible trash,
revision browser or soft-delete mechanism, and it does not erase historical transaction
journal payloads.

The interface uses explicit Save. Unsaved work stays in the tab when moving
between records; failed operations retain it. Closing the tab discards drafts
after the browser's unsaved-work warning. This is not a durable draft/history store.

The loopback default is intended for a trusted local operator. The listening
address is configurable, but the application has no login system. Cross-origin
browser mutations are rejected. No account-management or deployment system is
included.

A file containing invalid UTF-8 bytes cannot be safely round-tripped through a
text editor. Opening it reports an encoding problem and leaves its bytes intact;
other records remain usable. Repair that file's encoding before editing it here.

## Type settings

The library heading's **Type settings** opens a per-user editor. Each record kind has its
own list. Edit names and short definitions, add choices, or remove a choice.
Removing an in-use choice requires selecting a replacement or explicitly
clearing that user's assignments. Stable IDs do not change when names change.
New users receive independent defaults; their changes do not change anyone
else's setup.

The library's **Types for** selector controls classification independently of
the owner filter. The record editor has the same user selector and a type
dropdown. A shared entity can have different classifications for different users.
The chat uses its selected speaker's current definitions in the prompt and tool
schemas. The model cannot invent a type or edit settings.

Settings drafts are retained per user in browser storage. A lost save response
can be retried using the same durable request key. Conflicting settings changes
retain the draft and offer an explicit reload. Forms use intrinsic wrapping,
including on narrow screens, without viewport breakpoints.


### Recovery guidance and original words

Every model request has one opening system message. It includes the app's
recovery rules. Specific tool errors remain in their tool replies. Incomplete
text-only tool calls receive repair directions in the next opening system
message. App recovery notes already stored in a chat remain in that transcript;
they are not sent as later system messages or changed into user messages.

Original testimony must match the durable receipt for the submitted message and
current turn. A later app message cannot replace it just by using the `user`
role or a `SYSTEM/RECOVERY MESSAGE` prefix. Real user text keeps its exact spaces
and wording, including such a prefix when the person actually typed it.


### Entity lifetime controls

Entity metadata uses **Start** and **End** date controls. For an entity without
recorded dates, **Add lifetime dates** adds these controls explicitly. They
represent the start and end dates of the entity's existence, whatever its
user-configured type. Blank means no date is recorded, not proof of continued
existence. Memory controls retain **Exact date**, **Earliest**, and **Latest**.
Changing a new draft's kind converts empty controls, while entered date values
are retained for explicit review instead of being assigned a new meaning.

### Type and record colors

Type settings include **Record colors** for entities, memories, topics and stories,
and a color choice on every type definition. Each picker has the 23 Mocha presets
(all palette colors except Crust, Mantle and Base), followed by **Custom** and a
native color picker. Preset swatches show Mocha; the matching Latte color is used
in light mode. Custom hex colors remain unchanged across themes.

Record colors are the fallback; a configured type's color takes precedence.
The graph and library dots use the selected classification user's choices.
Changing colors does not edit memory files, their dates or their type assignments.
New and existing users receive defaults; existing settings are not rewritten just
to display them. Default entities use Green, memories Peach, topics Lavender and
stories Red. The 12 default entity types receive distinct accent colors; custom
types receive a stable default and can be changed normally.

Colors share the existing per-user settings version, durable write receipt,
conflict handling and browser draft retention. Saved color changes invalidate
open graph/library views. A lost response retries the same submitted choices;
newer edits stay in the draft until explicitly saved.

The shared `palette.json` is sourced from the official Catppuccin palette. The
frontend build generates `web/assets/palette.css` and includes the upstream MIT
license in `NOTICE.txt`; palette data and styles are bundled locally.


## Files in the editor and chat

Every kind of record has the same **Files** section. Select multiple files or drop
them onto the section. Dirty/new text is saved before attaching staged uploads.
Completed uploads stay available when a save or its acknowledgment fails. Keep the
same retry operation to resolve an uncertain result. Replace/remove are explicit
permanent actions; save a dirty text draft before using them.

The chat composer accepts multiple files without disabling text input or queued
messages. Files stay with the draft and are included automatically when sent.
The transcript
keeps upload receipts separately from the person's words; the model receives those
receipts without binary data. Uploaded and confirmed-attached states are distinct.
Failed memory saves do not erase completed uploads. Reloading recovers them.
In a multi-file turn, a filename/upload pair that identifies two different submitted
files is rejected before a file tool runs. Other file-tool rename inputs are unchanged.
Consumed uploads can be restaged from their verified record-owned bytes for a later
operation; changed or missing source bytes produce an explicit request for the original
file rather than reusing the wrong bytes. This adds no permanent attachment copies.

File previews use the browser's native image/media controls and safe text output.
Media responses support HTTP ranges. Original downloads keep their actual saved
filename and bytes. Active formats are served as downloads with restrictive headers.


## Entity profile pictures

In an entity’s Files section, choose **Use as profile picture** on an attached
image. Click the full image to set its focus point, or use the horizontal and
vertical focus sliders. Adjust zoom while watching the round preview, then use
**Save**. **Edit profile picture** keeps the existing framing; **Reset framing**
centres it, and **Remove profile picture** clears only the selection.

The original file is never cropped or rewritten. The entity’s ordinary Markdown
header stores `profile_picture: {filename: portrait.jpg, x: 0.5, y: 0.5, zoom: 1}`.
Focus coordinates range from 0 to 1 and zoom from 1 to 5. Both graph renderers use
the same crop. Unavailable pictures fall back to ordinary coloured graph nodes.

The web host needs `ffmpeg` on its PATH for browser-sized image previews,
including HEIC/HEIF. Original downloads remain available if conversion fails.
Previews use bounded concurrency and a timeout, and browsers revalidate them
against the original file’s modification time and size.

The graph’s bottom-left colour legend also contains checkboxes for memories,
entities, topics and stories. These controls combine with the owner filter and
hide connecting lines whose endpoints are hidden. They affect only the graph.


Entities can also select pictures from directly linked memories. These appear in
**Photos from linked memories**, below the entity’s own files, in both view
and edit mode. Each photo links back to its source memory.
The saved `profile_picture` adds `record: m_...` to identify the source record;
`filename` identifies its existing attachment. No file is copied or moved.
Changing back to an entity’s own attachment clears that external reference.
Focus, zoom, graph display and removal work the same for both sources.

Photo galleries use intrinsic masonry columns, retaining each image's proportions.
Click a thumbnail to open a photo dialog with the original preview, filename,
size, download link and available actions. The gallery itself shows no file
names, sizes or action buttons. Escape closes the photo and returns focus to its
thumbnail without closing the record. Profile selection opens the existing crop
editor; it remains a draft until the record is saved. Linked files are replaced
or removed only from their source memory. Story photos use the same viewer.


Graph point sizes measure recorded content. Each entry contributes its own Markdown
body's word count at full weight. Each distinct connected entry, of any kind,
contributes once at its shortest distance: 0.5, 0.4, 0.3, 0.2 or 0.1 for hops one
through five. Words are separated by whitespace, including Unicode spaces and
line breaks. Titles, aliases and other metadata do not add words.

`related_to`, `caused_by` and `part_of` provide paths in either direction, without
extra relationship multipliers or hub penalties. Ordering, possible identity
matches and redirects do not contribute. Hidden entries still contribute their
text through the neutral junctions. Dates do not affect weights.

Drawing sizes use the score divided by the mean score of visible entries, keeping
the layout independent of the score's units. Neutral junctions do not affect this
mean. An empty or zero-score view uses a unit of 1. The 2D radius is
`4 * sqrt(1 + weight / mean)`; 3D uses the corresponding volume scaling. Photos,
collision spacing and ordinary points use the same sizes.

`graph-weights.json` is the single rebuildable repository-wide weight cache. It
also retains source revisions, neighbours and pending work so restarts can detect
edits and removed connections. A background worker drains all due calculations
without a per-record rate limit, saves the results and notifies the chart after
the pass. Results expire after 24 hours. Edited and affected records have priority,
followed by never-calculated records and the oldest expired calculation. Ties
between never-calculated records favour the most connected records, then ID.
Records edited through the UI, chat or external clients and their affected
neighbours recalculate in the next pass. Edits outside
the web process are detected on its roughly one-second scan. Cached sizes remain
visible during refreshes. The chart shows progress during its initial pass.
Cache writes use synced atomic replacement and retry after failures; failures do
not block record access. No memory files or client protocols are changed.
