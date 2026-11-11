package songya

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"time"

	"go.yaml.in/yaml/v3"
)

// Ownership already connects a user's memories to their identity. Normalize
// both link directions in the same durable batch as the requested changes.
func normalizeOwnerLinks(root *os.Root, journal *batchJournal, settings *typeConfigurationChange) error {
	entities := map[string]string{}
	entityFor := func(user string) string {
		if entity, ok := entities[user]; ok {
			return entity
		}
		entity := configuredOwnerEntity(root, user)
		entities[user] = entity
		return entity
	}
	if settings != nil {
		entities[settings.User] = settings.Configuration.MyEntity
	}
	docs := map[string]*Document{}
	positions := map[string]int{}
	queue := []string{}
	queued := map[string]bool{}
	add := func(id string) {
		if id != "" && !queued[id] {
			queued[id] = true
			queue = append(queue, id)
		}
	}
	for i, entry := range journal.Entries {
		docs[entry.ID] = Parse(entry.After)
		positions[entry.ID] = i
		add(entry.ID)
	}
	read := func(id string) *Document {
		if doc, ok := docs[id]; ok {
			return doc
		}
		name, err := recordName(id)
		var doc *Document
		if err == nil {
			doc, _ = readLockedCopy(root, id, name)
		}
		docs[id] = doc
		return doc
	}
	// Choosing an identity also removes existing owner links. Other saves only
	// inspect the records being changed and the owner's identity.
	if settings != nil && settings.Configuration.MyEntity != "" {
		var before TypeConfiguration
		_ = json.Unmarshal(settings.Before, &before)
		if before.MyEntity != settings.Configuration.MyEntity {
			entries, err := fs.ReadDir(root.FS(), string(Memories))
			if err != nil {
				return err
			}
			for _, entry := range entries {
				if ValidID(entry.Name()) && kindOf(entry.Name()) == Memories {
					if doc := read(entry.Name()); doc != nil && documentUser(doc) == settings.User {
						add(entry.Name())
					}
				}
			}
			add(settings.Configuration.MyEntity)
		}
	}
	for i := 0; i < len(queue); i++ {
		id := queue[i]
		doc := read(id)
		if doc == nil {
			continue
		}
		if kindOf(id) == Memories {
			add(entityFor(documentUser(doc)))
		}
		if !stripOwnerLinks(doc, id, kindOf(id), entityFor, read) {
			continue
		}
		_ = doc.Set("updated_at", time.Now().UTC())
		data, err := doc.Marshal()
		if err != nil {
			return err
		}
		if position, ok := positions[id]; ok {
			// A redundant requested link alone is not a change to the record.
			if before := journal.Entries[position].Before; len(before) > 0 && len(changedRecordFields(before, data)) == 0 {
				data = before
			}
			journal.Entries[position].After = data
			journal.Entries[position].Revision = dataRevision(data)
			continue
		}
		name, _ := recordName(id)
		if _, err := root.Lstat(pendingName(name)); err == nil {
			return fmt.Errorf("pending work for %s must recover first", id)
		} else if !os.IsNotExist(err) {
			return err
		}
		before, err := readFile(root, name)
		if err != nil {
			return err
		}
		positions[id] = len(journal.Entries)
		journal.Entries = append(journal.Entries, batchEntry{ID: id, Source: name, Base: dataRevision(before), Revision: dataRevision(data), Before: before, After: data})
	}
	return nil
}

func configuredOwnerEntity(root *os.Root, user string) string {
	var cfg struct {
		MyEntity string `json:"my_entity"`
	}
	data, _ := readFile(root, typeConfigurationPath(user))
	// Unreadable settings cannot justify deleting a relationship.
	if json.Unmarshal(data, &cfg) != nil || !ValidID(cfg.MyEntity) || kindOf(cfg.MyEntity) != Entities {
		return ""
	}
	return cfg.MyEntity
}

func normalizeOwnerDocument(root *os.Root, doc *Document, id string, kind Destination) {
	stripOwnerLinks(doc, id, kind, func(user string) string { return configuredOwnerEntity(root, user) }, func(target string) *Document {
		name, err := recordName(target)
		if err != nil {
			return nil
		}
		other, _ := readLockedCopy(root, target, name)
		return other
	})
}

func stripOwnerLinks(doc *Document, id string, kind Destination, entityFor func(string) string, read func(string) *Document) bool {
	if kind != Memories && kind != Entities {
		return false
	}
	ownerEntity := ""
	if kind == Memories {
		ownerEntity = entityFor(documentUser(doc))
	}
	relations := dereference(metadataField(&doc.Frontmatter, "relations"))
	if relations == nil || relations.Kind != yaml.SequenceNode {
		return false
	}
	kept := *relations
	kept.Content = nil
	for _, node := range relations.Content {
		var relation Relation
		remove := false
		if node.Decode(&relation) == nil {
			remove = ownerEntity != "" && relation.Target == ownerEntity
			if kind == Entities && id != "" && kindOf(relation.Target) == Memories {
				if memory := read(relation.Target); memory != nil {
					remove = entityFor(documentUser(memory)) == id
				}
			}
		}
		if !remove {
			kept.Content = append(kept.Content, node)
		}
	}
	if len(kept.Content) == len(relations.Content) {
		return false
	}
	_ = doc.Set("relations", &kept)
	return true
}
