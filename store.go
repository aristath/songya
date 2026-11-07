package songya

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"
)

type Destination string

const (
	Memories Destination = "memories"
	Entities Destination = "entities"
	Topics   Destination = "topics"
	Stories  Destination = "stories"
)

var destinations = []Destination{Memories, Entities, Topics, Stories}

func (d Destination) prefix() (string, error) {
	switch d {
	case Memories:
		return "m_", nil
	case Entities:
		return "e_", nil
	case Topics:
		return "t_", nil
	case Stories:
		return "s_", nil
	}
	return "", fmt.Errorf("unknown destination %q", d)
}

func recordName(id string) (string, error) {
	if ValidID(id) {
		for _, destination := range destinations {
			prefix, _ := destination.prefix()
			if id[:2] == prefix {
				return filepath.Join(string(destination), id+".md"), nil
			}
		}
	}
	return "", fmt.Errorf("invalid record ID %q", id)
}

// Store holds no open files between calls. Diagnostics is safe across goroutines.
type Store struct {
	path       string
	configPath string
	embeddings EmbeddingConfig
	issues     sync.Map
	initMu     sync.Mutex
	needsInit  bool
}

// Directory is the canonical root used by applications storing companion state.
func (s *Store) Directory() string { return s.path }

func (s *Store) note(path string, err error) {
	if err == nil {
		s.issues.Delete(path)
	} else {
		s.issues.Store(path, err.Error())
	}
}

func (s *Store) Diagnostics() []string {
	var messages []string
	s.issues.Range(func(path, issue any) bool {
		messages = append(messages, fmt.Sprintf("%s: %s", path, issue))
		return true
	})
	sort.Strings(messages)
	return messages
}

// Open opens the root and lock. Problems in individual directories are reported
// through Diagnostics rather than disabling healthy parts of the store.
func Open(path string, configuration ...EmbeddingConfig) (*Store, error) {
	return openStore(path, configuration, true)
}

// OpenExisting opens without creating record directories or publishing recovery
// work. Query clients can report missing sources instead of silently replacing
// them with empty directories. Normal writers keep Open's automatic recovery.
func OpenExisting(path string, configuration ...EmbeddingConfig) (*Store, error) {
	return openStore(path, configuration, false)
}

func openStore(path string, configuration []EmbeddingConfig, initialize bool) (*Store, error) {
	if path == "" {
		return nil, errors.New("store path must not be empty")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if initialize {
		if err := os.MkdirAll(path, 0700); err != nil {
			return nil, err
		}
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	config := EmbeddingConfig{}
	if len(configuration) > 0 {
		config = configuration[0]
	}
	s := &Store{path: path, needsInit: true, embeddings: config.defaults()}
	s.note("close root", root.Close())
	if initialize {
		_ = s.Recover()
	}
	return s, nil
}

// Only an initialization known to be unfinished may create the lock. A missing
// lock on an already initialized handle must not replace another writer's lock.
func (s *Store) initialize(root *os.Root) (err error) {
	s.initMu.Lock()
	defer s.initMu.Unlock()
	defer func() { s.note("initialization", err) }()
	if !s.needsInit {
		return regularFile(root, ".lock")
	}
	if _, err = root.Lstat(".lock"); errors.Is(err, fs.ErrNotExist) {
		if err = syncParents(s.path); err != nil {
			return err
		}
		var file *os.File
		file, err = root.OpenFile(".lock", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
		if err == nil {
			err = file.Close()
		}
	}
	if err == nil || errors.Is(err, fs.ErrExist) {
		err = regularFile(root, ".lock")
	}
	if err == nil {
		s.needsInit = false
	}
	return err
}

// An initialized store does not need its ancestors reopened on later calls.
func syncParents(path string) error {
	for parent := filepath.Dir(path); ; parent = filepath.Dir(parent) {
		directory, err := os.Open(parent)
		if err != nil {
			return err
		}
		if err := errors.Join(directory.Sync(), directory.Close()); err != nil {
			return err
		}
		if parent == filepath.Dir(parent) {
			return nil
		}
	}
}

func ensureDirectory(root *os.Root, name string) error {
	info, err := root.Lstat(name)
	if err == nil {
		if info.IsDir() {
			return nil
		}
		return fmt.Errorf("%s is not a directory", name)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := root.Mkdir(name, 0700); err != nil {
		return err
	}
	return syncPath(root, ".")
}

// Recover resumes completed pending writes when the write lock is available.
// Open reports deferred recovery without blocking access to readable records.
func (s *Store) Recover() error {
	err := s.locked(false, func(root *os.Root) error {
		// The cache is disposable, unlike the record directories.
		if info, err := root.Lstat(".index"); err == nil && !info.IsDir() {
			_ = root.Remove(".index")
		}
		s.note(".index", ensureDirectory(root, ".index"))
		directories := []string{}
		for _, destination := range destinations {
			name := string(destination)
			err := ensureDirectory(root, name)
			s.note(name, err)
			if err == nil {
				directories = append(directories, name)
			}
		}
		s.recoverBatches(root)
		for _, name := range directories {
			s.recoverDirectory(root, name)
		}
		return nil
	})
	if s != nil {
		s.note("recovery", err)
	}
	return err
}

// Create assigns the filename ID and UTC timestamps. Metadata problems are
// diagnostics, not reasons to discard the body. Pending=true means the complete
// record is durably staged and readable, while final publication will be retried.
func (s *Store) Create(destination Destination, fields map[string]any, body string) (*Document, error) {
	destination = Destination(strings.ToLower(strings.TrimSpace(string(destination))))
	prefix, err := destination.prefix()
	if err != nil {
		return nil, err
	}
	values := make(map[string]any, len(fields)+3)
	for name, value := range fields {
		if name != "id" && name != "created_at" && name != "updated_at" {
			values[name] = value
		}
	}
	if _, exists := values["title"]; !exists {
		values["title"] = ""
	}
	now := time.Now().UTC()
	values["created_at"], values["updated_at"] = now, now
	if destination == Memories && values["time"] == nil {
		values["time"] = map[string]any{}
	}
	node, warnings := encodeValue(values)
	d := &Document{Body: body, warnings: warnings}
	assignNode(&d.Frontmatter, node)
	if err := requireUser(d, destination); err != nil {
		return d, err
	}
	if err := validateTimeChange(nil, metadataField(&d.Frontmatter, "time")); err != nil {
		return d, err
	}
	var data []byte
	err = s.locked(true, func(root *os.Root) error {
		if err := s.validateTypeChange(context.Background(), root, &Document{}, d, destination, nil); err != nil {
			return err
		}
		var err error
		data, err = d.Marshal()
		if err != nil {
			return err
		}
		err = ensureDirectory(root, string(destination))
		s.note(string(destination), err)
		if err != nil {
			return err
		}
		if err := syncPath(root, "."); err != nil {
			return err
		}
		for {
			id, err := NewID(prefix)
			if err != nil {
				return err
			}
			name := filepath.Join(string(destination), id+".md")
			if _, err := root.Lstat(name); err == nil {
				continue
			} else if !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			retained, err := stage(root, name, "", data)
			d.Pending = retained
			if retained {
				d.ID = id
			}
			if err != nil {
				if !retained && errors.Is(err, fs.ErrExist) {
					continue
				}
				return err
			}
			published, err := s.publish(root, name, data)
			d.Pending = !published
			s.note(name, err)
			if err != nil {
				d.warnings = append(d.warnings, err.Error())
			}
			if errors.Is(err, fs.ErrExist) {
				return err
			} // Conflicting data cannot confirm this save.
			return nil // The synced pending record remains readable even if publication is blocked.
		}
	})
	if err == nil && !d.Pending {
		d.Revision = dataRevision(data)
	}
	return d, err
}

func (s *Store) Read(id string) (*Document, error) {
	name, err := recordName(id)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, errors.New("store is nil")
	}
	root, err := os.OpenRoot(s.path)
	if err != nil {
		return nil, err
	}
	defer func() { s.note("close root", root.Close()) }()
	doc, readErr := readCopy(root, id, name)
	if doc != nil && !doc.Pending {
		return doc, nil
	}
	if _, err := root.Lstat(pendingName(name)); errors.Is(err, fs.ErrNotExist) {
		return doc, readErr
	}
	recoveryErr := s.locked(false, func(root *os.Root) error {
		_, err := s.complete(root, name)
		s.note(name, err)
		return err
	})
	if latest, err := readCopy(root, id, name); err == nil {
		doc, readErr = latest, nil
	} else if doc != nil {
		doc.warnings = append(doc.warnings, "Readable snapshot retained: "+err.Error())
	} else {
		readErr = err
	}
	if doc == nil {
		return nil, fmt.Errorf("read %s: %w", name, errors.Join(readErr, recoveryErr))
	}
	if recoveryErr != nil && doc.Pending && !slices.Contains(doc.warnings, recoveryErr.Error()) {
		doc.warnings = append(doc.warnings, recoveryErr.Error())
	}
	return doc, nil
}

// One policy for every read: committed content wins; staged bytes stay useful
// even when incomplete. Recheck the committed name after concurrent cleanup.
func readCopy(root *os.Root, id, name string) (*Document, error) {
	return readCopyWith(root, id, name, readRecordFile)
}

// The caller already holds the exclusive store lock. No writer can overlap
// this snapshot; retained pre-batch views and staged data still apply.
func readLockedCopy(root *os.Root, id, name string) (*Document, error) {
	return readCopyWith(root, id, name, func(root *os.Root, name string) ([]byte, error) {
		data, err := readFile(root, name)
		return batchView(root, name, data, err)
	})
}

func readCopyWith(root *os.Root, id, name string, read func(*os.Root, string) ([]byte, error)) (*Document, error) {
	data, err := read(root, name)
	pending := false
	var warning string
	if err != nil {
		committedErr := err
		var temporary, base string
		data, temporary, base, err = stagedData(root, name)
		if err == nil && base != "" {
			err = fmt.Errorf("pending update requires a committed base: %w", fs.ErrNotExist)
		}
		if err == nil {
			pending = true
			if temporary != temporaryName(name, "", data) {
				warning = errUnverifiedPending.Error()
			}
		} else {
			stagedErr := err
			data, err = read(root, name)
			if err != nil {
				if errors.Is(stagedErr, fs.ErrNotExist) {
					return nil, committedErr
				}
				return nil, stagedErr
			}
		}
	}
	doc := Parse(data)
	doc.source = data
	doc.ID, doc.Pending = id, pending
	doc.Unconfirmed = warning != ""
	if issue := batchReadIssue(root, name); issue != nil {
		doc.Unconfirmed = true
		doc.warnings = append(doc.warnings, issue.Error())
	}
	if warning != "" {
		doc.warnings = append(doc.warnings, warning)
	}
	if !pending && !doc.Unconfirmed {
		doc.Revision = dataRevision(data)
		if _, err := root.Lstat(pendingName(name)); err == nil {
			doc.warnings = append(doc.warnings, pendingWarning)
		}
	}
	return doc, nil
}

func (s *Store) locked(wait bool, operation func(*os.Root) error) error {
	if s == nil {
		return errors.New("store is nil")
	}
	root, err := os.OpenRoot(s.path)
	if err != nil {
		return err
	}
	defer func() { s.note("close root", root.Close()) }()
	if err := s.initialize(root); err != nil {
		return err
	}
	lock := flock.New(filepath.Join(s.path, ".lock"))
	defer func() { s.note("close lock", lock.Close()) }()
	if wait {
		if err := lock.Lock(); err != nil {
			return err
		}
	} else {
		acquired, err := lock.TryLock()
		if err != nil {
			return err
		}
		if !acquired {
			return errors.New("recovery deferred while another writer holds the lock")
		}
	}
	return operation(root)
}

func regularFile(root *os.Root, name string) error {
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", name)
	}
	return nil
}

func readFile(root *os.Root, name string) ([]byte, error) {
	if err := regularFile(root, name); err != nil {
		return nil, err
	}
	return root.ReadFile(name)
}

func syncPath(root *os.Root, name string) error {
	file, err := root.Open(name)
	if err != nil {
		return err
	}
	err = file.Sync()
	if err != nil {
		err = file.Sync()
	}
	return errors.Join(err, file.Close())
}
