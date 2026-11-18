package songya

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// EmbeddingConfig uses an OpenAI-compatible embedding endpoint. Defaults match
// Clara's BGE-M3 service; dimensions are learned from the returned vectors.
type EmbeddingConfig struct {
	Model     string `json:"model" yaml:"model"`
	BaseURL   string `json:"base_url" yaml:"base_url"`
	APIKey    string `json:"api_key,omitempty" yaml:"api_key"`
	ChunkSize int    `json:"chunk_size,omitempty" yaml:"chunk_size"`
}

// Configuration fields decode independently; numeric strings are accepted.
func (c *EmbeddingConfig) UnmarshalYAML(node *yaml.Node) error {
	var issues []error
	fields := map[string]*string{"model": &c.Model, "base_url": &c.BaseURL, "api_key": &c.APIKey}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, value := node.Content[i].Value, node.Content[i+1]
		if target := fields[key]; target != nil {
			issues = append(issues, value.Decode(target))
		}
		if key == "chunk_size" {
			var text string
			err := value.Decode(&text)
			if err == nil {
				var size int
				size, err = strconv.Atoi(strings.TrimSpace(text))
				if err == nil {
					c.ChunkSize = size
				}
			}
			issues = append(issues, err)
		}
	}
	return errors.Join(issues...)
}

func (c EmbeddingConfig) defaults() EmbeddingConfig {
	c.Model = strings.TrimSpace(c.Model)
	c.BaseURL = strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if c.Model == "" {
		c.Model = "BAAI/bge-m3"
	}
	if c.BaseURL == "" {
		c.BaseURL = "http://127.0.0.1:18200/v1"
	}
	if c.ChunkSize <= 0 {
		c.ChunkSize = 2000
	}
	return c
}

type EmbeddingStatus struct {
	Model       string       `json:"model"`
	Records     int          `json:"records"`
	Indexed     int          `json:"indexed"`
	Chunks      int          `json:"chunks"`
	Dimensions  int          `json:"dimensions"`
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
	Incomplete  bool         `json:"incomplete"`
}

type embeddingSession struct {
	store      *Store
	root       *os.Root
	config     EmbeddingConfig
	online     bool
	dimensions int
	cacheIssue error
}

func (s *Store) embeddingSession() (*embeddingSession, error) {
	config, issue := s.Configuration()
	s.note("configuration", issue)
	return s.embeddingSessionWithConfiguration(config.Embeddings)
}
func (s *Store) embeddingSessionWithConfiguration(config EmbeddingConfig) (*embeddingSession, error) {
	root, err := os.OpenRoot(s.path)
	if err != nil {
		return nil, err
	}
	e := &embeddingSession{store: s, root: root, config: config, online: true}
	err = ensureDirectory(root, ".index")
	if err == nil {
		err = ensureDirectory(root, ".index/embeddings")
	}
	s.note("embedding cache", err)
	e.cacheIssue = err
	return e, nil
}

func embeddingText(r Record) string {
	switch r.Kind {
	case Entities, Topics:
		parts := append([]string{r.Name, r.Title}, r.Aliases...)
		parts = append(parts, r.Body)
		return strings.Join(parts, "\n")
	case Memories, Stories:
		return strings.Join([]string{r.Title, r.Body, dateValue(r.Time.Date), dateValue(r.Time.Earliest), dateValue(r.Time.Latest), r.Context}, "\n")
	}
	return ""
}

func embeddingChunks(text string, size int) []string {
	runes := []rune(text)
	chunks := []string{}
	overlap := size / 10
	for start := 0; start < len(runes); {
		end := min(start+size, len(runes))
		chunks = append(chunks, string(runes[start:end]))
		if end == len(runes) {
			break
		}
		start = end - overlap
	}
	return chunks
}

func normalizedVector(v []float64) []float32 {
	norm := 0.0
	for _, n := range v {
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return nil
		}
		norm = math.Hypot(norm, n)
	}
	if norm == 0 || math.IsInf(norm, 0) {
		return nil
	}
	out := make([]float32, len(v))
	for i, n := range v {
		out[i] = float32(n / norm)
	}
	return out
}

func validVector(v []float32, dimensions int) bool {
	if len(v) == 0 || dimensions > 0 && len(v) != dimensions {
		return false
	}
	norm := 0.0
	for _, n := range v {
		if math.IsNaN(float64(n)) || math.IsInf(float64(n), 0) {
			return false
		}
		norm += float64(n) * float64(n)
	}
	return norm > 0 && !math.IsInf(norm, 0)
}

func (e *embeddingSession) vector(ctx context.Context, text string, cache bool) ([]float32, error) {
	identity, _ := json.Marshal([]string{e.config.BaseURL, e.config.Model, text})
	name := filepath.Join(".index", "embeddings", dataRevision(identity)+".json")
	var cached struct {
		Vector   []float32 `json:"vector"`
		Checksum string    `json:"checksum"`
	}
	if cache && regularFile(e.root, name) == nil {
		if file, err := e.root.Open(name); err == nil {
			err = json.NewDecoder(io.LimitReader(file, 16<<20)).Decode(&cached)
			_ = file.Close()
			raw, _ := json.Marshal(cached.Vector)
			if err == nil && cached.Checksum == dataRevision(raw) && validVector(cached.Vector, e.dimensions) {
				e.dimensions = len(cached.Vector)
				return cached.Vector, nil
			}
		}
	}
	vector, err := e.request(ctx, text)
	if err != nil {
		return nil, err
	}
	if !cache {
		return vector, nil
	}
	cached.Vector = vector
	raw, _ := json.Marshal(vector)
	cached.Checksum = dataRevision(raw)
	data, _ := json.Marshal(cached)
	temporary := name + "." + rand.Text() + ".tmp"
	file, err := e.root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err == nil {
		_, err = file.Write(data)
		err = errors.Join(err, file.Close())
		if err == nil {
			err = e.root.Rename(temporary, name)
		}
		if err != nil {
			_ = e.root.Remove(temporary)
		}
	}
	if err != nil {
		e.cacheIssue = err
	}
	e.store.note("embedding cache", err)
	return vector, nil
}

func (e *embeddingSession) request(ctx context.Context, text string) ([]float32, error) {
	if !e.online {
		return nil, fmt.Errorf("embedding service unavailable; uncached content remains pending")
	}
	input, _ := json.Marshal(map[string]any{"model": e.config.Model, "input": text})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.config.BaseURL+"/embeddings", bytes.NewReader(input))
	if err != nil {
		e.online = false
		return nil, fmt.Errorf("invalid embedding endpoint URL")
	}
	req.Header.Set("Content-Type", "application/json")
	if e.config.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.config.APIKey)
	}
	response, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		e.online = false
		return nil, fmt.Errorf("embedding endpoint unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		if response.StatusCode >= 500 || response.StatusCode == 401 || response.StatusCode == 403 || response.StatusCode == 429 {
			e.online = false
		}
		return nil, fmt.Errorf("embedding endpoint returned HTTP %d", response.StatusCode)
	}
	var payload struct {
		Data []struct {
			Embedding []json.Number `json:"embedding"`
		} `json:"data"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 16<<20)).Decode(&payload) != nil || len(payload.Data) != 1 {
		return nil, fmt.Errorf("embedding response needs one vector per input")
	}
	numbers := make([]float64, len(payload.Data[0].Embedding))
	for i, number := range payload.Data[0].Embedding {
		var err error
		numbers[i], err = number.Float64()
		if err != nil {
			return nil, fmt.Errorf("embedding response contains an unusable number")
		}
	}
	vector := normalizedVector(numbers)
	if !validVector(vector, e.dimensions) {
		return nil, fmt.Errorf("embedding response has an unusable vector or inconsistent dimensions")
	}
	e.dimensions = len(vector)
	return vector, nil
}

type embeddedChunk struct {
	Text   string
	Vector []float32
}

func (e *embeddingSession) vectors(ctx context.Context, text string, cache bool) ([]embeddedChunk, error) {
	vectors := []embeddedChunk{}
	var issue error
	for _, chunk := range embeddingChunks(text, e.config.ChunkSize) {
		if err := ctx.Err(); err != nil {
			return vectors, err
		}
		vector, err := e.vector(ctx, chunk, cache)
		if err != nil {
			issue = err
			continue
		}
		vectors = append(vectors, embeddedChunk{chunk, vector})
	}
	return vectors, issue
}

// IndexEmbeddings refreshes missing or damaged derived vectors. Writes to
// memories never depend on the endpoint; changed text gets new cache keys.
func (s *Store) IndexEmbeddings(ctx context.Context) (EmbeddingStatus, error) {
	c, err := s.Scan(ctx)
	if err != nil {
		return EmbeddingStatus{}, err
	}
	e, err := s.embeddingSession()
	if err != nil {
		return EmbeddingStatus{}, err
	}
	defer e.root.Close()
	status := EmbeddingStatus{Model: e.config.Model, Diagnostics: c.Diagnostics}
	for _, r := range c.Records {
		if embeddingText(r) != "" && (len(r.Redirects) == 0 || r.Unconfirmed) {
			status.Records++
		}
	}
	for _, r := range c.Records {
		text := embeddingText(r)
		if text == "" || len(r.Redirects) > 0 && !r.Unconfirmed {
			continue
		}
		vectors, err := e.vectors(ctx, text, true)
		status.Chunks += len(vectors)
		if err == nil {
			status.Indexed++
		} else {
			status.Diagnostics = append(status.Diagnostics, Diagnostic{r.Source, err.Error()})
		}
		if ctx.Err() != nil {
			break
		}
	}
	status.Incomplete = status.Indexed < status.Records || len(c.Diagnostics) > 0 || e.cacheIssue != nil
	if e.cacheIssue != nil {
		status.Diagnostics = append(status.Diagnostics, Diagnostic{".index/embeddings", "derived cache unavailable; vectors remain usable for this request"})
	}
	status.Dimensions = e.dimensions
	status.Diagnostics = boundedDiagnostics(status.Diagnostics)
	return status, ctx.Err()
}

func (s *Store) semanticSearch(ctx context.Context, c Catalog, q Query) (SearchResult, error) {
	fallback := func(message string) (SearchResult, error) {
		result := searchCatalog(c, q)
		result.Diagnostics = boundedDiagnostics(append(result.Diagnostics, Diagnostic{"embeddings", message + "; lexical results returned"}))
		return result, ctx.Err()
	}

	candidates := []Match{}
	for _, match := range rankMatches(c, Query{Kind: q.Kind, RelatedTo: q.RelatedTo, RelationType: q.RelationType, IncludeRetired: q.IncludeRetired}) {
		candidates = append(candidates, match)
	}
	if len(candidates) == 0 {
		return SearchResult{Mode: "semantic", Matches: []Match{}, Incomplete: len(c.Diagnostics) > 0, Diagnostics: boundedDiagnostics(c.Diagnostics)}, ctx.Err()
	}
	e, err := s.embeddingSession()
	if err != nil {
		return fallback("embedding cache root unavailable")
	}
	defer e.root.Close()
	query, queryErr := e.vectors(ctx, q.Text, true)
	if len(query) == 0 {
		return fallback("query embedding unavailable")
	}
	records := map[string]Record{}
	for _, r := range c.Records {
		records[r.Source] = r
	}
	matches := []Match{}
	diagnostics := append([]Diagnostic{}, c.Diagnostics...)
	incomplete := 0
	if queryErr != nil {
		incomplete++
		diagnostics = append(diagnostics, Diagnostic{"embeddings", "part of the query awaits embeddings; results are partial"})
	}
	for _, match := range candidates {
		record := records[match.Source]
		text := embeddingText(record)
		if text == "" {
			continue
		}
		vectors, err := e.vectors(ctx, text, true)
		if err != nil {
			match.Diagnostics = append(match.Diagnostics, "Some content awaits embeddings; semantic results are partial")
			incomplete++
			diagnostics = append(diagnostics, Diagnostic{record.Source, err.Error()})
		}
		if ctx.Err() != nil {
			return SearchResult{}, ctx.Err()
		}
		if len(vectors) == 0 {
			continue
		}
		score := -1.0
		for _, a := range query {
			for _, b := range vectors {
				similarity := 0.0
				for i, n := range a.Vector {
					similarity += float64(n) * float64(b.Vector[i])
				}
				if similarity > score {
					match.Excerpt = clip(b.Text, 512)
				}
				score = max(score, min(1, similarity))
			}
		}
		match.Score = score
		match.Reasons = []string{"semantic similarity"}
		matches = append(matches, match)
	}
	if len(matches) == 0 {
		return fallback("no usable record embeddings")
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Score != matches[j].Score {
			return matches[i].Score > matches[j].Score
		}
		return matches[i].Source < matches[j].Source
	})
	start, end := pageRange(q.Offset, q.Limit, len(matches))
	if e.cacheIssue != nil {
		diagnostics = append(diagnostics, Diagnostic{".index/embeddings", "derived cache unavailable; vectors remain usable for this request"})
	}
	result := SearchResult{Mode: "semantic", Matches: append([]Match{}, matches[start:end]...), Truncated: end < len(matches), Incomplete: incomplete > 0 || len(c.Diagnostics) > 0, Diagnostics: boundedDiagnostics(diagnostics)}
	if result.Truncated {
		result.Next = end
	}
	return result, nil
}

// LoadEmbeddingConfig reads the same store configuration used by the MCP server.
// Readable fields are retained when a damaged field needs repair.
func LoadEmbeddingConfig(path string) (EmbeddingConfig, error) {
	config, err := LoadConfiguration(path)
	return config.Embeddings, err
}
