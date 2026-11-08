# Explicit conversion to record folders

Prepare the new binaries and stop **all** writers before conversion: standalone
web, every MCP process, import workers, external editors, and applications that
bundle the engine. Replacing a binary alone does not stop a running old process.

```sh
go build -o songya-convert-folders ./cmd/songya-convert-folders
./songya-convert-folders --store /absolute/path/to/store
```

The converter takes the store lock. Before moving anything it durably publishes
`.layout-folders/inventory.json` with original names, target names, hashes, sizes,
modification times and permissions. It moves one file at a time, syncs the file
and both directories, reads it back, verifies preservation, and records progress.
No record parsing or serialization occurs. IDs, owner fields, original accounts,
custom metadata, dates, relations and story order remain in the exact original
bytes. Invalid but readable nonhidden Markdown filenames retain their names as
orphan record folders; the engine reports their missing IDs rather than inventing
ones. Ambiguous names or conflicting destinations stop conversion without deletion.

Pending record writes move with their checksum filenames and reservations. Empty
or unknown retained subdirectories survive in the corresponding pending folder.
Batch/import history stays in place and is checked unchanged. Old Source fields
are immutable provenance: the new engine resolves live batch targets by record ID
without rewriting original JSON, request identities, checksums, or receipts.

If interrupted, keep writers stopped and run the same command again. It reads the
original inventory, verifies already moved files, and continues. It never chooses
between two conflicting copies or silently replaces a destination. Read the error
and preserve both copies when operator repair is required.

After the `complete` marker and preservation checks pass, start only new-layout
consumers. Normal runtime uses `<kind>/<id>/record.md` exclusively; the converter
is the only code that interprets the old filesystem layout. Do not rerun the
converter after writers resume and legitimately change the inventoried records.
Keep the conversion inventory/progress with the store's operational recovery files.
