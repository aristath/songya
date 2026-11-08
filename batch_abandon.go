package songya

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// AbandonBatch explicitly withdraws an uncommitted batch. Its journal, original
// account, and proposals stay intact. Only bytes published by this exact batch
// are rolled back; later external edits or deletions are never replaced.
func (s *Store) AbandonBatch(ctx context.Context, key string) (Receipt, error) {
	receipt := Receipt{Key: key, Status: "unavailable"}
	err := s.lockedContext(ctx, func(root *os.Root) error {
		directory := batchDirectory(key)
		journal, err := loadJournal(root, directory)
		if err != nil {
			return err
		}
		receipt = receiptFor(journal, "blocked", nil)
		if err := syncBatchJournal(root, directory); err != nil {
			return err
		}
		committed, err := committedBatch(root, directory)
		if err != nil {
			return err
		}
		if committed {
			receipt.Status = "committed"
			return errors.New("a committed batch cannot be abandoned; submit a revision-checked change instead")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := mark(root, filepath.Join(directory, "abandoning")); err != nil {
			return err
		}
		// Once withdrawal is durable, interruption only defers its completion.
		if err := s.resumeAbandon(root, journal); err != nil {
			return err
		}
		receipt.Status = "abandoned"
		return nil
	})
	if err != nil {
		receipt.Diagnostics = boundedDiagnostics(append(receipt.Diagnostics, Diagnostic{batchDirectory(key), err.Error()}))
	}
	return receipt, err
}

func (s *Store) resumeAbandon(root *os.Root, journal batchJournal) error {
	directory := batchDirectory(journal.Key)
	abandoned, err := batchMarker(root, filepath.Join(directory, "abandoned"))
	if err != nil {
		return err
	}
	if abandoned {
		if err := mark(root, filepath.Join(directory, "abandoned")); err != nil {
			return err
		}
		for _, entry := range journal.Entries {
			s.clearBatchClaim(root, entry.target(), []byte(filepath.Base(directory)))
		}
		return nil
	}
	for _, entry := range journal.Entries {
		current, err := readFile(root, entry.target())
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if dataRevision(current) != entry.Revision {
			continue
		}
		if entry.Base == "" {
			// Recheck immediately before deletion, with the same advisory-lock limit
			// as publication when an external editor does not coordinate with us.
			current, err = readFile(root, entry.target())
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			if dataRevision(current) != entry.Revision {
				continue
			}
			if err := root.Remove(entry.target()); err != nil {
				return err
			}
		} else {
			temporary := filepath.Join(directory, entry.ID+"."+entry.Base+".rollback")
			if err := syncNew(root, temporary, entry.Before); err != nil {
				return err
			}
			current, err = readFile(root, entry.target())
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			if dataRevision(current) != entry.Revision {
				continue
			}
			if err := root.Rename(temporary, entry.target()); err != nil {
				return err
			}
		}
		if err := syncPath(root, filepath.Dir(entry.target())); err != nil {
			return err
		}
	}
	// Sync every affected directory on retries too: the previous process may have
	// died after a rollback rename but before its directory sync.
	for _, entry := range journal.Entries {
		if err := syncPath(root, filepath.Dir(entry.target())); err != nil {
			return err
		}
	}
	if err := mark(root, filepath.Join(directory, "abandoned")); err != nil {
		return err
	}
	for _, entry := range journal.Entries {
		s.clearBatchClaim(root, entry.target(), []byte(filepath.Base(directory)))
	}
	return nil
}

func abandonment(root *os.Root, directory string) (string, error) {
	for _, name := range []string{"abandoned", "abandoning"} {
		present, err := batchMarker(root, filepath.Join(directory, name))
		if err != nil {
			return "", err
		}
		if present {
			return name, nil
		}
	}
	return "", nil
}

func batchMarker(root *os.Root, path string) (bool, error) {
	data, err := readFile(root, path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if len(data) != 0 {
		return false, fmt.Errorf("damaged batch marker at %s; retained", path)
	}
	return true, nil
}
