package songya

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"go.yaml.in/yaml/v3"
)

// A nil body preserves the existing body on update, and means empty on creation.
type Change struct {
	enrich            bool           // Inline identity enrichment adds connections to the existing description.
	preserveUnchanged bool           // Memory saves must not touch timestamps for an unchanged account.
	source            *string        // Full document used internally by SaveSource.
	Ref               string         `json:"ref,omitempty"`
	ID                string         `json:"id,omitempty"`
	Kind              Destination    `json:"kind,omitempty"`
	Revision          string         `json:"-"`
	Fields            map[string]any `json:"fields,omitempty"`
	Body              *string        `json:"body,omitempty"`
	Append            string         `json:"append,omitempty"`
	AddRelations      []Relation     `json:"add_relations,omitempty"`
}
type BatchRequest struct {
	prepare           func(*os.Root) ([]Change, error)
	typeConfiguration *typeConfigurationChange
	User              string   `json:"user,omitempty"`
	Key               string   `json:"key"`
	Account           string   `json:"original_text,omitempty"`
	Changes           []Change `json:"changes"`
}

type originalTextKey struct{}

// WithOriginalText attaches the person's exact words, supplied by the client,
// not by the model. Generated record text remains in the before/after journal.
func WithOriginalText(ctx context.Context, text string) context.Context {
	return context.WithValue(ctx, originalTextKey{}, text)
}

type SavedRecord struct {
	ID            string   `json:"id"`
	Revision      string   `json:"-"`
	Outcome       string   `json:"outcome"`
	ChangedFields []string `json:"changed_fields"`
}
type Receipt struct {
	Key         string            `json:"key"`
	Status      string            `json:"status"`
	Refs        map[string]string `json:"refs,omitempty"`
	Records     []SavedRecord     `json:"records,omitempty"`
	Diagnostics []Diagnostic      `json:"diagnostics,omitempty"`
}
type batchEntry struct {
	ID       string
	Source   string
	Base     string
	Revision string
	Before   []byte
	After    []byte
}
type batchJournal struct {
	Account []byte
	Key     string
	Request string
	Refs    map[string]string
	Entries []batchEntry
}

func batchDirectory(key string) string { return filepath.Join(".batches", dataRevision([]byte(key))) }
func validHash(value string) bool {
	_, err := hex.DecodeString(value)
	return len(value) == 64 && err == nil
}

func (s *Store) lockedContext(ctx context.Context, operation func(*os.Root) error) error {
	if s == nil {
		return errors.New("store is nil")
	}
	root, err := os.OpenRoot(s.path)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := s.initialize(root); err != nil {
		return err
	}
	lock := flock.New(filepath.Join(s.path, ".lock"))
	defer lock.Close()
	acquired, err := lock.TryLockContext(ctx, 10*time.Millisecond)
	if err != nil {
		return err
	}
	if !acquired {
		return ctx.Err()
	}
	return operation(root)
}

// syncNew never replaces unrelated data. A checksum-named complete file may be
// reused after an interrupted sync; unknown or conflicting bytes stay intact.
func syncNew(root *os.Root, name string, data []byte) error {
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if errors.Is(err, fs.ErrExist) {
		existing, readErr := readFile(root, name)
		if readErr != nil || string(existing) != string(data) {
			return fmt.Errorf("retained data conflicts at %s: %w", name, fs.ErrExist)
		}
		return syncPath(root, name)
	}
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	return errors.Join(writeErr, file.Sync(), file.Close())
}
func marker(root *os.Root, path string) bool {
	data, err := readFile(root, path)
	return err == nil && len(data) == 0
}
func mark(root *os.Root, path string) error {
	if err := syncNew(root, path, nil); err != nil {
		return err
	}
	return syncPath(root, filepath.Dir(path))
}

func loadJournal(root *os.Root, directory string) (batchJournal, error) {
	var found *batchJournal
	entries, err := fs.ReadDir(root.FS(), directory)
	if err != nil {
		return batchJournal{}, err
	}
	for _, entry := range entries {
		name := entry.Name()
		if len(name) != 69 || !strings.HasSuffix(name, ".json") || !validHash(strings.TrimSuffix(name, ".json")) {
			continue
		}
		data, err := readFile(root, filepath.Join(directory, name))
		if err != nil {
			return batchJournal{}, err
		}
		if dataRevision(data) != strings.TrimSuffix(name, ".json") {
			return batchJournal{}, fmt.Errorf("batch journal checksum mismatch at %s; retained", directory)
		}
		var journal batchJournal
		if err := json.Unmarshal(data, &journal); err != nil {
			return journal, err
		}
		if batchDirectory(journal.Key) != directory || !validHash(journal.Request) || len(journal.Entries) == 0 {
			return journal, fmt.Errorf("invalid batch journal at %s; retained", directory)
		}
		seen := map[string]bool{}
		for _, record := range journal.Entries {
			source, err := batchTarget(record.ID)
			if err != nil || record.Source != source || seen[record.ID] || record.Revision != dataRevision(record.After) || record.Base != "" && record.Base != dataRevision(record.Before) {
				return journal, fmt.Errorf("invalid record in batch journal %s; retained", directory)
			}
			seen[record.ID] = true
		}
		if found != nil {
			return journal, fmt.Errorf("multiple batch journals at %s; retained", directory)
		}
		found = &journal
	}
	if found == nil {
		return batchJournal{}, fmt.Errorf("batch journal unavailable at %s: %w", directory, fs.ErrNotExist)
	}
	return *found, nil
}

func requestRevision(ctx context.Context, request BatchRequest) (string, error) {
	// Encode flexible metadata through the same safe data boundary as ordinary
	// writes, rather than invoking arbitrary Go JSON/YAML callbacks.
	type intent struct {
		Source    *string `json:",omitempty"`
		Ref, ID   string
		Kind      Destination
		Revision  string
		Fields    string
		Body      []byte
		BodySet   bool
		Append    []byte
		Relations []Relation
	}
	intents := []intent{}
	for _, change := range request.Changes {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		node, _ := encodeValue(change.Fields)
		data, err := yaml.Marshal(node)
		if err != nil {
			return "", err
		}
		item := intent{Source: change.source, Ref: change.Ref, ID: change.ID, Kind: change.Kind, Revision: change.Revision, Fields: string(data), BodySet: change.Body != nil, Append: []byte(change.Append), Relations: change.AddRelations}
		if change.Body != nil {
			item.Body = []byte(*change.Body)
		}
		intents = append(intents, item)
	}
	payload, err := json.Marshal(struct {
		User    []byte
		Account []byte
		Changes []intent
	}{[]byte(request.User), []byte(request.Account), intents})
	if err != nil {
		return "", err
	}
	return dataRevision(payload), nil
}

func localID(value string, refs map[string]string) string {
	if strings.HasPrefix(value, "$") {
		if id, ok := refs[strings.TrimPrefix(value, "$")]; ok {
			return id
		}
	}
	return value
}

func batchFields(values map[string]any, refs map[string]string) map[string]any {
	node, _ := encodeValue(values)
	var replace func(*yaml.Node)
	replace = func(node *yaml.Node) {
		target := dereference(node)
		if target != nil && target.Kind == yaml.ScalarNode && target.ShortTag() == "!!str" {
			value := localID(target.Value, refs)
			if value != target.Value {
				replacement := yamlValue(value)
				mergeComments(replacement, node)
				assignNode(node, replacement)
			}
			return
		}
		for i, child := range node.Content {
			if node.Kind != yaml.MappingNode || i%2 == 1 {
				replace(child)
			}
		}
	}
	replace(node)
	fields := map[string]any{}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if key, ok := fieldName(node.Content[i]); ok {
			fields[key] = node.Content[i+1]
		}
	}
	return fields
}

func prepareBatch(ctx context.Context, root *os.Root, request BatchRequest, revision string) (batchJournal, error) {
	journal := batchJournal{Key: request.Key, Account: []byte(request.Account), Request: revision, Refs: map[string]string{}}
	if request.prepare != nil {
		var err error
		request.Changes, err = request.prepare(root)
		if err != nil {
			return journal, err
		}
	}
	ids := map[string]bool{}
	creates := map[string]bool{}
	changes := append([]Change{}, request.Changes...)
	for i, change := range changes {
		if err := ctx.Err(); err != nil {
			return journal, err
		}
		id := change.ID
		if id == "" {
			kind := Destination(strings.ToLower(strings.TrimSpace(string(change.Kind))))
			if kind == "" {
				kind = Memories
			}
			prefix, err := kind.prefix()
			if err != nil {
				return journal, err
			}
			for {
				id, err = NewID(prefix)
				if err != nil {
					return journal, err
				}
				name, _ := recordName(id)
				_, committed := root.Lstat(name)
				_, pending := root.Lstat(pendingName(name))
				if errors.Is(committed, fs.ErrNotExist) && errors.Is(pending, fs.ErrNotExist) && !ids[id] {
					break
				}
				if committed != nil && !errors.Is(committed, fs.ErrNotExist) {
					return journal, committed
				}
				if pending != nil && !errors.Is(pending, fs.ErrNotExist) {
					return journal, pending
				}
			}
			creates[id] = true
		} else if _, err := recordName(id); err != nil {
			return journal, err
		}
		if ids[id] {
			return journal, fmt.Errorf("record %s occurs twice in one batch", id)
		}
		ids[id] = true
		changes[i].ID = id
		if change.Ref != "" {
			name := strings.TrimPrefix(change.Ref, "$")
			if _, exists := journal.Refs[name]; exists {
				return journal, fmt.Errorf("duplicate local reference %q", name)
			}
			journal.Refs[name] = id
		}
	}
	for _, change := range changes {
		if err := ctx.Err(); err != nil {
			return journal, err
		}
		name, err := recordName(change.ID)
		if err != nil {
			return journal, err
		}
		if _, err := root.Lstat(pendingName(name)); err == nil {
			return journal, fmt.Errorf("pending work for %s must recover first", change.ID)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return journal, err
		}
		before, readErr := readFile(root, name)
		var doc *Document
		base := ""
		if readErr == nil {
			base = dataRevision(before)
			if change.Revision != "" && base != strings.ToLower(strings.TrimSpace(change.Revision)) {
				return journal, fmt.Errorf("%s: %w", change.ID, ErrConflict)
			}
			doc = Parse(before)
		} else if !errors.Is(readErr, fs.ErrNotExist) {
			return journal, readErr
		} else if change.Revision != "" || !creates[change.ID] {
			return journal, readErr
		} else {
			doc = &Document{}
		}
		if change.source != nil {
			created, _ := normalizeNode(metadataField(&doc.Frontmatter, "created_at"))
			doc = Parse([]byte(*change.source))
			// Identity comes from the filename; creation time belongs to the engine.
			for i := len(doc.Frontmatter.Content) - 2; i >= 0; i -= 2 {
				if key, ok := fieldName(doc.Frontmatter.Content[i]); ok && key == "id" {
					doc.Frontmatter.Content = append(doc.Frontmatter.Content[:i], doc.Frontmatter.Content[i+2:]...)
				}
			}
			if base != "" {
				_ = doc.Set("created_at", created)
			}
		}
		doc.Kind = kindOf(change.ID)
		fields := batchFields(change.Fields, journal.Refs)
		if userOwned(kindOf(change.ID)) {
			if user, supplied := change.Fields["user"]; supplied {
				fields["user"] = user // User IDs are not request-local record references.
			} else if base == "" && request.User != "" {
				fields["user"] = request.User
			}
		}
		body := doc.Body
		if change.Body != nil {
			body = *change.Body
		}
		if change.Append != "" {
			if body != "" {
				body += "\n\n"
			}
			body += change.Append
		}
		if base == "" {
			created := time.Now().UTC()
			fields["created_at"], fields["updated_at"] = created, created
			if kindOf(change.ID) == Memories && fields["time"] == nil && metadataField(&doc.Frontmatter, "time") == nil {
				fields["time"] = map[string]any{}
			}
			delete(fields, "id")
			if _, ok := fields["title"]; !ok && (change.source == nil || metadataField(&doc.Frontmatter, "title") == nil) {
				fields["title"] = ""
			}
			if change.source == nil {
				node, warnings := encodeValue(fields)
				assignNode(&doc.Frontmatter, node)
				doc.warnings = warnings
			} else {
				for key, value := range fields {
					_ = doc.Set(key, value)
				}
			}
			doc.Body = body
		} else {
			applyFields(doc, fields, body)
		}
		if len(change.AddRelations) > 0 {
			relations := sequenceNode(metadataField(&doc.Frontmatter, "relations"))
			for _, relation := range change.AddRelations {
				relation.Target = localID(relation.Target, journal.Refs)
				node, _ := encodeValue(relation)
				relations.Content = append(relations.Content, node)
			}
			_ = doc.Set("relations", relations)
		}
		if request.Account != "" && strings.HasPrefix(change.ID, "m_") {
			accounts := nodeStrings("original_accounts", doc)
			found := false
			for _, account := range accounts {
				found = found || account == request.Account
			}
			if !found {
				// Preserve malformed or richer account values while adding the new raw
				// account. Flattening to strings would silently discard those originals.
				accountsNode := sequenceNode(metadataField(&doc.Frontmatter, "original_accounts"))
				value, _ := encodeValue(request.Account)
				accountsNode.Content = append(accountsNode.Content, value)
				_ = doc.Set("original_accounts", accountsNode)
			}
		}
		if err := requireUser(doc, kindOf(change.ID)); err != nil {
			return journal, fmt.Errorf("%s: %w", change.ID, err)
		}
		if err := (&Store{path: root.Name()}).validateTypeChange(ctx, root, Parse(before), doc, kindOf(change.ID), request.typeConfiguration); err != nil {
			return journal, fmt.Errorf("%s: %w", change.ID, err)
		}
		if err := validateTimeChange(metadataField(&Parse(before).Frontmatter, "time"), metadataField(&doc.Frontmatter, "time"), kindOf(change.ID)); err != nil {
			return journal, fmt.Errorf("%s: %w", change.ID, err)
		}
		data, err := doc.Marshal()
		if err != nil {
			return journal, err
		}
		if base != "" && change.preserveUnchanged && len(changedRecordFields(before, data)) == 0 {
			data = before
		}
		journal.Entries = append(journal.Entries, batchEntry{ID: change.ID, Source: name, Base: base, Revision: dataRevision(data), Before: before, After: data})
	}
	if config := request.typeConfiguration; config != nil {
		base := ""
		if len(config.Before) > 0 {
			base = dataRevision(config.Before)
		}
		journal.Entries = append(journal.Entries, batchEntry{ID: typeConfigurationID(config.User), Source: typeConfigurationPath(config.User), Base: base, Revision: dataRevision(config.After), Before: config.Before, After: config.After})
	}
	if len(journal.Entries) == 0 {
		return journal, errors.New("batch contains no changes")
	}
	sort.Slice(journal.Entries, func(i, j int) bool { return journal.Entries[i].ID < journal.Entries[j].ID })
	return journal, nil
}

func receiptFor(journal batchJournal, status string, err error) Receipt {
	receipt := Receipt{Key: journal.Key, Status: status, Refs: journal.Refs, Records: []SavedRecord{}}
	for _, entry := range journal.Entries {
		outcome := "updated"
		fields := changedRecordFields(entry.Before, entry.After)
		if entry.Base == "" {
			outcome = "created"
		} else if len(fields) == 0 {
			outcome = "unchanged"
		}
		if status != "committed" {
			outcome = status
		}
		receipt.Records = append(receipt.Records, SavedRecord{ID: entry.ID, Revision: entry.Revision, Outcome: outcome, ChangedFields: fields})
		for _, warning := range Parse(entry.After).Diagnostics() {
			receipt.Diagnostics = append(receipt.Diagnostics, Diagnostic{entry.ID, warning})
		}
	}
	if err != nil {
		receipt.Diagnostics = append(receipt.Diagnostics, Diagnostic{batchDirectory(journal.Key), err.Error()})
	}
	receipt.Diagnostics = boundedDiagnostics(receipt.Diagnostics)
	return receipt
}

func (s *Store) ApplyBatch(ctx context.Context, request BatchRequest) (Receipt, error) {
	if text, ok := ctx.Value(originalTextKey{}).(string); ok {
		request.Account = text
	}
	if request.Key == "" {
		return Receipt{}, errors.New("a stable request key is required for retry safety")
	}
	if err := ctx.Err(); err != nil {
		return Receipt{Key: request.Key, Status: "not_accepted"}, err
	}
	digest, err := requestRevision(ctx, request)
	if err != nil {
		return Receipt{}, err
	}
	var receipt Receipt
	err = s.lockedContext(ctx, func(root *os.Root) error {
		var issue error
		receipt, issue = s.applyBatchLocked(ctx, root, request, digest)
		return issue
	})
	return receipt, err
}

func (s *Store) applyBatchLocked(ctx context.Context, root *os.Root, request BatchRequest, digest string) (Receipt, error) {
	receipt := Receipt{Key: request.Key, Status: "not_accepted"}
	err := func() error {
		if err := ensureDirectory(root, ".batches"); err != nil {
			return err
		}
		directory := batchDirectory(request.Key)
		journal, err := loadJournal(root, directory)
		if err == nil {
			receipt = receiptFor(journal, "blocked", nil)
			// An accepted write keeps its captured source on recovery. The exact
			// same record changes must still match the original request digest.
			if _, supplied := ctx.Value(originalTextKey{}).(string); supplied {
				request.Account = string(journal.Account)
				digest, err = requestRevision(ctx, request)
				if err != nil {
					return err
				}
			}
			if journal.Request != digest {
				return fmt.Errorf("request key %q already belongs to different changes", request.Key)
			}
		} else {
			if !errors.Is(err, fs.ErrNotExist) {
				receipt.Status = "blocked"
				return err
			}
			// An existing damaged preparation is never mistaken for a fresh request.
			if entries, listErr := fs.ReadDir(root.FS(), directory); listErr == nil && len(entries) > 0 {
				receipt.Status = "blocked"
				return fmt.Errorf("retained preparation at %s requires recovery", directory)
			}
			journal, err = prepareBatch(ctx, root, request, digest)
			if err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			// Once persistence begins, a failed sync may still leave recoverable work.
			receipt = receiptFor(journal, "pending", nil)
			if err := root.Mkdir(directory, 0700); err != nil && !errors.Is(err, fs.ErrExist) {
				return err
			}
			data, err := json.Marshal(journal)
			if err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := syncNew(root, filepath.Join(directory, dataRevision(data)+".json"), data); err != nil {
				return err
			}
		}
		status, err := s.resumeBatch(root, journal)
		receipt = receiptFor(journal, status, err)
		return err
	}()
	return receipt, err
}

// Retry and startup recovery share one state machine. Terminal receipts never
// republish proposals, and every path establishes journal durability first.
func (s *Store) resumeBatch(root *os.Root, journal batchJournal) (string, error) {
	directory := batchDirectory(journal.Key)
	if err := syncBatchJournal(root, directory); err != nil {
		return "blocked", err
	}
	committed, err := committedBatch(root, directory)
	if err != nil {
		return "blocked", err
	}
	if committed {
		if err := mark(root, filepath.Join(directory, "committed")); err != nil {
			return "blocked", err
		}
		for _, entry := range journal.Entries {
			s.clearBatchClaim(root, entry.Source, []byte(filepath.Base(directory)))
		}
		return "committed", nil
	}
	withdrawal, err := abandonment(root, directory)
	if err != nil {
		return "blocked", err
	}
	if withdrawal != "" {
		if err := s.resumeAbandon(root, journal); err != nil {
			return "blocked", err
		}
		return "abandoned", nil
	}
	if err := s.publishBatch(root, journal); err != nil {
		return "blocked", err
	}
	return "committed", nil
}

func (s *Store) publishBatch(root *os.Root, journal batchJournal) error {
	directory := batchDirectory(journal.Key)
	owner := []byte(filepath.Base(directory))
	// Catch already-observed conflicts before reserving or publishing anything.
	for _, entry := range journal.Entries {
		current, err := readFile(root, entry.Source)
		if err == nil {
			revision := dataRevision(current)
			if revision != entry.Base && revision != entry.Revision {
				return fmt.Errorf("batch target %s: %w", entry.ID, ErrConflict)
			}
		} else if entry.Base != "" || !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	// Establish every reservation before publishing the first canonical file.
	for _, entry := range journal.Entries {
		if err := root.Mkdir(pendingName(entry.Source), 0700); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		claim := filepath.Join(pendingName(entry.Source), "batch")
		if existing, err := readFile(root, claim); err == nil {
			if string(existing) != string(owner) {
				return fmt.Errorf("%s is reserved by another batch", entry.ID)
			}
		} else {
			entries, listErr := fs.ReadDir(root.FS(), pendingName(entry.Source))
			if listErr != nil {
				return listErr
			}
			if len(entries) != 0 {
				return fmt.Errorf("pending work for %s belongs to another operation", entry.ID)
			}
		}
		if err := syncNew(root, claim, owner); err != nil {
			return err
		}
		if err := syncPath(root, pendingName(entry.Source)); err != nil {
			return err
		}
		if err := syncPath(root, filepath.Dir(entry.Source)); err != nil {
			return err
		}
	}
	for _, entry := range journal.Entries {
		current, err := readFile(root, entry.Source)
		if err == nil && dataRevision(current) == entry.Revision {
			if err := syncPath(root, entry.Source); err != nil {
				return err
			}
			if err := syncPath(root, filepath.Dir(entry.Source)); err != nil {
				return err
			}
			continue
		}
		if entry.Base == "" {
			if err == nil || !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("batch target %s: %w", entry.ID, ErrConflict)
			}
		} else if err != nil || dataRevision(current) != entry.Base {
			return fmt.Errorf("batch target %s: %w", entry.ID, ErrConflict)
		}
		temporary := filepath.Join(directory, entry.ID+"."+entry.Revision+".tmp")
		if err := syncNew(root, temporary, entry.After); err != nil {
			return err
		}
		// Recheck immediately before replacement, just as for single-record updates.
		current, err = readFile(root, entry.Source)
		if entry.Base == "" {
			if err == nil || !errors.Is(err, fs.ErrNotExist) {
				return ErrConflict
			}
			if err := root.Link(temporary, entry.Source); err != nil {
				return err
			}
		} else {
			if err != nil || dataRevision(current) != entry.Base {
				return ErrConflict
			}
			if err := root.Rename(temporary, entry.Source); err != nil {
				return err
			}
		}
		if err := syncPath(root, filepath.Dir(entry.Source)); err != nil {
			return err
		}
	}
	for _, entry := range journal.Entries {
		data, err := readFile(root, entry.Source)
		if err != nil || dataRevision(data) != entry.Revision {
			return fmt.Errorf("batch changed before commit: %w", ErrConflict)
		}
	}
	if err := mark(root, filepath.Join(directory, "committed")); err != nil {
		return err
	}
	for _, entry := range journal.Entries {
		s.clearBatchClaim(root, entry.Source, owner)
	}
	return nil
}
func (s *Store) clearBatchClaim(root *os.Root, name string, owner []byte) {
	claim := filepath.Join(pendingName(name), "batch")
	if data, err := readFile(root, claim); err == nil && string(data) == string(owner) {
		s.note(claim, root.Remove(claim))
		s.note(pendingName(name), root.Remove(pendingName(name)))
		s.note(name+".cleanup", syncPath(root, filepath.Dir(name)))
	}
}

func (s *Store) recoverBatches(root *os.Root) {
	entries, err := fs.ReadDir(root.FS(), ".batches")
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		s.note(".batches", err)
		return
	}
	s.note(".batches", nil)
	for _, entry := range entries {
		if !validHash(entry.Name()) || !entry.IsDir() {
			continue
		}
		directory := filepath.Join(".batches", entry.Name())
		journal, err := loadJournal(root, directory)
		if err == nil {
			_, err = s.resumeBatch(root, journal)
		}
		s.note(directory, err)
	}
}

// batchView provides the pre-batch view until the commit marker is durable.
// A conflicting external edit wins over this view and is never hidden.
func batchView(root *os.Root, name string, data []byte, readErr error) ([]byte, error) {
	owner, err := readFile(root, filepath.Join(pendingName(name), "batch"))
	if err != nil {
		return data, readErr
	}
	key := string(owner)
	if !validHash(key) {
		return data, readErr
	}
	directory := filepath.Join(".batches", key)
	journal, err := loadJournal(root, directory)
	if err != nil {
		return data, readErr
	}
	if marker(root, filepath.Join(directory, "committed")) || marker(root, filepath.Join(directory, "abandoned")) {
		return readFile(root, name)
	}
	for _, entry := range journal.Entries {
		if entry.Source != name {
			continue
		}
		if readErr == nil {
			actual := dataRevision(data)
			if actual != entry.Base && actual != entry.Revision {
				return data, nil
			}
		}
		if entry.Base == "" {
			return nil, fs.ErrNotExist
		}
		if readErr != nil {
			return data, readErr
		}
		return entry.Before, nil
	}
	return data, readErr
}

// A shared lock gives each ordinary read a stable batch boundary. If a writer
// is busy, readers still use retained prior views instead of waiting for it.
// Journals also catch a claim created and removed during an interrupted read.
func readRecordFile(root *os.Root, name string) ([]byte, error) {
	var busy bool
	if regularFile(root, ".lock") == nil {
		lock := flock.New(filepath.Join(root.Name(), ".lock"))
		defer lock.Close()
		acquired, err := lock.TryRLock()
		busy = err == nil && !acquired
	}
	// Only withdrawals that overlap this read can make its snapshot ambiguous.
	// Historical terminal operations never invalidate an independent later edit.
	knownWithdrawals := map[string]bool{}
	if busy {
		directories, _ := fs.ReadDir(root.FS(), ".batches")
		for _, directory := range directories {
			if validHash(directory.Name()) && marker(root, filepath.Join(".batches", directory.Name(), "abandoned")) {
				knownWithdrawals[directory.Name()] = true
			}
		}
	}
	data, err := readFile(root, name)
	data, err = batchView(root, name, data, err)
	if !busy || err != nil {
		return data, err
	}
	return busyBatchView(root, name, data, knownWithdrawals)
}

func busyBatchView(root *os.Root, name string, data []byte, knownWithdrawals map[string]bool) ([]byte, error) {
	directories, err := fs.ReadDir(root.FS(), ".batches")
	if err != nil {
		return data, nil
	}
	revision := dataRevision(data)
	withdrawn := false
	for _, directory := range directories {
		if !directory.IsDir() || !validHash(directory.Name()) || knownWithdrawals[directory.Name()] {
			continue
		}
		path := filepath.Join(".batches", directory.Name())
		journal, err := loadJournal(root, path)
		if err != nil || marker(root, filepath.Join(path, "committed")) {
			continue
		}
		for _, entry := range journal.Entries {
			if entry.Source != name || entry.Revision != revision {
				continue
			}
			if marker(root, filepath.Join(path, "abandoned")) {
				withdrawn = true
				continue
			}
			claim, claimErr := readFile(root, filepath.Join(pendingName(name), "batch"))
			if claimErr != nil || string(claim) != directory.Name() {
				// Without this claim, matching bytes may be an independent user edit.
				// Recheck terminal withdrawal after the claim lookup to close cleanup's race.
				withdrawn = withdrawn || marker(root, filepath.Join(path, "abandoned"))
				continue
			}
			if entry.Base == "" {
				return nil, fs.ErrNotExist
			}
			return entry.Before, nil
		}
	}
	if withdrawn {
		// This could be a stale, never-committed proposal observed just before
		// withdrawal, or a later edit with identical bytes. Retry a stable read;
		// do not expose a possibly unaccepted creation or conceal the later edit.
		return nil, fmt.Errorf("record view changed during concurrent withdrawal: %w", ErrConflict)
	}
	return data, nil
}

func committedBatch(root *os.Root, directory string) (bool, error) {
	return batchMarker(root, filepath.Join(directory, "committed"))
}

func (s *Store) BatchStatus(ctx context.Context, key string) (Receipt, error) {
	if s == nil {
		return Receipt{}, errors.New("store is nil")
	}
	if err := ctx.Err(); err != nil {
		return Receipt{}, err
	}
	root, err := os.OpenRoot(s.path)
	if err != nil {
		return Receipt{}, err
	}
	defer root.Close()
	if err := regularFile(root, ".lock"); err != nil {
		return Receipt{Key: key, Status: "blocked"}, err
	}
	lock := flock.New(filepath.Join(s.path, ".lock"))
	defer lock.Close()
	acquired, err := lock.TryRLockContext(ctx, 10*time.Millisecond)
	if err != nil {
		return Receipt{Key: key, Status: "blocked"}, err
	}
	if !acquired {
		return Receipt{Key: key, Status: "blocked"}, ctx.Err()
	}
	directory := batchDirectory(key)
	journal, err := loadJournal(root, directory)
	if err != nil {
		return Receipt{Key: key, Status: "unavailable"}, err
	}
	committed, err := committedBatch(root, directory)
	if err != nil {
		return receiptFor(journal, "blocked", err), err
	}
	if committed {
		return receiptFor(journal, "committed", nil), nil
	}
	withdrawal, err := abandonment(root, directory)
	if err != nil {
		return receiptFor(journal, "blocked", err), err
	}
	if withdrawal != "" {
		return receiptFor(journal, withdrawal, nil), nil
	}
	for _, entry := range journal.Entries {
		data, err := readFile(root, entry.Source)
		if err == nil {
			actual := dataRevision(data)
			if actual != entry.Base && actual != entry.Revision {
				return receiptFor(journal, "blocked", ErrConflict), nil
			}
		} else if entry.Base != "" || !errors.Is(err, fs.ErrNotExist) {
			return receiptFor(journal, "blocked", err), nil
		}
	}
	return receiptFor(journal, "pending", nil), nil
}

func syncBatchJournal(root *os.Root, directory string) error {
	entries, err := fs.ReadDir(root.FS(), directory)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if len(name) == 69 && strings.HasSuffix(name, ".json") && validHash(strings.TrimSuffix(name, ".json")) {
			if err := syncPath(root, filepath.Join(directory, name)); err != nil {
				return err
			}
		}
	}
	for _, path := range []string{directory, ".batches", "."} {
		if err := syncPath(root, path); err != nil {
			return err
		}
	}
	return nil
}
