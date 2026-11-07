package songya

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// RecallRequest accepts either one message or a batch sharing an ownership scope.
// Policies are deliberately absent: the store owns them.
type RecallRequest struct {
	Message  string   `json:"message,omitempty"`
	Messages []string `json:"messages,omitempty"`
	Owners   []string `json:"owners"`
}
type RecallHop struct {
	From string `json:"from"`
	To   string `json:"to"`
	Type string `json:"type"`
}
type Recollection struct {
	ID               string      `json:"id"`
	Kind             Destination `json:"kind"`
	User             string      `json:"user,omitempty"`
	Title            string      `json:"title"`
	Time             RecordTime  `json:"time"`
	Content          string      `json:"content"`
	Reason           string      `json:"reason"`
	Score            *float64    `json:"score,omitempty"`
	OriginID         string      `json:"origin_id,omitempty"`
	Depth            int         `json:"depth"`
	Path             []RecallHop `json:"path,omitempty"`
	ContentTruncated bool        `json:"content_truncated,omitempty"`
}
type SemanticRecallResult struct {
	Mode        string         `json:"mode"`
	Status      string         `json:"status"`
	Items       []Recollection `json:"items"`
	Incomplete  bool           `json:"incomplete"`
	Truncated   bool           `json:"truncated"`
	Diagnostics []Diagnostic   `json:"diagnostics,omitempty"`
}
type RecallResponse struct {
	Results []SemanticRecallResult `json:"results"`
	Policy  RecallPolicy           `json:"policy"`
}

func (r RecallRequest) Validate() error {
	if (strings.TrimSpace(r.Message) != "") == (len(r.Messages) > 0) {
		return fmt.Errorf("provide message or messages, not both")
	}
	if len(r.Owners) == 0 {
		return fmt.Errorf("provide a nonempty owners scope; use [\"all\"] explicitly for every collection")
	}
	for _, owner := range r.Owners {
		if strings.TrimSpace(owner) == "" {
			return fmt.Errorf("owners must not contain an empty user")
		}
	}
	for _, message := range r.Messages {
		if strings.TrimSpace(message) == "" {
			return fmt.Errorf("messages must not contain empty text")
		}
	}
	return nil
}
func cosine(a, b []float32) float64 {
	if len(a) != len(b) {
		return -1
	}
	var score float64
	for i, n := range a {
		score += float64(n) * float64(b[i])
	}
	return min(1, score)
}
func chunkScore(a, b []embeddedChunk) float64 {
	score := -1.0
	for _, x := range a {
		for _, y := range b {
			score = max(score, cosine(x.Vector, y.Vector))
		}
	}
	return score
}
func hasKind(kinds []Destination, kind Destination) bool {
	for _, k := range kinds {
		if k == kind {
			return true
		}
	}
	return false
}
func hasRelation(types []string, kind string) bool {
	for _, k := range types {
		if k == kind {
			return true
		}
	}
	return false
}

// Retrieve is the authoritative policy, ranking and traversal implementation.
// A batch scans and embeds each record once. Missing vectors never gain lexical scores.
func (s *Store) Retrieve(ctx context.Context, request RecallRequest) (RecallResponse, error) {
	config, issue := s.Configuration()
	return s.RetrieveWithConfiguration(ctx, request, config, issue)
}

// RetrieveWithConfiguration captures a turn's effective configuration for durable
// recovery. HTTP callers cannot override it through request fields.
func (s *Store) RetrieveWithConfiguration(ctx context.Context, request RecallRequest, config Configuration, configIssue error) (RecallResponse, error) {
	response := RecallResponse{Results: []SemanticRecallResult{}, Policy: config.Recall}
	if err := request.Validate(); err != nil {
		return response, err
	}
	messages := request.Messages
	if strings.TrimSpace(request.Message) != "" {
		messages = []string{request.Message}
	}
	c, err := s.Scan(ctx)
	if err != nil {
		return response, err
	}
	allRecords := catalogMap(c)
	c = c.ForUsers(request.Owners)
	diagnostics := append([]Diagnostic{}, c.Diagnostics...)
	if configIssue != nil {
		diagnostics = append(diagnostics, Diagnostic{"config.json", configIssue.Error()})
	}
	// Redirects may resolve through retired records but never through excluded owners.
	scoped := catalogMap(c)
	active := Catalog{}
	for _, r := range c.Records {
		if r.ID != "" && (len(r.Redirects) == 0 || r.Unconfirmed) {
			active.Records = append(active.Records, r)
		}
	}
	records := catalogMap(active)
	if config.Recall.DirectLimit == 0 || len(config.Recall.SearchKinds) == 0 {
		status := "ready"
		if len(diagnostics) > 0 {
			status = "partial"
		}
		for range messages {
			response.Results = append(response.Results, SemanticRecallResult{Mode: "semantic", Status: status, Items: []Recollection{}, Incomplete: len(diagnostics) > 0, Diagnostics: boundedDiagnostics(diagnostics)})
		}
		return response, nil
	}
	e, err := s.embeddingSessionWithConfiguration(config.Embeddings)
	if err != nil {
		return response, err
	}
	defer e.root.Close()
	queries := make([][]embeddedChunk, len(messages))
	queryIssues := make([]error, len(messages))
	for i, message := range messages {
		queries[i], queryIssues[i] = e.vectors(ctx, message, true)
	}
	vectors := map[string][]embeddedChunk{}
	for _, r := range active.Records {
		if ctx.Err() != nil {
			diagnostics = append(diagnostics, Diagnostic{"recall", "retrieval interrupted; available vectors retained"})
			break
		}
		if !hasKind(config.Recall.SearchKinds, r.Kind) || config.Recall.DirectLimit == 0 {
			continue
		}
		text := embeddingText(r)
		if strings.TrimSpace(text) == "" {
			continue
		}
		vectors[r.ID], err = e.vectors(ctx, text, true)
		if err != nil {
			diagnostics = append(diagnostics, Diagnostic{r.Source, err.Error()})
		}
	}
	if e.cacheIssue != nil {
		diagnostics = append(diagnostics, Diagnostic{".index/embeddings", "derived cache unavailable; available vectors remain usable"})
	}
	type neighbor struct{ id, kind string }
	adjacency := map[string][]neighbor{}
	for _, edge := range graphEdges(scoped) {
		if !hasRelation(config.Recall.RelationTypes, edge.Type) {
			continue
		}
		if records[edge.Source].ID == "" {
			continue
		}
		if !edge.Resolved || records[edge.Target].ID == "" {
			if scoped[edge.Target].ID != "" || allRecords[edge.Target].ID == "" {
				diagnostics = append(diagnostics, Diagnostic{edge.Source, "relationship target unavailable or ambiguous; not traversed"})
			}
			continue
		}
		adjacency[edge.Source] = append(adjacency[edge.Source], neighbor{edge.Target, edge.Type})
		adjacency[edge.Target] = append(adjacency[edge.Target], neighbor{edge.Source, edge.ReverseType})
	}
	for id := range adjacency {
		sort.Slice(adjacency[id], func(i, j int) bool {
			a, b := adjacency[id][i], adjacency[id][j]
			if a.id != b.id {
				return a.id < b.id
			}
			return a.kind < b.kind
		})
	}
	for i := range messages {
		result := SemanticRecallResult{Mode: "semantic", Status: "ready", Items: []Recollection{}, Diagnostics: append([]Diagnostic{}, diagnostics...)}
		if queryIssues[i] != nil {
			result.Diagnostics = append(result.Diagnostics, Diagnostic{"query", queryIssues[i].Error()})
		}
		if len(queries[i]) == 0 {
			result.Incomplete = true
			result.Status = "unavailable"
			result.Diagnostics = boundedDiagnostics(result.Diagnostics)
			response.Results = append(response.Results, result)
			continue
		}
		seeds := []Recollection{}
		for id, v := range vectors {
			if len(v) == 0 {
				continue
			}
			score := chunkScore(queries[i], v)
			if score < config.Recall.MinScore {
				continue
			}
			item := recallItem(records[id])
			item.Reason = "semantic"
			item.Score = &score
			seeds = append(seeds, item)
		}
		sort.Slice(seeds, func(i, j int) bool {
			if *seeds[i].Score != *seeds[j].Score {
				return *seeds[i].Score > *seeds[j].Score
			}
			return seeds[i].ID < seeds[j].ID
		})
		if len(seeds) > config.Recall.DirectLimit {
			result.Truncated = true
			seeds = seeds[:config.Recall.DirectLimit]
		}
		// Multi-source BFS: shortest paths first; ties use semantic seed rank, then IDs.
		type visit struct {
			id, origin string
			path       []RecallHop
		}
		queue := []visit{}
		seen := map[string]bool{}
		for _, seed := range seeds {
			seen[seed.ID] = true
			queue = append(queue, visit{id: seed.ID, origin: seed.ID})
			if hasKind(config.Recall.ReturnKinds, seed.Kind) {
				result.Items = append(result.Items, seed)
			}
		}
		connected := 0
		if config.Recall.ConnectionDepth > 0 && config.Recall.ConnectedLimit > 0 {
			for head := 0; head < len(queue); head++ {
				if ctx.Err() != nil {
					result.Diagnostics = append(result.Diagnostics, Diagnostic{"connections", "traversal interrupted; available results retained"})
					break
				}
				current := queue[head]
				if len(current.path) >= config.Recall.ConnectionDepth {
					continue
				}
				for _, next := range adjacency[current.id] {
					if seen[next.id] {
						continue
					}
					seen[next.id] = true
					path := append(append([]RecallHop{}, current.path...), RecallHop{From: current.id, To: next.id, Type: next.kind})
					queue = append(queue, visit{id: next.id, origin: current.origin, path: path})
					r := records[next.id]
					if !hasKind(config.Recall.ReturnKinds, r.Kind) {
						continue
					}
					if connected >= config.Recall.ConnectedLimit {
						result.Truncated = true
						continue
					}
					item := recallItem(r)
					item.Reason = "connection"
					item.OriginID = current.origin
					item.Depth = len(path)
					item.Path = path
					result.Items = append(result.Items, item)
					connected++
				}
			}
		}
		result.Incomplete = len(result.Diagnostics) > 0
		if result.Incomplete {
			result.Status = "partial"
		}
		if len(vectors) > 0 {
			usable := false
			for _, v := range vectors {
				usable = usable || len(v) > 0
			}
			if !usable {
				result.Status = "unavailable"
				result.Incomplete = true
			}
		}
		result.Diagnostics = boundedDiagnostics(result.Diagnostics)
		response.Results = append(response.Results, result)
	}
	return response, ctx.Err()
}

func recallItem(r Record) Recollection {
	// Each record supplies its own prose. Related story introductions/events are
	// explicit bounded graph results, never hidden expansion outside the policy.
	label, _ := json.Marshal(map[string]any{"id": r.ID, "kind": r.Kind, "user": r.User, "title": r.Title, "name": r.Name, "aliases": r.Aliases, "time": r.Time, "relations": r.Relations, "pending": r.Pending, "unconfirmed": r.Unconfirmed, "diagnostics": r.Diagnostics})
	metadataTruncated := len(label) > 8192
	if metadataTruncated {
		label, _ = json.Marshal(map[string]any{"id": r.ID, "kind": r.Kind, "user": clip(r.User, 512), "title": clip(r.Title, 512), "time": r.Time, "metadata_truncated": true})
	}
	body := r.Body
	if r.Context != "" {
		body += "\n" + r.Context
	}
	clipped := clip(body, 8192)
	return Recollection{ID: r.ID, Kind: r.Kind, User: r.User, Title: clip(r.Title, 512), Time: r.Time, Content: string(label) + "\n" + clipped, ContentTruncated: body != clipped || metadataTruncated || len(r.Title) > 512}
}
