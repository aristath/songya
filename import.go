package songya

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Span struct {
	Start int `json:"start"`
	End   int `json:"end"`
}
type ImportPart struct {
	Start  int            `json:"start"`
	End    int            `json:"end"`
	Kind   Destination    `json:"kind,omitempty"`
	Fields map[string]any `json:"fields,omitempty"`
}
type ImportRequest struct {
	User     string       `json:"user,omitempty"`
	Path     string       `json:"path"`
	Revision string       `json:"-"`
	Parts    []ImportPart `json:"parts,omitempty"`
}
type ImportedPart struct {
	ID    string `json:"id"`
	Start int    `json:"start"`
	End   int    `json:"end"`
}
type ImportMapping struct {
	Source           string         `json:"source"`
	Revision         string         `json:"-"`
	Archive          string         `json:"archive"`
	BatchKey         string         `json:"batch_key"`
	Status           string         `json:"status"`
	Parts            []ImportedPart `json:"parts"`
	CoverageComplete bool           `json:"coverage_complete"`
	Uncovered        []Span         `json:"uncovered,omitempty"`
	Diagnostics      []Diagnostic   `json:"diagnostics,omitempty"`
}
type ImportCandidate struct {
	Path     string `json:"path"`
	Revision string `json:"-"`
	Bytes    int    `json:"bytes"`
	Excerpt  string `json:"excerpt"`
}
type ImportPreview struct {
	Files       []ImportCandidate `json:"files"`
	Next        int               `json:"next,omitempty"`
	Truncated   bool              `json:"truncated"`
	Diagnostics []Diagnostic      `json:"diagnostics,omitempty"`
}

func importIdentity(sourceRoot, source string) (string, string, error) {
	absolute, err := filepath.Abs(sourceRoot)
	if err != nil {
		return "", "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err == nil {
		absolute = resolved
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", "", err
	}
	// A missing physical source root may still identify an archived prior import.
	// Reading a new source remains confined and requires that root to exist.
	if source == "" || filepath.IsAbs(source) || (filepath.Clean(source) == ".." || strings.HasPrefix(filepath.Clean(source), ".."+string(filepath.Separator))) {
		return "", "", fmt.Errorf("source must be a relative path inside the import root")
	}
	return absolute, "import:" + dataRevision([]byte(absolute+"\x00"+filepath.ToSlash(filepath.Clean(source)))), nil
}
func (s *Store) ImportSource(sourceRoot, source string) ([]byte, string, error) {
	if s == nil {
		return nil, "", errors.New("store is nil")
	}
	absolute, key, err := importIdentity(sourceRoot, source)
	if err != nil {
		return nil, "", err
	}
	full, err := filepath.EvalSymlinks(filepath.Join(absolute, source))
	if err != nil {
		return nil, "", err
	}
	relative, err := filepath.Rel(s.path, full)
	if err != nil {
		return nil, "", err
	}
	if relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, "", errors.New("the active store cannot be imported into itself")
	}
	root, err := os.OpenRoot(absolute)
	if err != nil {
		return nil, "", err
	}
	defer root.Close()
	data, err := readFile(root, source)
	return data, key, err
}

// PreviewImports never interprets episode boundaries. The client reviews exact
// source byte ranges; omitted parts import the entire file verbatim.
func (s *Store) PreviewImports(ctx context.Context, sourceRoot string, offset, limit int) (ImportPreview, error) {
	if s == nil {
		return ImportPreview{}, errors.New("store is nil")
	}
	root, err := os.OpenRoot(sourceRoot)
	if err != nil {
		return ImportPreview{}, err
	}
	defer root.Close()
	paths := []string{}
	diagnostics := []Diagnostic{}
	err = fs.WalkDir(root.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			diagnostics = append(diagnostics, Diagnostic{path, walkErr.Error()})
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if path != "." && strings.HasPrefix(entry.Name(), ".") {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !entry.IsDir() && strings.HasSuffix(strings.ToLower(entry.Name()), ".md") {
			// A record folder contains one record, even when notes.md is attached.
			parent := filepath.Base(filepath.Dir(path))
			if ValidID(parent) && entry.Name() != "record.md" {
				return nil
			}
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return ImportPreview{}, err
	}
	sort.Strings(paths)
	start, end := pageRange(offset, limit, len(paths))
	result := ImportPreview{Files: []ImportCandidate{}, Truncated: end < len(paths)}
	if result.Truncated {
		result.Next = end
	}
	for _, path := range paths[start:end] {
		data, _, err := s.ImportSource(sourceRoot, path)
		if err != nil {
			diagnostics = append(diagnostics, Diagnostic{path, err.Error()})
			continue
		}
		result.Files = append(result.Files, ImportCandidate{Path: path, Revision: dataRevision(data), Bytes: len(data), Excerpt: clip(string(data), 1024)})
	}
	result.Diagnostics = boundedDiagnostics(diagnostics)
	return result, nil
}

func sourceCoverage(data []byte, parts []ImportPart) []Span {
	sorted := append([]ImportPart{}, parts...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Start < sorted[j].Start })
	gaps := []Span{}
	cursor := 0
	for _, part := range sorted {
		if part.Start > cursor && strings.TrimSpace(string(data[cursor:part.Start])) != "" {
			gaps = append(gaps, Span{cursor, part.Start})
		}
		if part.End > cursor {
			cursor = part.End
		}
	}
	if cursor < len(data) && strings.TrimSpace(string(data[cursor:])) != "" {
		gaps = append(gaps, Span{cursor, len(data)})
	}
	return gaps
}

// Import keeps a checksum-named verbatim original and uses one idempotent batch
// per source path. Repartitioning an already imported source requires explicit
// organization of its mapped records, rather than duplicate active imports.
func (s *Store) Import(ctx context.Context, sourceRoot string, request ImportRequest) (ImportMapping, error) {
	if s == nil {
		return ImportMapping{}, errors.New("store is nil")
	}
	if err := ctx.Err(); err != nil {
		return ImportMapping{}, err
	}
	absolute, key, err := importIdentity(sourceRoot, request.Path)
	if err != nil {
		return ImportMapping{}, err
	}
	root, err := os.OpenRoot(s.path)
	if err != nil {
		return ImportMapping{}, err
	}
	defer root.Close()
	data, _, sourceErr := s.ImportSource(absolute, request.Path)
	// A retry of accepted work uses its preserved original even if the source was
	// subsequently edited or disappeared. The same key still binds the proposal.
	if journal, journalErr := loadJournal(root, batchDirectory(key)); journalErr == nil {
		for _, entry := range journal.Entries {
			doc := Parse(entry.After)
			source := dereference(metadataField(&doc.Frontmatter, "import_source"))
			archive := strings.Join(stringsFromNode(metadataField(source, "archive")), "")
			if request.Revision == "" || archive == request.Revision {
				data, sourceErr = readFile(root, filepath.Join(".imports", archive+".md"))
				break
			}
		}
	}
	if sourceErr != nil {
		return ImportMapping{}, sourceErr
	}
	revision := dataRevision(data)
	if request.Revision != "" && revision != request.Revision {
		return ImportMapping{}, fmt.Errorf("import source changed: %w", ErrConflict)
	}
	if len(request.Parts) == 0 {
		request.Parts = []ImportPart{{Start: 0, End: len(data), Kind: Memories}}
	}
	changes := []Change{}
	for i, part := range request.Parts {
		if err := ctx.Err(); err != nil {
			return ImportMapping{}, err
		}
		if part.Start < 0 || part.End < part.Start || part.End > len(data) {
			return ImportMapping{}, fmt.Errorf("source range %d:%d is unavailable", part.Start, part.End)
		}
		fields := copyFields(part.Fields)
		fields["import_source"] = map[string]any{"path": request.Path, "archive": revision, "start": part.Start, "end": part.End}
		if _, ok := fields["title"]; !ok {
			fields["title"] = strings.TrimSuffix(filepath.Base(request.Path), filepath.Ext(request.Path))
		}
		kind := part.Kind
		if kind == "" {
			kind = Memories
		}
		changes = append(changes, Change{Ref: fmt.Sprintf("part_%d", i+1), Kind: kind, Fields: fields, Body: textValue(string(data[part.Start:part.End]))})
	}
	if err := ensureDirectory(root, ".imports"); err != nil {
		return ImportMapping{}, err
	}
	archive := filepath.Join(".imports", revision+".md")
	if err := syncNew(root, archive, data); err != nil {
		return ImportMapping{}, err
	}
	if err := syncPath(root, ".imports"); err != nil {
		return ImportMapping{}, err
	}
	receipt, err := s.ApplyBatch(ctx, BatchRequest{User: request.User, Key: key, Changes: changes})
	mapping := ImportMapping{Source: request.Path, Revision: revision, Archive: revision, BatchKey: key, Status: receipt.Status, Parts: []ImportedPart{}, Uncovered: sourceCoverage(data, request.Parts), Diagnostics: receipt.Diagnostics}
	mapping.CoverageComplete = len(mapping.Uncovered) == 0
	for i, part := range request.Parts {
		if id := receipt.Refs[fmt.Sprintf("part_%d", i+1)]; id != "" {
			mapping.Parts = append(mapping.Parts, ImportedPart{id, part.Start, part.End})
		}
	}
	if !mapping.CoverageComplete {
		mapping.Diagnostics = append(mapping.Diagnostics, Diagnostic{request.Path, "uncovered source ranges remain in the preserved original"})
	}
	if err != nil {
		return mapping, err
	}
	// The journal already preserves the mapping if this convenience receipt cannot
	// be saved; retrying reconstructs it with the same IDs.
	payload, _ := json.Marshal(mapping)
	mappingPath := filepath.Join(".imports", strings.TrimPrefix(key, "import:")+".json")
	if saveErr := syncNew(root, mappingPath, payload); saveErr != nil {
		mapping.Diagnostics = append(mapping.Diagnostics, Diagnostic{mappingPath, "mapping remains recoverable from batch journal: " + saveErr.Error()})
	} else if syncErr := syncPath(root, ".imports"); syncErr != nil {
		mapping.Diagnostics = append(mapping.Diagnostics, Diagnostic{mappingPath, syncErr.Error()})
	}
	return mapping, nil
}
