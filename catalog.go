package songya

import (
	"bytes"
	"context"
	"encoding/gob"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gofrs/flock"
	"go.yaml.in/yaml/v3"
	"time"
)

const indexFormat = 12

// Diagnostic identifies a recoverable problem without hiding healthy records.
type Diagnostic struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// Record is the derived search/graph view. Source Markdown is authoritative.
type Record struct {
	User        string            `json:"user,omitempty"`
	ID          string            `json:"id"`
	Source      string            `json:"source"`
	Kind        Destination       `json:"kind"`
	Revision    string            `json:"-"`
	UpdatedAt   string            `json:"updated_at,omitempty"`
	Pending     bool              `json:"pending,omitempty"`
	Unconfirmed bool              `json:"unconfirmed,omitempty"`
	Title       string            `json:"title"`
	Name        string            `json:"name,omitempty"`
	Aliases     []string          `json:"aliases,omitempty"`
	Description string            `json:"description,omitempty"`
	Type        string            `json:"type,omitempty"`
	Types       map[string]string `json:"types,omitempty"`
	Relations   []Relation        `json:"relations,omitempty"`
	Redirects   []string          `json:"redirects,omitempty"`
	Time        RecordTime        `json:"time"`
	Body        string            `json:"body"`
	Context     string            `json:"context,omitempty"`
	Diagnostics []string          `json:"diagnostics,omitempty"`
}
type Catalog struct {
	Records     []Record     `json:"records"`
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
}

func nodeStrings(nodeName string, d *Document) []string {
	return stringsFromNode(metadataField(&d.Frontmatter, nodeName))
}
func stringsFromNode(node *yaml.Node) []string {
	var values []string
	seen := map[*yaml.Node]bool{}
	var visit func(*yaml.Node)
	visit = func(n *yaml.Node) {
		n = dereference(n)
		if n == nil || seen[n] {
			return
		}
		seen[n] = true
		if n.Kind == yaml.SequenceNode {
			for _, child := range n.Content {
				visit(child)
			}
		} else if n.Kind == yaml.ScalarNode && n.ShortTag() != "!!null" {
			var value string
			if n.Decode(&value) == nil && value != "" {
				values = append(values, value)
			}
		}
	}
	visit(node)
	return values
}

func recordFromDocument(source string, d *Document, kind Destination) Record {
	view := *d
	view.Kind = kind
	d = &view
	metadata := d.Metadata()
	r := Record{ID: d.ID, Source: source, Kind: kind, Revision: d.Revision, Pending: d.Pending, Unconfirmed: d.Unconfirmed, Title: metadata.Title, Body: d.Body, Relations: metadata.Relations, Diagnostics: d.Diagnostics()}
	if !metadata.UpdatedAt.IsZero() {
		r.UpdatedAt = metadata.UpdatedAt.UTC().Format(time.RFC3339Nano)
	}
	if userOwned(kind) {
		r.User = metadata.User
	}
	r.Name = strings.Join(nodeStrings("name", d), " ")
	if r.Title == "" {
		r.Title = r.Name
	}
	r.Context = recallContext(d)
	r.Aliases = nodeStrings("aliases", d)
	r.Description = strings.Join(nodeStrings("description", d), " ")
	if kind == Entities || kind == Topics {
		r.Description = d.Body
	}
	r.Types = documentTypes(d)
	r.Type = r.Types[r.User]
	r.Redirects = nodeStrings("redirects", d)
	r.Time = recordTime(metadataField(&d.Frontmatter, "time"), kind)
	if r.Title == "" {
		r.Title = d.ID
		if r.Title == "" {
			r.Title = filepath.Base(source)
		}
	}
	return r
}

// Scan refreshes byte revisions on every call. Cached parsing never masks direct
// edits, and a damaged cache is rebuilt from the readable source files.
func (s *Store) Scan(ctx context.Context) (Catalog, error) { return s.scan(ctx, false) }

// Rebuild ignores cached parsing and derives a fresh catalogue from source data.
func (s *Store) Rebuild(ctx context.Context) (Catalog, error) { return s.scan(ctx, true) }

func (s *Store) scan(ctx context.Context, rebuild bool) (Catalog, error) {
	if s == nil {
		return Catalog{}, errors.New("store is nil")
	}
	root, err := os.OpenRoot(s.path)
	if err != nil {
		return Catalog{}, err
	}
	defer root.Close()
	var coordination []Diagnostic
	if lockErr := regularFile(root, ".lock"); lockErr == nil {
		lock := flock.New(filepath.Join(s.path, ".lock"))
		defer lock.Close()
		acquired, err := lock.TryRLockContext(ctx, 10*time.Millisecond)
		if err != nil {
			return Catalog{}, err
		}
		if !acquired {
			return Catalog{}, ctx.Err()
		}
	} else {
		coordination = append(coordination, Diagnostic{".lock", "scan could not coordinate with writers: " + lockErr.Error()})
	}
	return s.scanRoot(ctx, root, rebuild, coordination)
}

// scanRoot shares the caller's lock, including an exclusive writer lock.
func (s *Store) scanRoot(ctx context.Context, root *os.Root, rebuild bool, coordination []Diagnostic) (Catalog, error) {
	cached := map[string]Record{}
	cacheName := filepath.Join(".index", "catalog.json")
	var envelope struct {
		Format   int    `json:"format"`
		Checksum string `json:"checksum"`
		Records  []byte `json:"records"`
	}
	if !rebuild {
		if data, err := readFile(root, cacheName); err == nil && json.Unmarshal(data, &envelope) == nil && envelope.Format == indexFormat && dataRevision(envelope.Records) == envelope.Checksum {
			var records []Record
			if gob.NewDecoder(bytes.NewReader(envelope.Records)).Decode(&records) == nil {
				for _, r := range records {
					cached[r.Source] = r
				}
			}
		}
	}
	result := Catalog{Records: []Record{}, Diagnostics: coordination}
	for _, destination := range destinations {
		directory := string(destination)
		entries, err := fs.ReadDir(root.FS(), directory)
		if err != nil {
			result.Diagnostics = append(result.Diagnostics, Diagnostic{directory, err.Error()})
			continue
		}
		prefix, _ := destination.prefix()
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			id := entry.Name()
			if !entry.IsDir() || strings.HasPrefix(id, ".") {
				if strings.HasSuffix(id, ".md") {
					result.Diagnostics = append(result.Diagnostics, Diagnostic{filepath.Join(directory, id), "Flat record requires the explicit folder conversion; retained unchanged"})
				}
				continue
			}
			if ValidID(id) && id[:2] == prefix {
				if err := recordWritable(root, id); err != nil {
					continue
				}
			}
			source := filepath.Join(directory, id, "record.md")
			if _, err := readFile(root, source); err != nil {
				// Uncommitted batch creations stay hidden until the batch commits.
				if _, claimErr := root.Lstat(filepath.Join(pendingName(source), "batch")); claimErr == nil {
					continue
				}
				pendingID := id
				if !ValidID(id) || id[:2] != prefix {
					pendingID = ""
				}
				doc, readErr := readCopy(root, pendingID, source)
				if readErr != nil {
					result.Diagnostics = append(result.Diagnostics, Diagnostic{source, readErr.Error()})
					continue
				}
				result.Records = append(result.Records, recordFromDocument(source, doc, destination))
				continue
			}
			data, err := readFile(root, source)
			data, err = batchView(root, source, data, err)
			if err != nil {
				result.Diagnostics = append(result.Diagnostics, Diagnostic{source, err.Error()})
				continue
			}
			revision := dataRevision(data)
			r, ok := cached[source]
			if !ok || r.Revision != revision {
				d := Parse(data)
				d.Revision = revision
				id := entry.Name()
				if ValidID(id) && id[:2] == prefix {
					d.ID = id
				}
				r = recordFromDocument(source, d, destination)
			}
			if issue := batchReadIssue(root, source); issue != nil {
				r.Unconfirmed = true
				r.Revision = ""
				r.Diagnostics = append(r.Diagnostics, issue.Error())
				result.Diagnostics = append(result.Diagnostics, Diagnostic{source, issue.Error()})
			}
			if r.ID == "" {
				result.Diagnostics = append(result.Diagnostics, Diagnostic{source, "record has no valid filename ID"})
			}
			result.Records = append(result.Records, r)
		}
	}
	sort.Slice(result.Records, func(i, j int) bool { return result.Records[i].Source < result.Records[j].Source })
	var encoded bytes.Buffer
	_ = gob.NewEncoder(&encoded).Encode(result.Records)
	records := encoded.Bytes()
	envelope.Format = indexFormat
	envelope.Checksum = dataRevision(records)
	envelope.Records = records
	payload, _ := json.Marshal(envelope)
	if existing, err := readFile(root, cacheName); err != nil || string(existing) != string(payload) {
		err := ensureDirectory(root, ".index")
		if err == nil {
			temporary := cacheName + "." + dataRevision(payload) + ".tmp"
			err = root.WriteFile(temporary, payload, 0600)
			if err == nil {
				err = root.Rename(temporary, cacheName)
			}
		}
		s.note(cacheName, err)
		if err != nil {
			result.Diagnostics = append(result.Diagnostics, Diagnostic{cacheName, "derived index unavailable: " + err.Error()})
		}
	} else {
		s.note(cacheName, nil)
	}
	return result, nil
}

func words(value string) []string {
	return strings.FieldsFunc(strings.ToLower(value), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}
func clip(value string, maximum int) string {
	if len(value) <= maximum {
		return value
	}
	for maximum > 0 && !utf8.RuneStart(value[maximum]) {
		maximum--
	}
	return strings.Clone(value[:maximum])
}

type Query struct {
	Mode           string      `json:"mode,omitempty"`
	User           string      `json:"user,omitempty"`
	Text           string      `json:"text,omitempty"`
	Kind           Destination `json:"kind,omitempty"`
	RelatedTo      string      `json:"related_to,omitempty"`
	RelationType   string      `json:"relation_type,omitempty"`
	IncludeRetired bool        `json:"include_retired,omitempty"`
	Offset         int         `json:"offset,omitempty"`
	Limit          int         `json:"limit,omitempty"`
}
type Match struct {
	Type             string      `json:"type,omitempty"`
	User             string      `json:"user,omitempty"`
	ID               string      `json:"id,omitempty"`
	Source           string      `json:"source"`
	Kind             Destination `json:"kind"`
	Revision         string      `json:"-"`
	Pending          bool        `json:"pending,omitempty"`
	Unconfirmed      bool        `json:"unconfirmed,omitempty"`
	Title            string      `json:"title"`
	Aliases          []string    `json:"aliases,omitempty"`
	Excerpt          string      `json:"excerpt"`
	Redirects        []string    `json:"redirects,omitempty"`
	Score            float64     `json:"score"`
	Reasons          []string    `json:"reasons,omitempty"`
	Diagnostics      []string    `json:"diagnostics,omitempty"`
	DetailsTruncated bool        `json:"details_truncated,omitempty"`
}
type SearchResult struct {
	Mode        string       `json:"mode"`
	Incomplete  bool         `json:"incomplete,omitempty"`
	Matches     []Match      `json:"matches"`
	Next        int          `json:"next,omitempty"`
	Truncated   bool         `json:"truncated"`
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
}

func pageRange(offset, limit, length int) (int, int) {
	if offset < 0 {
		offset = 0
	}
	if offset > length {
		offset = length
	}
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}
	end := offset + limit
	if end > length {
		end = length
	}
	return offset, end
}
func boundedDiagnostics(diagnostics []Diagnostic) []Diagnostic {
	if len(diagnostics) > 20 {
		diagnostics = append(append([]Diagnostic{}, diagnostics[:19]...), Diagnostic{"diagnostics", "additional diagnostics omitted; inspect affected records or refresh in smaller sections"})
	}
	for i := range diagnostics {
		diagnostics[i].Path = clip(diagnostics[i].Path, 256)
		diagnostics[i].Message = clip(diagnostics[i].Message, 512)
	}
	return diagnostics
}

func rankMatches(c Catalog, q Query) []Match {
	c = userCatalog(c, q.User)
	matches := []Match{}
	normalizedText := " " + strings.Join(words(q.Text), " ") + " "
	tokens := words(q.Text)
	if len(tokens) > 64 {
		tokens = tokens[:64]
	}
	for _, r := range c.Records {
		if !q.IncludeRetired && len(r.Redirects) > 0 && !r.Unconfirmed || q.Kind != "" && r.Kind != q.Kind {
			continue
		}
		if q.RelatedTo != "" {
			found := false
			for _, edge := range r.Relations {
				if edge.Target == q.RelatedTo && (q.RelationType == "" || edge.Type == q.RelationType) {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		score := 0
		reasons := []string{}
		names := append([]string{r.Title}, r.Aliases...)
		for _, name := range names {
			lower := strings.Join(words(name), " ")
			if lower != "" && strings.Contains(normalizedText, " "+lower+" ") {
				score += 20
				reasons = append(reasons, "name or alias: "+clip(name, 96))
			}
		}
		title := strings.ToLower(strings.Join(names, " "))
		description := strings.ToLower(r.Description)
		body := strings.ToLower(r.Body + " " + dateValue(r.Time.Date) + " " + dateValue(r.Time.Earliest) + " " + dateValue(r.Time.Latest))
		for _, token := range tokens {
			if strings.Contains(title, token) {
				score += 5
			}
			if strings.Contains(description, token) {
				score += 3
			}
			if strings.Contains(body, token) {
				score++
			}
		}
		if len(tokens) > 0 && score == 0 {
			continue
		}
		excerpt := r.Description
		if excerpt == "" {
			excerpt = r.Body
		}
		if len(reasons) > 4 {
			reasons = reasons[:4]
		}
		aliases := []string{}
		for _, alias := range r.Aliases {
			if len(aliases) == 8 {
				break
			}
			aliases = append(aliases, clip(alias, 96))
		}
		redirects := []string{}
		detailsTruncated := len(r.Redirects) > 8 || len(r.Aliases) > 8 || len(r.Title) > 160 || len(r.Diagnostics) > 4
		for _, redirect := range r.Redirects[:min(8, len(r.Redirects))] {
			value := clip(redirect, 128)
			detailsTruncated = detailsTruncated || value != redirect
			redirects = append(redirects, value)
		}
		warnings := []string{}
		for _, warning := range r.Diagnostics[:min(4, len(r.Diagnostics))] {
			warnings = append(warnings, clip(warning, 512))
		}
		matches = append(matches, Match{Type: r.Type, User: clip(r.User, 160), ID: r.ID, Source: r.Source, Kind: r.Kind, Revision: r.Revision, Pending: r.Pending, Unconfirmed: r.Unconfirmed, Title: clip(r.Title, 160), Aliases: aliases, Excerpt: clip(excerpt, 512), Redirects: redirects, Score: float64(score), Reasons: reasons, Diagnostics: warnings, DetailsTruncated: detailsTruncated || len(r.User) > 160})
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Score != matches[j].Score {
			return matches[i].Score > matches[j].Score
		}
		return matches[i].Source < matches[j].Source
	})
	return matches
}

func searchCatalog(c Catalog, q Query) SearchResult {
	matches := rankMatches(c, q)
	start, end := pageRange(q.Offset, q.Limit, len(matches))
	result := SearchResult{Mode: "lexical", Matches: append([]Match{}, matches[start:end]...), Truncated: end < len(matches), Diagnostics: boundedDiagnostics(c.Diagnostics)}
	if result.Truncated {
		result.Next = end
	}
	return result
}
func (s *Store) Search(ctx context.Context, q Query) (SearchResult, error) {
	catalog, err := s.Scan(ctx)
	if err != nil {
		return SearchResult{}, err
	}
	if strings.EqualFold(strings.TrimSpace(q.Mode), "semantic") && strings.TrimSpace(q.Text) != "" {
		return s.semanticSearch(ctx, catalog, q)
	}
	return searchCatalog(catalog, q), nil
}

// ReadSource exposes only Markdown sources in the four record directories. This
// also keeps records with damaged filenames inspectable without inventing IDs.
func (s *Store) ReadSource(source string) ([]byte, error) {
	if s == nil {
		return nil, errors.New("store is nil")
	}
	parts := strings.Split(filepath.ToSlash(source), "/")
	if len(parts) != 3 || parts[1] == "" || strings.HasPrefix(parts[1], ".") || parts[2] != "record.md" {
		return nil, fmt.Errorf("invalid record source %q", source)
	}
	_, kindErr := Destination(parts[0]).prefix()
	if kindErr != nil || filepath.ToSlash(filepath.Clean(source)) != source {
		return nil, fmt.Errorf("invalid record source %q", source)
	}
	root, err := os.OpenRoot(s.path)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	data, err := readRecordFile(root, source)
	if err == nil {
		return data, nil
	}
	if _, claimErr := root.Lstat(filepath.Join(pendingName(source), "batch")); errors.Is(claimErr, fs.ErrNotExist) {
		if staged, _, base, stageErr := stagedData(root, source); stageErr == nil && base == "" {
			return staged, nil
		}
	}
	return data, err
}

// ForUsers selects explicit owners and shared identities. An empty selection
// includes only shared records; all is the explicit unrestricted selector.
func (c Catalog) ForUsers(owners []string) Catalog {
	allowed := map[string]bool{}
	for _, owner := range owners {
		allowed[strings.TrimSpace(owner)] = true
	}
	selected := Catalog{Diagnostics: c.Diagnostics}
	for _, r := range c.Records {
		if !userOwned(r.Kind) || allowed[r.User] || allowed["all"] {
			if len(owners) == 1 {
				r.Type = r.Types[strings.TrimSpace(owners[0])]
			}
			selected.Records = append(selected.Records, r)
		}
	}
	return selected
}

// Select applies the engine's lexical ranking/filters without another scan.
// Applications can page/project the returned records before crossing a boundary.
func (c Catalog) Select(q Query) []Record {
	bySource := map[string]Record{}
	for _, r := range c.Records {
		bySource[r.Source] = r
	}
	records := []Record{}
	for _, match := range rankMatches(c, q) {
		records = append(records, bySource[match.Source])
	}
	return records
}
