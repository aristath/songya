package songya

import (
	"context"
	"testing"
)

func TestOriginalWordsAreSeparateFromGeneratedMemoryAndSurviveRetry(t *testing.T) {
	store, _ := Open(t.TempDir())
	original := "  Correction: we were together from May to November 2019.\nI moved on December 1, 2019.  "
	generated := "Ari moved on December 1, 2019 after the breakup."
	ctx := WithOriginalText(context.Background(), original)
	input := MemoryInput{Key: "source-test", Text: &generated}
	receipt, err := store.SaveMemory(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	id := receipt.Refs["memory"]
	account, err := store.BatchData(ctx, receipt.Key, "", "account")
	if err != nil || string(account) != original {
		t.Fatalf("original words changed: %q %v", account, err)
	}
	doc, _ := store.Read(id)
	accounts := nodeStrings("original_accounts", doc)
	if doc.Body != generated || len(accounts) != 1 || accounts[0] != original {
		t.Fatal(doc.Body, accounts)
	}
	// Recovery keeps the accepted source; it must not replay or relabel a write.
	retry, err := store.SaveMemory(WithOriginalText(context.Background(), "Later unrelated words"), input)
	if err != nil || retry.Refs["memory"] != id {
		t.Fatal(retry, err)
	}
	account, _ = store.BatchData(ctx, receipt.Key, "", "account")
	if string(account) != original {
		t.Fatal("retry replaced the original words")
	}
	changed := "Different generated text"
	input.Text = &changed
	if _, err := store.SaveMemory(ctx, input); err == nil {
		t.Fatal("same request key accepted different record changes")
	}
	// A caller without original words must not label generated prose as testimony.
	input.Key = "no-original"
	receipt, err = store.SaveMemory(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	account, _ = store.BatchData(ctx, receipt.Key, "", "account")
	if len(account) != 0 {
		t.Fatalf("generated prose was called original: %q", account)
	}
}
