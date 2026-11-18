package songya_test

import (
	"context"
	"fmt"
	"os"

	"github.com/aristath/songya"
)

func ExampleStore() {
	path, err := os.MkdirTemp("", "songya-example-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(path)
	store, err := songya.Open(path)
	if err != nil {
		panic(err)
	}
	created, err := store.Create(songya.Memories, map[string]any{
		"user":   "ari",
		"title":  "Example",
		"custom": map[string]any{"labels": []string{"one", "two"}},
	}, "Markdown body.")
	if err != nil {
		panic(err)
	}
	read, err := store.Read(created.ID)
	if err != nil {
		panic(err)
	}
	fmt.Println(read.Body)
	updated, err := store.Update(read.ID, read.Revision, map[string]any{"title": "Edited"}, "More detail.")
	if err != nil {
		panic(err)
	}
	fmt.Println(updated.Body)
	// Output:
	// Markdown body.
	// More detail.
}

func ExampleStore_ApplyBatch() {
	path, err := os.MkdirTemp("", "songya-capture-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(path)
	store, err := songya.Open(path)
	if err != nil {
		panic(err)
	}
	ctx := context.Background()
	prepared, err := store.Prepare(ctx, "I met John for coffee.", 5)
	if err != nil {
		panic(err)
	}
	body := "I met John for coffee."
	receipt, err := store.ApplyBatch(ctx, songya.BatchRequest{Key: prepared.Key, Account: body, Changes: []songya.Change{
		{Ref: "john", Kind: songya.Entities, Fields: map[string]any{"name": "John"}},
		{Ref: "episode", Fields: map[string]any{"relations": []songya.Relation{{Type: "with", Target: "$john"}}}, Body: &body},
	}})
	if err != nil {
		panic(err)
	}
	doc, err := store.Read(receipt.Refs["episode"])
	if err != nil {
		panic(err)
	}
	fmt.Println(receipt.Status)
	fmt.Println(doc.Body)
	fmt.Println(doc.Metadata().Relations[0].Target == receipt.Refs["john"])
	// Output:
	// committed
	// I met John for coffee.
	// true
}
