package songya

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// File identity is the containing record ID and the actual filename. This view
// is always derived from the folder, never a second saved attachment list.
type Attachment struct {
	Filename  string `json:"filename"`
	Bytes     int64  `json:"bytes"`
	MediaType string `json:"media_type"`
	Preview   string `json:"preview"` // image, audio, video, text, or download
}
type Attachments struct {
	Files       []Attachment `json:"files"`
	Revision    string       `json:"revision"`
	Pending     bool         `json:"pending,omitempty"`
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
}

func attachmentName(name string) (string, error) {
	if name == "" || name == "." || name == ".." || !utf8.ValidString(name) || strings.ContainsAny(name, "/\\") || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return "", errors.New("Use a filename without folders, slashes, or control characters")
	}
	if strings.HasPrefix(name, ".") || strings.EqualFold(name, "record.md") {
		return "", errors.New("This filename is reserved for record text or internal state")
	}
	return name, nil
}
func uploadName(name string) (string, error) {
	if strings.EqualFold(name, "record.md") {
		name = strings.TrimSuffix(name, filepath.Ext(name)) + " (file)" + filepath.Ext(name)
	}
	if strings.HasPrefix(name, ".") && name != "." && name != ".." {
		name = "attachment-" + name
	}
	if _, err := attachmentName(name); err != nil {
		return "", err
	}
	return fitFilename(name, ""), nil
}

// Fit the filesystem's filename byte limit without cutting a UTF-8 rune.
// Keep the extension when it fits; collision suffixes reserve their own space.
func fitFilename(name, suffix string) string {
	extension := filepath.Ext(name)
	stem := strings.TrimSuffix(name, extension)
	available := 255 - len(extension) - len(suffix)
	if available < 1 {
		return filenamePrefix(name, 255-len(suffix)) + suffix
	}
	return filenamePrefix(stem, available) + suffix + extension
}
func filenamePrefix(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	for limit > 0 && !utf8.RuneStart(value[limit]) {
		limit--
	}
	return value[:limit]
}

// Refuse symlinked record directories as well as symlinked files. os.Root also
// confines every subsequent open/rename if an external editor changes a path.
func recordDirectory(root *os.Root, id string) (string, error) {
	name, err := recordName(id)
	if err != nil {
		return "", err
	}
	if err := recordWritable(root, id); err != nil {
		return "", err
	}
	for _, directory := range []string{string(kindOf(id)), filepath.Dir(name)} {
		info, err := root.Lstat(directory)
		if err != nil {
			return "", err
		}
		if !info.IsDir() {
			return "", fmt.Errorf("%s is not a record directory", directory)
		}
	}
	return filepath.Dir(name), nil
}
func openAttachment(root *os.Root, id, filename string) (*os.File, error) {
	name, err := attachmentName(filename)
	if err != nil {
		return nil, err
	}
	directory, err := recordDirectory(root, id)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(directory, name)
	if err := regularFile(root, path); err != nil {
		return nil, err
	}
	return root.Open(path)
}
func attachmentInfo(file *os.File, name string) (Attachment, error) {
	info, err := file.Stat()
	if err != nil {
		return Attachment{}, err
	}
	if !info.Mode().IsRegular() {
		return Attachment{}, errors.New("Not a regular file")
	}
	head := make([]byte, 512)
	n, err := file.ReadAt(head, 0)
	if err != nil && err != io.EOF {
		return Attachment{}, err
	}
	media := http.DetectContentType(head[:n])
	if media == "application/octet-stream" || media == "application/ogg" {
		if detected := map[string]string{".flac": "audio/flac", ".m4a": "audio/mp4", ".aac": "audio/aac", ".ogg": "audio/ogg", ".ogv": "video/ogg", ".webm": "video/webm"}[strings.ToLower(filepath.Ext(name))]; detected != "" {
			media = detected
		}
	}
	preview := "download"
	switch strings.Split(media, ";")[0] {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "image/avif":
		preview = "image"
	case "audio/mpeg", "audio/ogg", "audio/wave", "audio/wav", "audio/flac", "audio/aac", "audio/mp4", "audio/webm":
		preview = "audio"
	case "video/mp4", "video/webm", "video/ogg":
		preview = "video"
	case "text/plain":
		sample := head[:n]
		if n == len(head) && info.Size() > int64(n) {
			for trim := 0; trim < 3 && !utf8.Valid(sample); trim++ {
				sample = sample[:len(sample)-1]
			}
		}
		if utf8.Valid(sample) {
			preview = "text"
		}
	}
	return Attachment{name, info.Size(), media, preview}, nil
}
func (s *Store) Attachments(ctx context.Context, id string) (Attachments, error) {
	result := Attachments{Files: []Attachment{}}
	err := s.lockedContext(ctx, func(root *os.Root) error {
		directory, err := recordDirectory(root, id)
		if err != nil {
			return err
		}
		doc, err := readLockedCopy(root, id, filepath.Join(directory, "record.md"))
		if err != nil {
			return err
		}
		result.Revision, result.Pending = doc.Revision, doc.Pending || doc.Unconfirmed
		if _, err := root.Lstat(pendingName(filepath.Join(directory, "record.md"))); err == nil {
			result.Pending = true
		}
		entries, err := fs.ReadDir(root.FS(), directory)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.Name() == "record.md" || strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			file, err := openAttachment(root, id, entry.Name())
			if err == nil {
				var item Attachment
				item, err = attachmentInfo(file, entry.Name())
				err = errors.Join(err, file.Close())
				if err == nil {
					result.Files = append(result.Files, item)
				}
			}
			if err != nil {
				result.Diagnostics = append(result.Diagnostics, Diagnostic{entry.Name(), err.Error()})
			}
		}
		return nil
	})
	return result, err
}

// OpenAttachment returns a streaming file, not bytes buffered in the engine.
// Its inode remains usable if a concurrent change later replaces/removes it.
func (s *Store) OpenAttachment(ctx context.Context, id, filename string) (*os.File, Attachment, error) {
	var file *os.File
	var info Attachment
	err := s.lockedContext(ctx, func(root *os.Root) error {
		var err error
		file, err = openAttachment(root, id, filename)
		if err != nil {
			return err
		}
		info, err = attachmentInfo(file, filename)
		if err != nil {
			_ = file.Close()
			file = nil
		}
		return err
	})
	return file, info, err
}

// ReadAttachmentText never treats arbitrary binary/HTML as readable text. Byte
// paging is bounded; models must read returned text rather than invent contents.
func (s *Store) ReadAttachmentText(ctx context.Context, id, filename string, offset, limit int64) (string, int64, error) {
	file, info, err := s.OpenAttachment(ctx, id, filename)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	if info.Preview != "text" {
		return "", 0, fmt.Errorf("%s is %s; only plain text can be read by this tool. The file is stored and downloadable", filename, info.MediaType)
	}
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 || limit > 16384 {
		limit = 16384
	}
	if offset >= info.Bytes {
		return "", 0, nil
	}
	data := make([]byte, min(limit+utf8.UTFMax, info.Bytes-offset))
	n, err := file.ReadAt(data, offset)
	if err != nil && err != io.EOF {
		return "", 0, err
	}
	end := min(int(limit), n)
	for end > 0 && end < n && !utf8.RuneStart(data[end]) {
		end--
	}
	if end == 0 {
		_, end = utf8.DecodeRune(data[:n])
	}
	data = data[:end]
	n = len(data)
	if !utf8.Valid(data) {
		return "", 0, errors.New("This byte range is not valid UTF-8 text; download the original file")
	}

	next := offset + int64(n)
	if next >= info.Bytes {
		next = 0
	}
	return string(data), next, nil
}
func fileHash(root *os.Root, name string) (string, error) {
	if err := regularFile(root, name); err != nil {
		return "", err
	}
	file, err := root.Open(name)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}
