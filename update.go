package songya

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// ErrConflict means the committed bytes differ from the update's base revision.
var ErrConflict = errors.New("record changed since it was read")

// Update applies metadata changes and replaces the body of a committed record.
// Pass the revision from Read or a committed Create result. Identity and creation
// time are preserved. An error with Pending=true retains this attempt in staging;
// Recover can retry it without replacing a conflicting committed version.
func (s *Store) Update(id, revision string, fields map[string]any, body string) (*Document, error) {
	name, err := recordName(id)
	if err != nil {
		return nil, err
	}
	revision = strings.ToLower(strings.TrimSpace(revision))
	if revision == "" {
		return nil, errors.New("update requires the revision of a committed record")
	}
	doc, err := s.Read(id)
	if err != nil {
		return nil, err
	}
	if doc.Pending || doc.Unconfirmed || doc.Revision != revision {
		return doc, fmt.Errorf("update %s: %w", id, ErrConflict)
	}
	if err := validateTimeInput(fields, kindOf(id)); err != nil {
		return doc, err
	}
	previousTime := metadataField(&Parse(doc.OriginalSource()).Frontmatter, "time")
	applyFields(doc, fields, body)
	if err := requireUser(doc, kindOf(id)); err != nil {
		return doc, err
	}
	if err := validateTimeChange(previousTime, metadataField(&doc.Frontmatter, "time"), kindOf(id)); err != nil {
		return doc, err
	}
	var data []byte
	err = s.locked(true, func(root *os.Root) (err error) {
		if err := s.recoverRecordFiles(root, id); err != nil {
			return err
		}
		if err := s.validateTypeChange(context.Background(), root, Parse(doc.OriginalSource()), doc, kindOf(id), nil); err != nil {
			return err
		}
		data, err = doc.Marshal()
		if err != nil {
			return err
		}
		defer func() {
			if doc.Pending || err == nil {
				s.note(name, err)
			}
		}()
		if _, err := root.Lstat(pendingName(name)); err == nil {
			_, err := s.complete(root, name)
			s.note(name, err)
			if err != nil {
				return fmt.Errorf("pending work for %s must recover first: %w", id, err)
			}
		}
		current, err := readFile(root, name)
		if err != nil {
			return err
		}
		if dataRevision(current) != revision {
			return fmt.Errorf("update %s: %w", id, ErrConflict)
		}
		retained, err := stage(root, name, revision, data)
		doc.Pending = retained
		if err != nil {
			return err
		}
		published, err := s.publishUpdate(root, name, revision, data)
		doc.Pending = !published
		return err
	})
	if err != nil {
		doc.warnings = append(doc.warnings, err.Error())
	} else {
		doc.Revision = dataRevision(data)
	}
	return doc, err
}

func applyFields(doc *Document, fields map[string]any, body string) {
	doc.warnings = slices.DeleteFunc(doc.warnings, func(warning string) bool { return warning == pendingWarning })
	created, _ := normalizeNode(metadataField(&doc.Frontmatter, "created_at"))
	for name, value := range fields {
		if name != "id" && name != "created_at" && name != "updated_at" {
			if err := doc.Set(name, value); err != nil {
				_ = doc.Set(name, issueValue(err.Error()))
			}
		}
	}
	_ = doc.Set("updated_at", time.Now().UTC())
	// An edit to an anchor or merge source must not change creation time indirectly.
	changed, _ := normalizeNode(metadataField(&doc.Frontmatter, "created_at"))
	// These independent snapshots compare values and alias targets, not anchor
	// spelling or parser positions. Set assigns labels again if restoration is needed.
	for _, snapshot := range []*yaml.Node{created, changed} {
		walkNodes(snapshot, func(node *yaml.Node) {
			node.Anchor, node.Line, node.Column = "", 0, 0
			if node.Kind == yaml.AliasNode {
				node.Value = ""
			}
		})
	}
	if !reflect.DeepEqual(created, changed) {
		_ = doc.Set("created_at", created)
	}
	doc.Body = body
}

func (s *Store) publishUpdate(root *os.Root, name, base string, data []byte) (bool, error) {
	ready := filepath.Join(pendingName(name), "publish")
	// Keep the synced payload while a second hard link is atomically renamed.
	if err := linkData(root, temporaryName(name, base, data), ready, data); err != nil {
		return false, err
	}
	current, err := readFile(root, name)
	if err != nil {
		return false, err
	}
	switch actual := dataRevision(current); {
	case actual == dataRevision(data):
		if err := syncPath(root, name); err != nil {
			return false, err
		}
	case strings.EqualFold(actual, base):
		if err := root.Rename(ready, name); err != nil {
			return false, err
		}
	default:
		return false, fmt.Errorf("update %s: %w", name, ErrConflict)
	}
	if err := syncPath(root, filepath.Dir(name)); err != nil {
		return false, err
	}
	if remaining, err := readFile(root, ready); err == nil && bytes.Equal(remaining, data) {
		s.note(ready, root.Remove(ready))
	}
	s.cleanup(root, name, temporaryName(name, base, data))
	return true, nil
}
