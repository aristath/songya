package songya

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

var errUnverifiedPending = errors.New("pending checksum cannot be verified; data retained")

const pendingWarning = "Pending work is retained for recovery; committed content returned"

func dataRevision(data []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func pendingName(name string) string {
	return filepath.Join(filepath.Dir(name), "."+filepath.Base(name)+".pending")
}

// The directory reserves the ID; its checksum-named file records the complete
// intended write. No scan of other records is needed to claim an ID.
func temporaryName(name, base string, data []byte) string {
	if base != "" {
		base += "."
	}
	return filepath.Join(pendingName(name), base+dataRevision(data)+".tmp")
}

func stage(root *os.Root, name, base string, data []byte) (bool, error) {
	if err := ensureDirectory(root, filepath.Dir(name)); err != nil {
		return false, err
	}
	if err := root.Mkdir(pendingName(name), 0700); err != nil {
		return false, err
	}
	temporary := temporaryName(name, base, data)
	file, err := root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return true, err
	}
	_, writeErr := file.Write(data)
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return true, err
	}
	return true, prepare(root, name, base, temporary, data)
}

func prepare(root *os.Root, name, base, temporary string, data []byte) error {
	if temporary != temporaryName(name, base, data) {
		return errUnverifiedPending
	}
	for _, path := range []string{temporary, pendingName(name), filepath.Dir(name)} {
		if err := syncPath(root, path); err != nil {
			return err
		}
	}
	return nil
}

// Unknown files are retained. Multiple candidate records are ambiguous, so none
// is chosen for publication merely because it appeared first in a listing.
func stagedData(root *os.Root, name string) ([]byte, string, string, error) {
	directory := pendingName(name)
	info, err := root.Lstat(directory)
	if err != nil {
		return nil, "", "", err
	}
	if !info.IsDir() {
		return nil, "", "", fmt.Errorf("%s is not a staging directory", directory)
	}
	entries, err := fs.ReadDir(root.FS(), directory)
	if err != nil {
		return nil, "", "", err
	}
	var temporary, base string
	for _, entry := range entries {
		parts := strings.Split(entry.Name(), ".")
		if len(parts) != 2 && len(parts) != 3 || parts[len(parts)-1] != "tmp" {
			continue
		}
		valid := true
		for _, hash := range parts[:len(parts)-1] {
			if _, err := hex.DecodeString(hash); len(hash) != 64 || err != nil {
				valid = false
			}
		}
		if !valid {
			continue
		}
		if temporary != "" {
			return nil, "", "", fmt.Errorf("multiple staged records for %s; all retained", name)
		}
		temporary = filepath.Join(directory, entry.Name())
		if len(parts) == 3 {
			base = parts[0]
		}
	}
	if temporary == "" {
		return nil, "", "", fmt.Errorf("unfinished write for %s is retained without a readable record", name)
	}
	data, err := readFile(root, temporary)
	return data, temporary, base, err
}

func (s *Store) complete(root *os.Root, name string) (bool, error) {
	if owner, err := readFile(root, filepath.Join(pendingName(name), "files")); err == nil {
		if !validHash(string(owner)) {
			return false, errors.New("File operation ownership is damaged; retained")
		}
		journal, err := readFileJournal(root, filepath.Join(".file-ops", string(owner)))
		if err != nil {
			return false, err
		}
		err = s.resumeFileChange(root, journal)
		if err == nil {
			s.cleanupFileChange(root, journal)
		}
		return err == nil, err
	}
	if filepath.Base(name) == "record.md" {
		if err := recordWritable(root, filepath.Base(filepath.Dir(name))); err != nil {
			return false, err
		}
	}
	data, temporary, base, err := stagedData(root, name)
	if err != nil {
		// A crash during cleanup may leave an empty reservation after the final
		// record is durable. Remove only an empty directory, never unknown data.
		info, statErr := root.Lstat(pendingName(name))
		if _, readErr := readFile(root, name); readErr == nil && statErr == nil && info.IsDir() {
			if removeErr := root.Remove(pendingName(name)); removeErr == nil {
				s.note(name+".cleanup", syncPath(root, filepath.Dir(name)))
				return true, nil
			}
		}
		return false, err
	}
	if err := prepare(root, name, base, temporary, data); err != nil {
		return false, err
	}
	if base != "" {
		return s.publishUpdate(root, name, base, data)
	}
	return s.publish(root, name, data)
}

// Both a new write and recovered work arrive here with verified, synced staging
// data. A new hard link reuses that synced inode. An existing final file is
// checked and synced before its staged copy is removed.
func (s *Store) publish(root *os.Root, name string, data []byte) (bool, error) {
	temporary := temporaryName(name, "", data)
	if err := linkData(root, temporary, name, data); err != nil {
		return false, err
	}
	if err := syncPath(root, filepath.Dir(name)); err != nil {
		return false, err
	}
	s.cleanup(root, name, temporary)
	return true, nil
}

func linkData(root *os.Root, source, destination string, data []byte) error {
	if err := root.Link(source, destination); err != nil {
		if !errors.Is(err, fs.ErrExist) {
			return err
		}
		existing, readErr := readFile(root, destination)
		if readErr != nil || !bytes.Equal(data, existing) {
			return fmt.Errorf("pending data conflicts with %s: %w", destination, fs.ErrExist)
		}
		return syncPath(root, destination)
	}
	return nil
}

func (s *Store) cleanup(root *os.Root, name, temporary string) {
	// Publication is durable. Cleanup problems are diagnostics, never failed saves.
	s.note(temporary, root.Remove(temporary))
	s.note(pendingName(name), root.Remove(pendingName(name)))
	s.note(name+".cleanup", syncPath(root, filepath.Dir(name)))
}

func (s *Store) recoverDirectory(root *os.Root, directory string) {
	entries, err := fs.ReadDir(root.FS(), directory)
	if err == nil {
		err = syncPath(root, directory)
	}
	if err != nil {
		s.note(directory+".scan", err)
		return
	}
	s.note(directory+".scan", nil)
	s.issues.Range(func(key, _ any) bool {
		if strings.HasPrefix(key.(string), directory+string(filepath.Separator)) {
			s.issues.Delete(key)
		}
		return true
	})
	for _, entry := range entries {
		id := entry.Name()
		prefix, _ := Destination(directory).prefix()
		if !entry.IsDir() || !ValidID(id) || id[:2] != prefix {
			continue
		}
		name, _ := recordName(id)
		if _, err := root.Lstat(pendingName(name)); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		_, err := s.complete(root, name)
		s.note(name, err)
	}
}
