package songya

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// publishImmutable never exposes a partly written canonical receipt. Unfinished
// temporary files are retained outside the accepted name and do not block retry.
func publishImmutable(root *os.Root, name string, data []byte) error {
	if existing, err := readFile(root, name); err == nil {
		if string(existing) != string(data) {
			return ErrConflict
		}
		if err := syncPath(root, name); err != nil {
			return err
		}
		return syncPath(root, filepath.Dir(name))
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	temporary := filepath.Join(filepath.Dir(name), fmt.Sprintf(".%x.tmp", randomUploadSuffix()))
	if err := syncNew(root, temporary, data); err != nil {
		return err
	}
	if err := root.Link(temporary, name); err != nil {
		if !errors.Is(err, fs.ErrExist) {
			return err
		}
		existing, err := readFile(root, name)
		if err != nil {
			return err
		}
		if string(existing) != string(data) {
			return ErrConflict
		}
	}
	if err := syncPath(root, name); err != nil {
		return err
	}
	if err := syncPath(root, filepath.Dir(name)); err != nil {
		return err
	}
	_ = root.Remove(temporary)
	return nil
}
