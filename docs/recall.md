# Centralized retrieval

Songya owns message embeddings, ranking, thresholds, result counts, searched and
returned types, ownership filtering, and relationship traversal. Clients supply
text and an explicit ownership scope, then consume the returned evidence. Clara
and Songya Chat use this same engine. Deliberate MCP search/read/write tools remain
available independently.

## HTTP contract

`POST /api/recall` accepts JSON:

```json
{"message":"Do you remember our rehearsal?","owners":["ari","clara"]}
```

For a batch use `messages: ["first message", "second message"]` instead of
`message`. A batch shares a catalogue/configuration snapshot and record vectors;
results correspond to input order. Supply one text form, nonempty messages, and a
nonempty owners array. `owners:["all"]` explicitly selects all collections.
Shared entities/topics remain available; memories/stories stay within the requested
owners at every traversal hop. Ownership is a retrieval scope, not authentication.
Request fields cannot override the retrieval policy.

Clients retaining previously emitted evidence can also supply `known_ids`.
The response then includes `known` entries with `id`, `status`, and an `item`
for `current` records. These refreshes use the same catalogue and ownership scope,
independently of ranking and embedding availability. `missing` confirms an absent
record in an accessible record directory; `retracted` means the record is retired
or outside the requested scope. `unknown` means its current state cannot be
confirmed, including unavailable or unfinished sources. An omitted search result
alone never confirms deletion. Clients can preserve earlier blocks and append
corrections or withdrawal notices at the current interaction.

The response is `{ "policy": {...}, "results": [...] }`. Each result contains:

- `mode: "semantic"` and `status: "ready" | "partial" | "unavailable"`.
- `items`: records selected by the saved policy.
- `incomplete`, `truncated`, and bounded `diagnostics` with path/message entries.

Each item contains `id`, `kind`, `user` when owned, `title`, structured `time`,
`content`, `depth`, and `reason`. Direct matches have `reason:"semantic"`, depth
zero, and a measured `score` (cosine similarity, not a probability). Connected
items have `reason:"connection"`, `origin_id`, and `path` entries
`{from,to,type}`. They deliberately have no semantic score. `content_truncated`
identifies clipped prose; the full record remains readable by ID.

A ready result with no items means nothing qualifies under the configured policy.
Unavailable/partial retrieval must never be interpreted as proof that no memories
exist. Embedding failures do not become lexical scores. Available cached vectors
and healthy records remain usable; missing vectors are retried on later requests.
The service uses a 60-second operational request deadline and propagates caller
cancellation. Available partial results are retained when that deadline expires.

## Policy and configuration

`config.json` at the memory-store root holds the embedding and retrieval sections.
Its current values are reloaded for each operation, including MCP/CLI embedding
operations. An explicit MCP `--config` selects another configuration file. Readable
fields survive damaged options, with diagnostics rather than silent resets of
healthy fields. Missing configuration uses these defaults:

```json
{
  "embeddings": {
    "model": "BAAI/bge-m3",
    "base_url": "http://127.0.0.1:18200/v1",
    "chunk_size": 2000
  },
  "recall": {
    "direct_limit": 5,
    "min_score": 0.42,
    "search_kinds": ["memories","entities","stories","topics"],
    "return_kinds": ["memories","entities","stories","topics"],
    "connection_depth": 1,
    "connected_limit": 5,
    "relation_types": ["after","caused_by","part_of","related_to","possibly_same_as"]
  }
}
```

The direct limit counts semantic seeds, including seeds whose types are excluded
from the returned types. The threshold applies to those seeds. The connected limit
counts additional returned records, independently of the seed limit. Depth zero
or a connected limit of zero disables expansion. A direct limit of zero or empty
searched types disables matching. Empty returned types return no records; empty
relationship types disable traversal. Counts accept zero; safety ceilings are
1,000 direct/additional records and 50 relationship hops per request.

Search and return types are independent: search entities and return memories to
retrieve accounts through a matching identity. Traversal can pass through other
record types, but only configured returned types consume the additional-result
budget. It follows permitted structural relationships in both directions, labeling
reverse hops (`before`, `causes`, `contains`) honestly. Cycles and repeated records
are visited once. Missing/ambiguous targets are diagnosed and skipped. Retired
records are not returned; unambiguous redirects can resolve relationship targets.

Seeds are ordered by descending similarity then stable ID. Multi-source breadth-
first traversal chooses shortest paths; equal-length paths follow seed rank then
neighbor ID and relationship label. Direct seeds are never duplicated as connected
items. Connection scores do not boost or masquerade as measured similarities.
Returned prose is bounded to 8,192 UTF-8 bytes per record, plus at most 8,192 bytes of metadata. Oversized metadata is replaced with a compact identifying label and explicitly marked as truncated. Stories contribute their
own introduction and ordered relationships; their members are explicit traversal
results, so hidden story expansion cannot bypass ownership/type/depth/count rules.

## Settings interface

**Retrieval settings**, beside the Chat/List tabs, edits this policy and tests a
message against the saved policy. The embedding model/endpoint is shown separately
from the conversational LLM settings. `GET /api/recall/settings` returns effective
policy, embedding information without API keys, diagnostics and a source `version`.
`PUT` accepts `{version,recall}`. Stale versions receive HTTP 409. Drafts survive reloads in browser storage when available; an explicit Reload saved policy retains a recovery copy before replacing the draft. Lost acknowledgements are resolved against the saved policy without replaying a write. Saves preserve other configuration fields, use a cross-process
lock and synced atomic publication, and never report a failed publication as saved.

## Embedding cache and chat

All four record types embed their own identifying text/prose; memories and stories
also include supported dates and semantic metadata. Records do not recursively
embed their neighbors. Text uses 2,000-character chunks by default with 10% overlap.
Each normalized vector is cached by endpoint/model/text under `.index/embeddings`.
Unchanged messages and records reuse those vectors; edits and model/endpoint changes
produce new keys. Damaged vectors regenerate. Memory writes do not depend on the
embedding endpoint.

Songya Chat retrieves evidence automatically before model generation and supplies
it as explicitly untrusted context, separate from user instructions and the stored
transcript. A durable turn captures its effective retrieval configuration and then
its retrieval result before generation. Retries/restarts reuse that accepted
snapshot, including honest partial/unavailable diagnostics; a new turn uses current
configuration. Private captured credentials are never returned through chat APIs.
