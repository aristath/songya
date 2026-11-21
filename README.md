# sōngyā Memory Engine

A memory engine that keeps connected records in Markdown files. Use it from an
MCP client, its standalone browser editor, or as a Go library. Memories, entities,
topics, and stories share one store, with open metadata and five structural relationship types.

Durability comes first: preserve original content, recover interrupted writes,
and keep healthy records usable when something else is damaged. Lexical search,
graph traversal, partial timelines and story composition run locally. Optional
semantic search uses a configured embedding service.

The repository and Go module use `github.com/aristath/songya`; the MCP
executable is `songya-mcp`.
There is no Clara dependency or integration.

## Standalone browser editor

```sh
go install github.com/aristath/songya/cmd/songya-web@latest
songya-web --config /absolute/path/to/admin.json
```

Open <http://127.0.0.1:8664>. Change the address with `--listen 127.0.0.1:8665`.
The executable contains the HTML, CSS, Alpine.js and both editors. It needs no
Node runtime, CDN, frontend service or Clara installation. Point it at the same
store as the MCP to edit that store; engine writers coordinate across processes.
The editor requires the password in its private JSON configuration:

```json
{"stores":{"person":"/absolute/path/to/person/store","agent":"/absolute/path/to/agent/store"},"password":"CHOOSE_A_PRIVATE_PASSWORD"}
```

Keep this file outside Git, readable only by the administrator. HTTP Basic
 authentication accepts any username with that password. The store selector
 switches between independent directories. All API, attachment, chat and event
 routes are scoped to the selected store and require authentication.

Search content or filter by title, description, record kind, type and aliases.
Each store has independent type definitions, colors, and a **My entity** owner
selection. Defaults include 12 entity types; other kinds start without subtypes.
Removing an in-use type requires explicit replacement or clearing.
Ownership is implicit in the store: its memories already belong to its owner,
so redundant links to **My entity** are removed. The graph uses that entity for
positioning without creating extra visible relationships.

Click a title to inspect the record in a read-only modal, or **Edit** to open the
editor modal. **Visual** edits formatted Markdown; **Source** edits the complete
Markdown/YAML document with syntax highlighting. Both modes share the same draft.
Using **Close**, Escape or clicking outside the modal keeps unsaved work in the
current tab.
Metadata has text, number and boolean controls, with YAML values for lists and
nested/custom structures.
Creation/update timestamps are automatic and read-only. IDs come from record-folder names.

**Connections** searches for targets by name or text, accepts custom relationship
types, and shows outgoing connections and incoming links. Opening another record
keeps unsaved drafts in the current browser tab. **Save** explicitly publishes
changes. Failed saves retain the draft; uncertain writes reuse the original
request on retry, while rejected input can be corrected. Conflicts preserve your
draft; **Load saved version** explicitly replaces it with the current stored record
after confirmation. **Discard draft/changes** explicitly abandons local edits. The browser warns before leaving a tab with
unsaved work. Drafts are not saved to disk until you press Save.

**Delete permanently** removes the live record immediately, without trash or a
soft-delete state. The engine refuses deletion while other records link to it,
including ordered story relations and retired-record redirects. Blockers are listed with links so you can open and
edit them. Deletion never cascades or edits those referring records. Unreadable
sources or unfinished writes can defer deletion; healthy reads and edits continue.
Deleted records stay deleted through normal restart/recovery. Existing transaction
receipts retain their original audit bytes; deletion does not expose a restore UI.

Raw HTML is shown as inert source content in the visual editor, never executed.
Use Source for exact HTML or Markdown constructs outside the visual editor's
CommonMark controls. Merely switching views does not rewrite the document.

Build from a checkout with `go build -o songya-web ./cmd/songya-web`.
See [UI development](docs/ui.md) for asset builds and browser tests.

## Android app and remote pairing

Songya includes a thin Android wrapper with QR pairing. In the selected store,
open **Connect phone** to download the APK, create a pairing code or revoke a
phone. Paired phones can read and edit only that store; administrator HTTP Basic
authentication continues to access all configured stores.

Add these fields to the private web configuration:

```json
{
  "public_url": "https://songya.example.com",
  "mobile_state": "/private/path/songya.mobile.json",
  "android_apk": "/path/to/songya/android/Songya.apk"
}
```

Keep the existing `stores` and `password` fields. Route the public HTTPS hostname
to the web listener with a tunnel or reverse proxy. The phone then works away
from the local network. `mobile_state` is optional and defaults beside the web
configuration; its public keys and hashed tokens are separate from memory files.
The app stores no administrator password. Codes expire after five minutes and
can be used once; returning phones use Android Keystore signatures to reconnect.

See [android/README.md](android/README.md) for signed release builds and deployment.

## Start with MCP

Install Go at the version required by [go.mod](go.mod) or newer. Go's automatic
toolchain selection can download the required version. The store needs a local
filesystem with hard links, advisory locks, and directory syncing.

```sh
git clone https://github.com/aristath/songya.git
cd songya
go install ./cmd/songya-mcp
```

Go puts the executable in `GOBIN` when configured, otherwise in `bin` under the
first `GOPATH` directory (normally `$HOME/go/bin`). Check your paths with:

```sh
go env GOBIN GOPATH
```

Add the server to your MCP client's configuration, replacing both paths below
with absolute paths on your machine. Clients have their own configuration file
locations; this is the common `mcpServers` shape:

```json
{
  "mcpServers": {
    "songya": {
      "command": "/absolute/path/to/songya-mcp",
      "args": ["--store", "person=/absolute/path/to/person/store", "--store", "agent=/absolute/path/to/agent/store"]
    }
  }
}
```

The server creates missing store directories. Your client launches it over stdio;
it opens no network listener. Running the executable directly waits for MCP
protocol messages on stdin. Semantic search calls the embedding service below.

After connecting, use `recall_memory({"text":"John"})` to retrieve connected
accounts by name or subject. It includes surrounding story context and record
references. Follow `next` as `offset` to continue larger accounts.

Ask your client to save a memory, for example:

> Remember that I had coffee with John today. Check existing records first.

Use `prepare_memory` before saving, then `save_memory` with text, time and relations. The engine creates and
connects the records automatically. Read the [MCP guide](docs/mcp.md) for the tools, concrete requests,
responses, retry handling, and optional imports.

## Embeddings

Place `config.json` in the store, or pass `--config /path/to/songya.json`:

```json
{
  "embeddings": {
    "model": "BAAI/bge-m3",
    "base_url": "http://127.0.0.1:18200/v1"
  }
}
```

These defaults match Clara's embedding setup. `api_key` is optional. Restart the
MCP server after changing its configuration. The endpoint serves the model through
an OpenAI-compatible `/embeddings` API; Songya does not download model weights.

`search_memory({"text":"orchestral performer","mode":"semantic"})` searches
entity names, aliases and descriptions, and memory titles, contents and semantic
metadata. Entity and memory vectors refresh as their text changes. Use
`memory_maintenance({"action":"embed"})` to index all existing records ahead of
time. Otherwise semantic searches embed missing content when needed.

Long text uses overlapping chunks of 2,000 characters; `chunk_size` adjusts this
for another model. Vectors are rebuildable files under `.index/embeddings`.
Changing the model or endpoint uses a separate cache. Reads and saves keep working
when the service or cache is unavailable; search reports partial results or an
explicit lexical fallback with diagnostics.

## Use the Go library

From your application's Go module (for a new module, run
`go mod init example.com/songya-example` first):

```sh
go get github.com/aristath/songya
```

Save this as `main.go` and run `go run .`:

```go
package main

import (
	"fmt"
	"log"

	"github.com/aristath/songya"
)

func main() {
	store, err := songya.Open("./memory-store")
	if err != nil {
		log.Fatal(err)
	}
	saved, err := store.Create(songya.Memories,
		map[string]any{"title": "Coffee with John"}, "I had coffee with John.")
	if err != nil {
		log.Fatal(err)
	}
	record, err := store.Read(saved.ID)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(record.ID, record.Body)
}
```

Each run creates a new record. For revision-checked updates and retry-safe related
changes, see the [executable examples](example_test.go) and
[engine reference](docs/engine.md).

## Files and backups

Records live in `memories/`, `entities/`, `topics/`, and `stories/`. Each record-folder
name is its identity: a type prefix (`m_`, `e_`, `t_`, or `s_`) plus 12 random
lowercase letters or digits. Metadata is YAML frontmatter; the body is Markdown.
Each folder contains `record.md` plus any photos, audio, documents, or other files
belonging to that record. Only `record.md` is indexed as a record; an attached
`notes.md` stays a file. No attachment manifest or per-file metadata is required.
The engine assigns IDs and timestamps. No duplicate ID field is needed in YAML.
Each directory is an isolated store with its own records, files, indexes and
settings. Records have no owner field; links stay inside their store. MCP and Pi
tools require a configured `store` name on every call.

Back up the **whole store**, including hidden files and directories. `.batches/`
retains transaction journals, original accounts, and before/proposed bytes;
`.imports/` retains imported originals and mappings. Hidden pending directories
retain unfinished writes. `.uploads/` retains completed uploads until attached;
`.file-ops/` retains file-operation receipts, and `.deletions/` retains deletion
intents/tombstones. These are operational recovery state, not another attachment
store. Only `.index/` is disposable.

For a consistent backup, stop writers and external editors while copying, or use
an atomic filesystem snapshot. Restore into a directory on a supported local
filesystem, then open that directory with the library or `--store`; recovery runs
automatically. Keep `.lock` in place while a store is in use.

See [durability and recovery](docs/engine.md#durable-writes-and-recovery) for what
pending writes mean and [batch recovery](docs/mcp.md#recovery-and-clients) for
inspecting unfinished work.

### Adding files

Use **Add files** in the editor, or **Attach files** in the chat composer. You can
also drop files onto either one. In chat, files appear beside the current draft;
remove any you do not want, write your message, and press **Send**. Sending waits
for those uploads automatically while you can write the next message. Completed
draft uploads survive reloads, and a failed send keeps the message and its files for retry.
Completed uploads also survive a failed record save. The editor attaches staged
files after saving the record. Tell Songya which record the chat files belong
to. A reply must confirm a committed attachment, not only a saved memory.

Images have previews; supported audio/video have controls and seeking. Plain text
has a bounded preview. Every file can be downloaded unchanged. HTML, SVG and other
active formats are downloads, not application content. Duplicate, reserved, or overlong names
receive a reported safe filename; existing bytes are never silently overwritten.
Use **Replace** or **Remove permanently** deliberately. Deleting a record removes
its files too, while keeping existing incoming-link checks.

Use ordinary relative Markdown links such as `[Notes](notes.txt)` or
`![A caption](photo.jpg)` for deliberate inline placement. Story readers resolve
these links and file previews against each originating record, including repeated
memories and nested stories. They do not copy the files into the story folder.

### Converting an existing flat store

The layout conversion is explicit and offline, not an automatic runtime fallback.
See [folder conversion](docs/folders.md). Stop every old writer, including long-lived
MCP processes, before running it. All consumers must run the new engine afterward.

## Development

From the repository root:

```sh
go test -race -count=1 ./...
go vet ./...
go build -o /tmp/songya-mcp ./cmd/songya-mcp
/tmp/songya-mcp --help
```

The stdio tests build and launch the real server with the official MCP client.
To instrument those child executables with the race detector too:

```sh
GOFLAGS=-race GORACE=atexit_sleep_ms=0 go test -count=1 ./mcpserver
```

Tests use temporary stores and import fixtures. Project priorities are in
[AGENTS.md](AGENTS.md).

### Application recall

`Store.Retrieve(ctx, RecallRequest{Messages: []string{...}})` performs recall
inside that store. Query vectors reuse the model/endpoint/content cache.
Missing embeddings produce partial diagnostics, never invented lexical scores.

`go run ./cmd/songya-query` reads a JSON request from stdin: a `store` directory,
optional `config`, and `command` (`configuration`, `list`, `read`, `stats`, `recall`).
Recall accepts `message` text through `messages`. Configuration inspection excludes credentials.
This application boundary exposes no write commands.

`OpenExisting` lets query clients inspect an existing store without creating
record directories or publishing pending work. Missing sources remain explicit
diagnostics. Normal `Open` retains automatic recovery for writers and the MCP.

Message retrieval is centralized in Songya: [HTTP recall API, settings and client integration](docs/recall.md).
