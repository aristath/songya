package songya

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type FileChange struct {
	Key      string `json:"key"`
	ID       string `json:"id"`
	Revision string `json:"revision"`
	Action   string `json:"action"` // add, replace, remove
	Upload   string `json:"upload,omitempty"`
	Filename string `json:"filename,omitempty"`
}
type FileReceipt struct {
	Key      string `json:"key"`
	ID       string `json:"id"`
	Filename string `json:"filename"`
	Status   string `json:"status"`
	Revision string `json:"revision,omitempty"`
}
type fileJournal struct {
	Request    FileChange
	Filename   string
	BeforeFile string
	AfterFile  string
	Record     []byte
}

func fileOperation(key string) string { return filepath.Join(".file-ops", dataRevision([]byte(key))) }
func readFileJournal(root *os.Root, directory string) (fileJournal, error) {
	var journal fileJournal
	data, err := readFile(root, filepath.Join(directory, "journal.json"))
	if err != nil {
		return journal, err
	}
	if err := json.Unmarshal(data, &journal); err != nil {
		return journal, err
	}
	if fileOperation(journal.Request.Key) != directory || !ValidID(journal.Request.ID) || !validHash(journal.Request.Revision) || len(journal.Record) == 0 {
		return journal, errors.New("File operation journal damaged; retained")
	}
	if _, err := attachmentName(journal.Filename); err != nil {
		return journal, err
	}
	// The complete proposal also has an immutable checksum-named copy.
	retained, err := readFile(root, filepath.Join(directory, dataRevision(data)+".json"))
	if err != nil || string(retained) != string(data) {
		return journal, errors.New("File operation checksum failed; retained")
	}
	return journal, nil
}
func uniqueFilename(root *os.Root, directory, name string) (string, error) {
	extension := filepath.Ext(name)
	stem := strings.TrimSuffix(name, extension)
	for n := 1; ; n++ {
		candidate := name
		if n > 1 {
			candidate = fmt.Sprintf("%s (%d)%s", stem, n, extension)
		}
		if _, err := root.Lstat(filepath.Join(directory, candidate)); errors.Is(err, fs.ErrNotExist) {
			return candidate, nil
		} else if err != nil {
			return "", err
		}
	}
}

// ChangeFile binds the caller's retry key to the exact operation. A durable
// journal precedes every effect, and a per-record reservation protects both text
// and file changes until the operation is fully committed.
func (s *Store) ChangeFile(ctx context.Context, request FileChange) (FileReceipt, error) {
	receipt := FileReceipt{Key: request.Key, ID: request.ID, Status: "unavailable"}
	if request.Key == "" || request.Revision == "" {
		return receipt, errors.New("Use a retry key and the revision from the latest record read")
	}
	if request.Action != "add" && request.Action != "replace" && request.Action != "remove" {
		return receipt, errors.New("Choose add, replace, or remove")
	}
	err := s.lockedContext(ctx, func(root *os.Root) error {
		directory := fileOperation(request.Key)
		journal, err := readFileJournal(root, directory)
		if err == nil {
			if journal.Request != request {
				return ErrConflict
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		} else {
			if err := s.recoverRecordFiles(root, request.ID); err != nil {
				return err
			}
			recordDir, err := recordDirectory(root, request.ID)
			if err != nil {
				return err
			}
			name, _ := recordName(request.ID)
			if _, err := root.Lstat(pendingName(name)); !errors.Is(err, fs.ErrNotExist) {
				return errors.New("This record has unfinished work; recover it before changing files")
			}
			doc, err := readLockedCopy(root, request.ID, name)
			if err != nil {
				return err
			}
			if doc.Pending || doc.Unconfirmed || doc.Revision != request.Revision {
				return ErrConflict
			}
			journal.Request = request
			if request.Action != "remove" {
				upload, err := readUpload(root, request.Upload)
				if err != nil {
					return err
				}
				uploadDir, _ := uploadDirectory(upload.ID)
				actual, err := fileHash(root, filepath.Join(uploadDir, "content"))
				if err != nil {
					return fmt.Errorf("Completed upload unavailable: %w", err)
				}
				if actual != upload.SHA256 {
					return errors.New("Upload bytes changed; retained for repair")
				}
				journal.AfterFile = actual
				filename := upload.Filename
				if request.Filename != "" {
					filename = request.Filename
				}
				if _, err := attachmentName(filename); err != nil {
					return err
				}
				if request.Action == "add" {
					filename, err = uniqueFilename(root, recordDir, filename)
					if err != nil {
						return err
					}
				}
				journal.Filename = filename
			} else {
				journal.Filename = request.Filename
			}
			if _, err := attachmentName(journal.Filename); err != nil {
				return err
			}
			if request.Action != "add" {
				journal.BeforeFile, err = fileHash(root, filepath.Join(recordDir, journal.Filename))
				if err != nil {
					return err
				}
			}
			// Set only the engine's update time; never change body/owner/original accounts.
			if err := doc.Set("updated_at", time.Now().UTC()); err != nil {
				return err
			}
			journal.Record, err = doc.Marshal()
			if err != nil {
				return err
			}
			if err := ensureDirectory(root, ".file-ops"); err != nil {
				return err
			}
			if err := ensureDirectory(root, directory); err != nil {
				return err
			}
			data, _ := json.Marshal(journal)
			immutable := filepath.Join(directory, dataRevision(data)+".json")
			if err := publishImmutable(root, immutable, data); err != nil {
				return err
			}
			if err := root.Link(immutable, filepath.Join(directory, "journal.json")); err != nil {
				return err
			}
			if err := syncPath(root, directory); err != nil {
				return err
			}
		}
		receipt.Filename = journal.Filename
		receipt.Status = "pending"
		if err := s.resumeFileChange(root, journal); err != nil {
			return err
		}
		receipt.Status = "committed"
		receipt.Revision = dataRevision(journal.Record)
		return nil
	})
	return receipt, err
}
func (s *Store) resumeFileChange(root *os.Root, journal fileJournal) error {
	request := journal.Request
	directory := fileOperation(request.Key)
	// Retrying a completed request cannot replace later edits or resurrect files.
	if marker(root, filepath.Join(directory, "committed")) {
		if err := syncPath(root, directory); err != nil {
			return err
		}
		s.cleanupFileChange(root, journal)
		return nil
	}
	recordDir, err := recordDirectory(root, request.ID)
	if err != nil {
		return err
	}
	name, _ := recordName(request.ID)
	current, err := readFile(root, name)
	if err != nil {
		return err
	}
	afterRevision := dataRevision(journal.Record)
	if revision := dataRevision(current); revision != request.Revision && revision != afterRevision {
		return ErrConflict
	}
	// Re-establish journal durability before effects, including after a lost ack.
	for _, path := range []string{filepath.Join(directory, "journal.json"), directory, ".file-ops"} {
		if err := syncPath(root, path); err != nil {
			return err
		}
	}
	if err := ensureDirectory(root, pendingName(name)); err != nil {
		return err
	}
	claim := filepath.Join(pendingName(name), "files")
	if err := syncNew(root, claim, []byte(filepath.Base(directory))); err != nil {
		return err
	}
	if err := syncPath(root, pendingName(name)); err != nil {
		return err
	}
	if err := syncPath(root, recordDir); err != nil {
		return err
	}
	target := filepath.Join(recordDir, journal.Filename)
	actual, readErr := fileHash(root, target)
	if request.Action == "remove" {
		if readErr == nil {
			if actual != journal.BeforeFile {
				return ErrConflict
			}
			if err := root.Remove(target); err != nil {
				return err
			}
		} else if !errors.Is(readErr, fs.ErrNotExist) {
			return readErr
		}
	} else if readErr == nil && actual == journal.AfterFile {
		if err := syncPath(root, target); err != nil {
			return err
		}
	} else {
		if request.Action == "add" {
			if !errors.Is(readErr, fs.ErrNotExist) {
				return ErrConflict
			}
		} else if readErr != nil || actual != journal.BeforeFile {
			return ErrConflict
		}
		uploadDir, _ := uploadDirectory(request.Upload)
		content := filepath.Join(uploadDir, "content")
		actual, err := fileHash(root, content)
		if err != nil {
			return err
		}
		if actual != journal.AfterFile {
			return errors.New("Retained upload checksum failed; no file was replaced")
		}
		if err := syncPath(root, content); err != nil {
			return err
		}
		if request.Action == "add" {
			if err := root.Link(content, target); err != nil {
				return err
			}
		} else {
			temporary := filepath.Join(directory, "replacement")
			if err := root.Link(content, temporary); err != nil && !errors.Is(err, fs.ErrExist) {
				return err
			}
			if actual, err := fileHash(root, temporary); err != nil || actual != journal.AfterFile {
				return errors.New("Replacement checksum failed; retained")
			}
			if err := root.Rename(temporary, target); err != nil {
				return err
			}
		}
	}
	if err := syncPath(root, recordDir); err != nil {
		return err
	}
	// An external editor may not honor our advisory lock. Recheck the prose.
	latest, err := readFile(root, name)
	if err != nil {
		return err
	}
	if revision := dataRevision(latest); revision != request.Revision && revision != afterRevision {
		return ErrConflict
	}
	// Publish the exact retained timestamp proposal. A retry never creates another.
	if dataRevision(latest) != afterRevision {
		temporary := filepath.Join(directory, "record.tmp")
		if err := syncNew(root, temporary, journal.Record); err != nil {
			return err
		}
		if err := root.Rename(temporary, name); err != nil {
			return err
		}
	}
	if err := syncPath(root, name); err != nil {
		return err
	}
	if err := syncPath(root, recordDir); err != nil {
		return err
	}
	if err := mark(root, filepath.Join(directory, "committed")); err != nil {
		return err
	}
	s.cleanupFileChange(root, journal)
	return nil
}
func (s *Store) cleanupFileChange(root *os.Root, journal fileJournal) {
	name, _ := recordName(journal.Request.ID)
	directory := fileOperation(journal.Request.Key)
	claim := filepath.Join(pendingName(name), "files")
	if data, err := readFile(root, claim); err == nil && string(data) == filepath.Base(directory) {
		s.note(claim, root.Remove(claim))
		s.note(pendingName(name), root.Remove(pendingName(name)))
		s.note(name+".files-cleanup", syncPath(root, filepath.Dir(name)))
	}
	if journal.Request.Upload != "" {
		uploadDir, _ := uploadDirectory(journal.Request.Upload)
		// Keep the result for lost acknowledgments and the chat's honest status.
		receipt := FileReceipt{Key: journal.Request.Key, ID: journal.Request.ID, Filename: journal.Filename, Status: "committed", Revision: dataRevision(journal.Record)}
		data, _ := json.Marshal(receipt)
		if err := publishImmutable(root, filepath.Join(uploadDir, "attached.json"), data); err != nil {
			s.note(uploadDir, err)
			return
		}
		if err := syncPath(root, uploadDir); err != nil {
			s.note(uploadDir, err)
			return
		}
		// Bytes are now owned by the record folder.
		if err := mark(root, filepath.Join(uploadDir, "attached")); err != nil {
			s.note(uploadDir, err)
			return
		}
		if err := root.Remove(filepath.Join(uploadDir, "content")); err != nil && !errors.Is(err, fs.ErrNotExist) {
			s.note(uploadDir, err)
			return
		}
		s.note(uploadDir, syncPath(root, uploadDir))
	}
}
func (s *Store) recoverFileChanges(root *os.Root) {
	entries, err := fs.ReadDir(root.FS(), ".file-ops")
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		s.note(".file-ops", err)
	}
	for _, entry := range entries {
		if !validHash(entry.Name()) || !entry.IsDir() {
			continue
		}
		directory := filepath.Join(".file-ops", entry.Name())
		journal, err := readFileJournal(root, directory)
		if err == nil {
			err = s.resumeFileChange(root, journal)
			if err == nil {
				s.cleanupFileChange(root, journal)
			}
		}
		s.note(directory, err)
	}
}

// A process may stop after accepting a journal but before publishing its claim.
// Writers resolve that record's accepted work before validating a new revision.
// Queries and streaming responses do not scan operation history.
func (s *Store) recoverRecordFiles(root *os.Root, id string) error {
	entries, err := fs.ReadDir(root.FS(), ".file-ops")
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		s.note(".file-ops", err)
		return nil // Individual pending claims still protect affected records.
	}
	for _, entry := range entries {
		if !validHash(entry.Name()) || !entry.IsDir() {
			continue
		}
		directory := filepath.Join(".file-ops", entry.Name())
		if marker(root, filepath.Join(directory, "committed")) {
			continue
		}
		journal, err := readFileJournal(root, directory)
		if err != nil {
			s.note(directory, err)
			continue
		}
		if journal.Request.ID != id {
			continue
		}
		if err := s.resumeFileChange(root, journal); err != nil {
			return fmt.Errorf("File change %s is retained for recovery: %w", journal.Request.Key, err)
		}
	}
	return nil
}

// FileChangeReceipt checks a retained operation without replaying it. Clients can
// resolve a lost acknowledgment using the returned key instead of a new save.
func (s *Store) FileChangeReceipt(ctx context.Context, key string) (FileReceipt, error) {
	receipt := FileReceipt{Key: key, Status: "unavailable"}
	err := s.lockedContext(ctx, func(root *os.Root) error {
		directory := fileOperation(key)
		journal, err := readFileJournal(root, directory)
		if err != nil {
			return err
		}
		receipt.ID = journal.Request.ID
		receipt.Filename = journal.Filename
		receipt.Status = "pending"
		if marker(root, filepath.Join(directory, "committed")) {
			receipt.Status = "committed"
			receipt.Revision = dataRevision(journal.Record)
		}
		return nil
	})
	return receipt, err
}
