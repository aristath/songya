package songya

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// Upload receipts are operational staging, not attachment records. The bytes
// leave staging once the record folder is durable. Receipts remain retryable.
type Upload struct {
	ID       string `json:"upload"`
	Filename string `json:"filename"`
	Scope    string `json:"scope"`
	Bytes    int64  `json:"bytes"`
	SHA256   string `json:"sha256"`
}

func uploadDirectory(id string) (string, error) {
	if !validHash(id) {
		return "", errors.New("Use the upload value returned after a completed upload")
	}
	return filepath.Join(".uploads", id), nil
}
func readUpload(root *os.Root, id string) (Upload, error) {
	directory, err := uploadDirectory(id)
	if err != nil {
		return Upload{}, err
	}
	data, err := readFile(root, filepath.Join(directory, "receipt.json"))
	if err != nil {
		return Upload{}, err
	}
	var receipt Upload
	if err := json.Unmarshal(data, &receipt); err != nil {
		return receipt, err
	}
	if receipt.ID != id || !validHash(receipt.SHA256) {
		return receipt, errors.New("Upload receipt damaged; retained for repair")
	}
	return receipt, nil
}

// Upload streams to a unique unfinished file outside the write lock. Only fully
// synced bytes and a synced receipt are acknowledged. Retrying the same key and
// bytes returns the same receipt; a reused key with different content conflicts.
func (s *Store) Upload(ctx context.Context, key, scope, filename string, reader io.Reader) (Upload, error) {
	var receipt Upload
	name, err := uploadName(filename)
	if err != nil {
		return receipt, err
	}
	if key == "" || scope == "" {
		return receipt, errors.New("Upload needs a retry key and an owner/session scope")
	}
	id := dataRevision([]byte(key))
	directory, _ := uploadDirectory(id)
	err = s.lockedContext(ctx, func(root *os.Root) error {
		if err := ensureDirectory(root, ".uploads"); err != nil {
			return err
		}
		return ensureDirectory(root, directory)
	})
	if err != nil {
		return receipt, err
	}
	root, err := os.OpenRoot(s.path)
	if err != nil {
		return receipt, err
	}
	defer root.Close()
	temporary := filepath.Join(directory, fmt.Sprintf(".%x.part", randomUploadSuffix()))
	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return receipt, err
	}
	hash := sha256.New()
	bytes, copyErr := io.Copy(io.MultiWriter(file, hash), &contextReader{ctx, reader})
	err = errors.Join(copyErr, file.Sync(), file.Close())
	if err != nil {
		_ = root.Remove(temporary)
		return receipt, err
	} // Not acknowledged; this attempt owns only its partial file.
	proposed := Upload{id, name, scope, bytes, fmt.Sprintf("%x", hash.Sum(nil))}
	err = s.lockedContext(ctx, func(root *os.Root) error {
		if existing, err := readUpload(root, id); err == nil {
			if existing != proposed {
				return ErrConflict
			}
			receipt = existing
			for _, path := range []string{filepath.Join(directory, "receipt.json"), directory, ".uploads"} {
				if err := syncPath(root, path); err != nil {
					return err
				}
			}
			if !marker(root, filepath.Join(directory, "attached")) {
				return syncPath(root, filepath.Join(directory, "content"))
			}
			return nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		content := filepath.Join(directory, "content")
		if err := root.Link(temporary, content); err != nil {
			if !errors.Is(err, fs.ErrExist) {
				return err
			}
			actual, err := fileHash(root, content)
			if err != nil {
				return err
			}
			if actual != proposed.SHA256 {
				return ErrConflict
			}
		}
		if err := syncPath(root, content); err != nil {
			return err
		}
		if err := syncPath(root, directory); err != nil {
			return err
		}
		data, _ := json.Marshal(proposed)
		if err := publishImmutable(root, filepath.Join(directory, "receipt.json"), data); err != nil {
			return err
		}
		receipt = proposed
		return nil
	})
	if err == nil {
		_ = root.Remove(temporary)
	} // Retain the complete bytes when final acknowledgement is uncertain.
	return receipt, err
}
func randomUploadSuffix() []byte { data := make([]byte, 16); _, _ = rand.Read(data); return data }

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

// Uploads recovers completed uploads after page reloads and lost acknowledgments.
// Scope comes from the active user/session, not from model-invented disk paths.
func (s *Store) Uploads(ctx context.Context, scope string) ([]Upload, error) {
	results := []Upload{}
	err := s.lockedContext(ctx, func(root *os.Root) error {
		entries, err := fs.ReadDir(root.FS(), ".uploads")
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if !validHash(entry.Name()) || !entry.IsDir() {
				continue
			}
			receipt, err := readUpload(root, entry.Name())
			if err != nil {
				s.note(filepath.Join(".uploads", entry.Name()), err)
				continue
			}
			if receipt.Scope != scope {
				continue
			}
			// A committed attachment no longer needs a staged payload in the list.
			if marker(root, filepath.Join(".uploads", entry.Name(), "attached")) {
				continue
			}
			results = append(results, receipt)
		}
		return nil
	})
	return results, err
}

// UploadState reports only operational status. The record folder is still the
// source of truth for its files, including after a later replacement/removal.
type UploadState struct {
	Upload
	Status           string `json:"status"`
	RecordID         string `json:"record_id,omitempty"`
	AttachedFilename string `json:"attached_filename,omitempty"`
}

// UploadStateByKey recovers a completed upload when its acknowledgement was lost.
func (s *Store) UploadStateByKey(ctx context.Context, key, scope string) (UploadState, error) {
	id := dataRevision([]byte(key))
	err := s.lockedContext(ctx, func(root *os.Root) error {
		upload, err := readUpload(root, id)
		if err != nil {
			return err
		}
		if upload.Scope != scope {
			return fs.ErrNotExist
		}
		directory, _ := uploadDirectory(id)
		// Finish uncertain syncing before acknowledging recovered work.
		if !marker(root, filepath.Join(directory, "attached")) {
			if err := syncPath(root, filepath.Join(directory, "content")); err != nil {
				return err
			}
		}
		for _, path := range []string{filepath.Join(directory, "receipt.json"), directory, ".uploads"} {
			if err := syncPath(root, path); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return UploadState{}, err
	}
	return s.UploadState(ctx, id, scope)
}

func (s *Store) UploadState(ctx context.Context, id, scope string) (UploadState, error) {
	var state UploadState
	err := s.lockedContext(ctx, func(root *os.Root) error {
		receipt, err := readUpload(root, id)
		if err != nil {
			return err
		}
		if receipt.Scope != scope {
			return fs.ErrNotExist
		}
		state.Upload = receipt
		state.Status = "uploaded"
		directory, _ := uploadDirectory(id)
		data, err := readFile(root, filepath.Join(directory, "attached.json"))
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		var attached FileReceipt
		if err := json.Unmarshal(data, &attached); err != nil {
			return err
		}
		state.Status = "attached"
		state.RecordID = attached.ID
		state.AttachedFilename = attached.Filename
		return nil
	})
	return state, err
}
