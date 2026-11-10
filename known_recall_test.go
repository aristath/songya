package songya

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKnownRecallIsIndependentOfRankAndPreservesUncertainty(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	doc, err := s.Create(Memories, map[string]any{"user": "ari"}, "Original")
	if err != nil {
		t.Fatal(err)
	}
	config, err := s.Configuration()
	if err != nil {
		t.Fatal(err)
	}
	config.Recall.DirectLimit = 0
	recall := func(owners ...string) KnownRecollection {
		t.Helper()
		response, err := s.RetrieveWithConfiguration(context.Background(), RecallRequest{Message: "unrelated", Owners: owners, KnownIDs: []string{doc.ID, doc.ID}}, config, nil)
		if err != nil || len(response.Known) != 1 || len(response.Results) != 1 || len(response.Results[0].Items) != 0 {
			t.Fatal(response, err)
		}
		return response.Known[0]
	}
	if state := recall("ari"); state.Status != "current" || state.Item == nil || !strings.Contains(state.Item.Content, "Original") {
		t.Fatal(state)
	}
	doc, err = s.Update(doc.ID, doc.Revision, nil, "Corrected")
	if err != nil {
		t.Fatal(err)
	}
	if state := recall("ari"); state.Status != "current" || !strings.Contains(state.Item.Content, "Corrected") {
		t.Fatal(state)
	}
	if state := recall("bob"); state.Status != "retracted" || state.Item != nil {
		t.Fatal("excluded owner leaked", state)
	}
	name, err := recordName(doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	full := filepath.Join(s.path, name)
	if err := os.Remove(full); err != nil {
		t.Fatal(err)
	}
	if state := recall("ari"); state.Status != "missing" {
		t.Fatal("confirmed deletion not reported", state)
	}
	if err := os.RemoveAll(filepath.Dir(full)); err != nil {
		t.Fatal(err)
	}
	if state := recall("ari"); state.Status != "missing" {
		t.Fatal("deleted record directory not confirmed", state)
	}
	if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(full, 0700); err != nil {
		t.Fatal(err)
	}
	if state := recall("ari"); state.Status != "unknown" {
		t.Fatal("unreadable record treated as deletion", state)
	}
	if err := os.Rename(filepath.Join(s.path, "memories"), filepath.Join(s.path, "unavailable-memories")); err != nil {
		t.Fatal(err)
	}
	if state := recall("ari"); state.Status != "unknown" {
		t.Fatal("unavailable collection treated as deletion", state)
	}
}
