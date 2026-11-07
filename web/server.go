// Package web serves Songya's standalone editor; all persistence stays in the engine.
package web

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aristath/songya"
	"go.yaml.in/yaml/v3"
)

//go:embed assets/*
var assets embed.FS

type Application struct {
	http.Handler
	chat *chatApp
}

// Close stops chat workers, retaining accepted unfinished turns for recovery.
func (a *Application) Close() error { return a.chat.Close() }

// Handler serves the library and chat. Call Close when the application shuts down.
func Handler(store *songya.Store) *Application {
	mux := http.NewServeMux()
	chat := newChatApp(store)
	chat.register(mux)
	chat.registerRecall(mux)
	chat.registerTypes(mux)
	mux.HandleFunc("GET /api/records/events", chat.recordEventsHTTP)
	files, _ := fs.Sub(assets, "assets")
	page := template.Must(template.ParseFS(assets, "assets/index.html"))
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = page.Execute(w, nil)
	})
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.FileServerFS(files)))
	mux.HandleFunc("GET /api/graph", func(w http.ResponseWriter, r *http.Request) {
		catalog, err := store.Scan(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		nodes := []map[string]any{}
		links := []map[string]string{}
		seen := map[[3]string]bool{}
		for _, record := range catalog.Records {
			if record.ID == "" {
				continue
			}
			nodes = append(nodes, map[string]any{"id": record.ID, "title": record.Title, "aliases": record.Aliases, "kind": record.Kind, "user": record.User})
			relations := append([]songya.Relation{}, record.Relations...)
			for _, target := range record.Redirects {
				relations = append(relations, songya.Relation{Type: "redirects", Target: target})
			}
			for _, relation := range relations {
				key := [3]string{record.ID, relation.Type, relation.Target}
				if !seen[key] {
					links = append(links, map[string]string{"source": record.ID, "target": relation.Target, "type": relation.Type})
					seen[key] = true
				}
			}
		}
		write(w, map[string]any{"nodes": nodes, "links": links})
	})
	mux.HandleFunc("GET /api/records", func(w http.ResponseWriter, r *http.Request) {
		catalog, err := store.Scan(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		if user := r.URL.Query().Get("classification_user"); user != "" && user != "all" {
			for i := range catalog.Records {
				catalog.Records[i].Type = catalog.Records[i].Types[user]
			}
		}
		records := catalog.Select(songya.Query{Text: r.URL.Query().Get("q"), Kind: songya.Destination(r.URL.Query().Get("kind")), User: r.URL.Query().Get("user")})
		if user := r.URL.Query().Get("classification_user"); user != "" && user != "all" {
			for i := range records {
				records[i].Type = records[i].Types[user]
			}
		}
		summaries := []map[string]any{}
		owners := map[string]bool{}
		types := map[string]bool{}
		for _, record := range catalog.Records {
			if record.User != "" {
				owners[record.User] = true
			}
			if record.Type != "" {
				types[record.Type] = true
			}
		}
		for _, record := range records {
			summaries = append(summaries, map[string]any{"id": record.ID, "source": record.Source, "title": record.Title, "description": record.Description, "type": record.Type, "types": record.Types, "aliases": record.Aliases, "kind": record.Kind, "user": record.User, "updated_at": record.UpdatedAt, "version": record.Revision, "pending": record.Pending || record.Unconfirmed})
		}
		write(w, map[string]any{"records": summaries, "owners": owners, "types": types, "diagnostics": catalog.Diagnostics})
	})
	mux.HandleFunc("GET /api/records/{id}", func(w http.ResponseWriter, r *http.Request) {
		doc, err := store.Read(r.PathValue("id"))
		if err != nil {
			fail(w, err)
			return
		}
		source := doc.OriginalSource()
		if source == nil {
			source, err = doc.Marshal()
			if err != nil {
				fail(w, err)
				return
			}
		}
		if !utf8.Valid(source) {
			writeStatus(w, 422, map[string]any{"error": "This record contains invalid UTF-8 bytes. Its file is intact; repair its encoding before editing."})
			return
		}
		kind := kindFor(doc.ID)

		catalog, err := store.Scan(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		links := catalog.Connections(doc.ID)

		write(w, map[string]any{"id": doc.ID, "kind": kind, "version": doc.Revision, "source": string(source), "draft": describe(string(source), kind), "links": links, "diagnostics": append(doc.Diagnostics(), diagnosticText(catalog.Diagnostics)...), "pending": doc.Pending || doc.Unconfirmed})
	})
	mux.HandleFunc("POST /api/draft", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Source             string             `json:"source"`
			ClassificationUser string             `json:"classification_user"`
			ClassificationType *string            `json:"classification_type"`
			ID                 string             `json:"id"`
			Kind               songya.Destination `json:"kind"`
			Action             string             `json:"action"`
			Type               string             `json:"type"`
			Target             string             `json:"target"`
			Body               *string            `json:"body"`
			Fields             *[]Field           `json:"fields"`
		}
		if !decode(w, r, &input) {
			return
		}
		doc := songya.Parse([]byte(input.Source))
		doc.ID, doc.Kind = input.ID, input.Kind
		if input.ClassificationType != nil {
			if err := doc.SetType(input.ClassificationUser, *input.ClassificationType); err != nil {
				writeStatus(w, 422, map[string]any{"error": err.Error()})
				return
			}
		}
		if input.Body != nil {
			doc.Body = *input.Body
		}
		if input.Fields != nil {
			next := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", HeadComment: doc.Frontmatter.HeadComment, FootComment: doc.Frontmatter.FootComment}
			original := describe(input.Source, input.Kind).Fields
			for _, f := range *input.Fields {
				if f.Key == "id" || f.System || f.Key == "created_at" || f.Key == "updated_at" {
					continue
				}
				if f.Index >= 0 && f.Index < len(original) && f.Key == original[f.Index].Key && f.Value == original[f.Index].Value {
					i := f.Index * 2
					next.Content = append(next.Content, doc.Frontmatter.Content[i], doc.Frontmatter.Content[i+1])
					continue
				}

				var node yaml.Node
				if err := yaml.Unmarshal([]byte(f.Value), &node); err != nil {
					writeStatus(w, 422, map[string]any{"error": fmt.Sprintf("%s: %v. Your draft is unchanged.", f.Key, err)})
					return
				}
				if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
					node = *node.Content[0]
				}
				if node.Kind == 0 {
					node = yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}
				}
				next.Content = append(next.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: f.Key}, &node)
			}
			// Automatic fields cannot be removed or changed through visual controls.
			for i, f := range original {
				if f.System {
					next.Content = append(next.Content, doc.Frontmatter.Content[i*2], doc.Frontmatter.Content[i*2+1])
				}
			}

			doc.Frontmatter = *next
		}
		if input.Action != "" {
			relation := songya.Relation{Type: input.Type, Target: input.Target}
			var err error
			switch input.Action {
			case "up":
				err = doc.MoveConnection(relation, -1)
			case "down":
				err = doc.MoveConnection(relation, 1)
			default:
				err = doc.EditConnection(relation, input.Action == "remove")
			}
			if err != nil {
				fail(w, err)
				return
			}
		}

		source := input.Source
		if input.Body != nil || input.Fields != nil || input.Action != "" || input.ClassificationType != nil {
			data, err := doc.Marshal()
			if err != nil {
				fail(w, err)
				return
			}
			source = string(data)
		}
		catalog, err := store.Scan(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		doc.ID = input.ID
		if doc.ID == "" {
			doc.ID = "draft"
		}
		kept := catalog.Records[:0]
		for _, record := range catalog.Records {
			if record.ID != doc.ID {
				kept = append(kept, record)
			}
		}
		catalog.Records = append(kept, doc.Record(input.Kind))

		write(w, map[string]any{"source": source, "draft": describe(source, input.Kind), "links": catalog.Connections(doc.ID)})
	})
	mux.HandleFunc("POST /api/records", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Key, ID, Version, Source string
			Kind                     songya.Destination
			ClassificationUser       string `json:"classification_user"`
		}
		if !decode(w, r, &input) {
			return
		}
		receipt, err := store.SaveSource(songya.WithTypeUser(r.Context(), input.ClassificationUser), input.Key, input.ID, input.Kind, input.Version, input.Source)
		if receipt.Status != "not_accepted" && receipt.Status != "" {
			chat.recordsRevision.Add(1)
		}
		if err != nil {
			if receipt.Status == "not_accepted" {
				status := 422
				if errors.Is(err, songya.ErrConflict) {
					status = 409
				}
				writeStatus(w, status, map[string]any{"error": err.Error(), "rejected": true})
				return
			}
			fail(w, err)
			return
		}
		if receipt.Status != "committed" {
			writeStatus(w, 503, map[string]any{"error": "Your write is retained but not yet published. Retry Save to check it.", "receipt": receipt})
			return
		}
		id := receipt.Refs["record"]
		data, err := store.BatchData(r.Context(), input.Key, id, "proposed")
		if err != nil {
			fail(w, err)
			return
		}
		version := ""
		for _, record := range receipt.Records {
			if record.ID == id {
				version = record.Revision
			}
		}
		write(w, map[string]any{"id": id, "kind": kindFor(id), "version": version, "source": string(data), "draft": describe(string(data), kindFor(id))})
	})
	mux.HandleFunc("DELETE /api/records/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := store.Delete(r.Context(), r.PathValue("id"), r.Header.Get("If-Match")); err != nil {
			fail(w, err)
			return
		}
		chat.recordsRevision.Add(1)
		write(w, map[string]any{"deleted": true})
	})
	// Requests from other sites must not mutate a locally running store.
	return &Application{chat: chat, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		if r.Method != "GET" && r.Method != "HEAD" && r.Header.Get("Origin") != "" && r.Header.Get("Origin") != "http://"+r.Host && r.Header.Get("Origin") != "https://"+r.Host {
			http.Error(w, "Open Songya directly to edit this store", 403)
			return
		}
		mux.ServeHTTP(w, r)
	})}
}

type Field struct {
	Time        *songya.RecordTime `json:"time,omitempty"`
	Protected   string             `json:"protected,omitempty"`
	Index       int                `json:"index"`
	Text        string             `json:"text"`
	Type        string             `json:"type"`
	Assignments map[string]string  `json:"assignments,omitempty"`
	Key         string             `json:"key"`
	Value       string             `json:"value"`
	System      bool               `json:"system"`
}
type Draft struct {
	Body     string   `json:"body"`
	Fields   []Field  `json:"fields"`
	Warnings []string `json:"warnings"`
}

func describe(source string, kind songya.Destination) Draft {
	doc := songya.Parse([]byte(source))
	lines := strings.SplitAfter(source, "\n")
	closing := len(lines)
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], " \t\r\n") == "---" {
			closing = i
			break
		}
	}
	draft := Draft{Body: doc.Body, Fields: []Field{}, Warnings: doc.Diagnostics()}
	for i := 0; i+1 < len(doc.Frontmatter.Content); i += 2 {
		key := doc.Frontmatter.Content[i]
		name := key.Value
		_ = key.Decode(&name)
		value := doc.Frontmatter.Content[i+1]
		data, err := yaml.Marshal(value)
		if err != nil {
			continue
		}
		typ := "yaml"
		if value.Kind == yaml.ScalarNode {
			switch value.ShortTag() {
			case "!!str":
				typ = "text"
			case "!!int", "!!float":
				typ = "number"
			case "!!bool":
				typ = "boolean"
			}
		}
		system := name == "created_at" || name == "updated_at"
		protected := ""
		if system && key.Line > 0 && key.Line < closing {
			end := closing
			if i+2 < len(doc.Frontmatter.Content) && doc.Frontmatter.Content[i+2].Line > key.Line {
				end = min(end, doc.Frontmatter.Content[i+2].Line)
			}
			protected = strings.Join(lines[key.Line:end], "")
		}
		f := Field{Index: i / 2, Text: value.Value, Type: typ, Key: name, Value: string(data), System: system, Protected: protected}
		if name == "types" {
			f.Assignments = doc.Types()
		}
		if name == "time" && value.Kind == yaml.MappingNode {
			var event songya.RecordTime
			usable := value.Decode(&event) == nil
			keys := []string{"date", "earliest", "latest"}
			dates := []*string{event.Date, event.Earliest, event.Latest}
			if kind == songya.Entities {
				event.Lifetime = true
				keys = []string{"start", "end"}
				dates = []*string{event.Start, event.End}
			}
			for j := 0; j+1 < len(value.Content); j += 2 {
				key := value.Content[j].Value
				usable = usable && slices.Contains(keys, key)
			}
			for _, date := range dates {
				if date != nil {
					_, err := time.Parse(time.DateOnly, *date)
					usable = usable && err == nil
				}
			}
			if usable {
				f.Type, f.Time = "time", &event
			}
		}
		draft.Fields = append(draft.Fields, f)
	}
	return draft
}
func kindFor(id string) songya.Destination {
	switch {
	case strings.HasPrefix(id, "m_"):
		return songya.Memories
	case strings.HasPrefix(id, "e_"):
		return songya.Entities
	case strings.HasPrefix(id, "t_"):
		return songya.Topics
	case strings.HasPrefix(id, "s_"):
		return songya.Stories
	}
	return ""
}
func diagnosticText(issues []songya.Diagnostic) []string {
	var out []string
	for _, issue := range issues {
		out = append(out, issue.Path+": "+issue.Message)
	}
	return out
}
func decode(w http.ResponseWriter, r *http.Request, value any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<20)).Decode(value); err != nil {
		writeStatus(w, 400, map[string]any{"error": err.Error()})
		return false
	}
	return true
}
func write(w http.ResponseWriter, value any) { writeStatus(w, 200, value) }
func writeStatus(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func fail(w http.ResponseWriter, err error) {
	var blocked *songya.DeleteBlocked
	if errors.As(err, &blocked) {
		rows := []map[string]any{}
		for _, record := range blocked.Records {
			rows = append(rows, map[string]any{"id": record.ID, "title": record.Title, "kind": record.Kind, "source": record.Source, "pending": record.Pending})
		}
		writeStatus(w, 409, map[string]any{"error": blocked.Error(), "blockers": rows, "diagnostics": blocked.Diagnostics})
		return
	}
	status := 500
	if errors.Is(err, songya.ErrConflict) {
		status = 409
	}
	if errors.Is(err, fs.ErrNotExist) {
		status = 404
	}
	writeStatus(w, status, map[string]any{"error": err.Error()})
}
