package songya

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// A tiny durable tombstone survives cleanup. A stopped writer cannot resurrect
// the record from an old proposal, even if deletion was interrupted.
func deletionDirectory(id string) string { return filepath.Join(".deletions", id) }
func recordWritable(root *os.Root, id string) error {
	if !ValidID(id) {
		return nil
	} // Non-record batch entries have their own names.
	if _, err := root.Lstat(filepath.Join(deletionDirectory(id), "revision")); err == nil {
		return fmt.Errorf("%s was deleted; old writes cannot restore it", id)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
func (s *Store) deleteFolder(root *os.Root, id, revision string) error {
	directory := deletionDirectory(id)
	if err := ensureDirectory(root, ".deletions"); err != nil {
		return err
	}
	if err := ensureDirectory(root, directory); err != nil {
		return err
	}
	if err := publishImmutable(root, filepath.Join(directory, "revision"), []byte(revision)); err != nil {
		return err
	}
	if err := syncPath(root, directory); err != nil {
		return err
	}
	return s.finishDeletion(root, id)
}
func (s *Store) finishDeletion(root *os.Root, id string) error {
	name, err := recordName(id)
	if err != nil {
		return err
	}
	directory := deletionDirectory(id)
	revision, err := readFile(root, filepath.Join(directory, "revision"))
	if err != nil {
		return err
	}
	if !validHash(string(revision)) {
		return errors.New("deletion receipt damaged; files retained")
	}
	if marker(root, filepath.Join(directory, "complete")) {
		return nil
	}
	moved := filepath.Join(directory, "files")
	if _, err := root.Lstat(filepath.Dir(name)); err == nil {
		data, err := readFile(root, name)
		if err != nil {
			return err
		}
		if dataRevision(data) != string(revision) {
			return ErrConflict
		}
		if _, err := root.Lstat(moved); !errors.Is(err, fs.ErrNotExist) {
			return errors.New("both deletion locations exist; files retained")
		}
		if err := root.Rename(filepath.Dir(name), moved); err != nil {
			return err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := syncPath(root, filepath.Dir(filepath.Dir(name))); err != nil {
		return err
	}
	if err := syncPath(root, directory); err != nil {
		return err
	}
	// The intent is durable and the live name is absent before unlinking any bytes.
	// RemoveAll does not follow symlinks; a failed cleanup is safely resumable.
	if err := root.RemoveAll(moved); err != nil {
		return err
	}
	if err := syncPath(root, directory); err != nil {
		return err
	}
	return mark(root, filepath.Join(directory, "complete"))
}
func (s *Store) recoverDeletions(root *os.Root) {
	entries, err := fs.ReadDir(root.FS(), ".deletions")
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		s.note(".deletions", err)
	}
	for _, entry := range entries {
		if !ValidID(entry.Name()) || !entry.IsDir() {
			continue
		}
		s.note(deletionDirectory(entry.Name()), s.finishDeletion(root, entry.Name()))
	}
}
