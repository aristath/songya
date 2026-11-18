// songya-query is the JSON query boundary for application recall and browsing.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/aristath/songya"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	var input struct {
		Store    string             `json:"store"`
		Config   string             `json:"config"`
		Command  string             `json:"command"`
		Messages []string           `json:"messages"`
		Source   string             `json:"source"`
		ID       string             `json:"id"`
		Text     string             `json:"text"`
		Kind     songya.Destination `json:"kind"`
		Offset   int                `json:"offset"`
		Limit    int                `json:"limit"`
	}
	if err := json.NewDecoder(os.Stdin).Decode(&input); err != nil {
		return err
	}
	if input.Store == "" {
		return fmt.Errorf("provide the Songya store")
	}
	if input.Config == "" {
		input.Config = filepath.Join(input.Store, "config.json")
	}
	config, issue := songya.LoadEmbeddingConfig(input.Config)
	if issue != nil {
		fmt.Fprintln(os.Stderr, "Songya configuration needs repair; readable values and defaults remain usable")
	}
	// Configuration inspection never opens the store and never returns credentials.
	if input.Command == "configuration" {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"model": config.Model, "base_url": config.BaseURL, "chunk_size": config.ChunkSize, "incomplete": issue != nil})
	}
	store, err := songya.OpenExisting(input.Store)
	if err != nil {
		return err
	}
	store.UseConfiguration(input.Config)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var result any
	switch input.Command {
	case "recall":
		result, err = store.Retrieve(ctx, songya.RecallRequest{Messages: input.Messages})
	case "list", "read", "stats":
		var catalog songya.Catalog
		catalog, err = store.Scan(ctx)
		if err != nil {
			return err
		}
		if input.Command == "read" {
			var record *songya.Record
			for _, r := range catalog.Records {
				if (input.Source != "" && r.Source == input.Source) || (input.Source == "" && r.ID == input.ID) {
					record = &r
					break
				}
			}
			result = map[string]any{"record": record, "diagnostics": catalog.Diagnostics}
			break
		}
		if input.Kind == "all" {
			input.Kind = ""
		}
		records := catalog.Select(songya.Query{Text: input.Text, Kind: input.Kind})
		if input.Command == "stats" {
			counts := map[string]int{}
			for _, r := range records {
				counts[string(r.Kind)]++
			}
			result = map[string]any{"counts": counts, "diagnostics": catalog.Diagnostics}
			break
		}
		start := min(max(0, input.Offset), len(records))
		size := input.Limit
		if size <= 0 {
			size = 25
		}
		end := start + min(size, len(records)-start)
		summaries := []map[string]any{}
		for _, r := range records[start:end] {
			preview := []rune(r.Body)
			if len(preview) > 1500 {
				preview = preview[:1500]
			}
			summaries = append(summaries, map[string]any{"id": r.ID, "source": r.Source, "kind": r.Kind, "title": r.Title, "preview": string(preview), "pending": r.Pending, "unconfirmed": r.Unconfirmed})
		}
		result = map[string]any{"engine": "songya", "items": summaries, "total": len(records), "diagnostics": catalog.Diagnostics}
	default:
		return fmt.Errorf("unknown read operation %q", input.Command)
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}
