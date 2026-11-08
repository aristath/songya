package songya

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

type TypeDefinition struct {
	ID          string `json:"id" yaml:"id"`
	Name        string `json:"name" yaml:"name"`
	Description string `json:"description" yaml:"description"`
	Color       string `json:"color,omitempty" yaml:"color,omitempty"`
}
type TypeConfiguration struct {
	KindColors  map[Destination]string           `json:"kind_colors" yaml:"kind_colors"`
	User        string                           `json:"user" yaml:"user"`
	Kinds       map[Destination][]TypeDefinition `json:"kinds" yaml:"kinds"`
	Version     string                           `json:"version,omitempty" yaml:"-"`
	Diagnostics []string                         `json:"diagnostics,omitempty" yaml:"-"`
	Usage       map[Destination]map[string]int   `json:"usage,omitempty" yaml:"-"`
}
type typeConfigurationChange struct {
	User          string
	Before, After []byte
	Configuration TypeConfiguration
}
type typeUserKey struct{}

func WithTypeUser(ctx context.Context, user string) context.Context {
	return context.WithValue(ctx, typeUserKey{}, strings.TrimSpace(user))
}
func TypeUser(ctx context.Context) string { user, _ := ctx.Value(typeUserKey{}).(string); return user }
func validTypeUser(user string) bool      { return strings.TrimSpace(user) != "" && user != "all" }
func DefaultTypeConfiguration(user string) TypeConfiguration {
	cfg := TypeConfiguration{User: user, Kinds: map[Destination][]TypeDefinition{Memories: {}, Entities: {}, Topics: {}, Stories: {}}}
	for _, d := range [][3]string{
		{"person", "Person", "An individual represented as a person. A person is not required to be human."},
		{"group", "Group", "A band or informal group of people. Use organization for a business or institution."},
		{"organization", "Organization", "A business, institution, union, political party, or other organization."},
		{"place", "Place", "A physical location, building, room, region, country, or garden."},
		{"animal", "Animal", "An animal. State the species and other identifying details in the description."},
		{"plant", "Plant", "A plant, botanical species, genus, or variety. Use food for an ingredient or dish."},
		{"brand", "Brand", "A brand or trade name. A brand and the company that owns it are separate things."},
		{"object", "Physical item", "A physical item or product model, such as a vehicle, instrument, computer, or clothing."},
		{"food", "Food", "An edible ingredient, food, or dish. Use plant when the record describes a botanical plant."},
		{"software", "Software or online service", "An application, software platform, theme, online service, or software system."},
		{"creative_work", "Creative work", "A book, text, song, recording, television episode, series, or other creative work."},
		{"event", "Named event", "A named event or recurring event series. A person's experience of attending it belongs in a memory."},
	} {
		cfg.Kinds[Entities] = append(cfg.Kinds[Entities], TypeDefinition{ID: d[0], Name: d[1], Description: d[2], Color: DefaultTypeColor(Entities, d[0])})
	}
	fillTypeColors(&cfg, TypeConfiguration{})
	return cfg
}
func typeConfigurationID(user string) string { return "cfg_" + dataRevision([]byte(user)) }
func typeConfigurationPath(user string) string {
	return filepath.Join(".types", dataRevision([]byte(user))+".json")
}
func batchTarget(id string) (string, error) {
	if strings.HasPrefix(id, "cfg_") && validHash(strings.TrimPrefix(id, "cfg_")) {
		return filepath.Join(".types", strings.TrimPrefix(id, "cfg_")+".json"), nil
	}
	return recordName(id)
}
func (s *Store) typeConfigurationRoot(ctx context.Context, root *os.Root, user string, seed bool) (TypeConfiguration, []byte, error) {
	user = strings.TrimSpace(user)
	cfg := DefaultTypeConfiguration(user)
	if !validTypeUser(user) {
		return cfg, nil, fmt.Errorf("choose one user for type settings; all is not a user")
	}
	name := typeConfigurationPath(user)
	if owner, err := readFile(root, filepath.Join(pendingName(name), "batch")); err == nil && validHash(string(owner)) {
		journal, err := loadJournal(root, filepath.Join(".batches", string(owner)))
		if err == nil {
			_, err = s.resumeBatch(root, journal)
		}
		if err != nil {
			return cfg, nil, fmt.Errorf("type settings have retained work to recover: %w", err)
		}
	}
	raw, err := readFile(root, name)
	if errors.Is(err, fs.ErrNotExist) {
		if !seed {
			cfg.Version = dataRevision(nil)
			return cfg, nil, nil
		}
		request := typeConfigurationRequest(cfg, dataRevision(nil), nil, "type-defaults:"+user)
		digest, e := requestRevision(ctx, request)
		if e != nil {
			return cfg, nil, e
		}
		receipt, e := s.applyBatchLocked(ctx, root, request, digest)
		if e != nil || receipt.Status != "committed" {
			return cfg, nil, fmt.Errorf("default type settings retained for retry: %v", e)
		}
		raw, err = readFile(root, name)
	}
	if err != nil {
		return cfg, raw, err
	}
	cfg = TypeConfiguration{User: user, Kinds: map[Destination][]TypeDefinition{Memories: {}, Entities: {}, Topics: {}, Stories: {}}, Version: dataRevision(raw)}
	// Decode kind by kind so damage to one user's list does not hide healthy lists.
	var stored struct {
		User       string                          `json:"user"`
		Kinds      map[Destination]json.RawMessage `json:"kinds"`
		KindColors json.RawMessage                 `json:"kind_colors"`
	}
	if err := json.Unmarshal(raw, &stored); err != nil {
		return cfg, raw, fmt.Errorf("type settings need repair; saved bytes retained: %w", err)
	}
	if stored.User != user {
		return cfg, raw, fmt.Errorf("type settings identify another user; saved bytes retained")
	}
	cfg.KindColors = map[Destination]string{}
	var storedColors map[Destination]json.RawMessage
	if len(stored.KindColors) > 0 && json.Unmarshal(stored.KindColors, &storedColors) != nil {
		cfg.Diagnostics = append(cfg.Diagnostics, "record colors need repair; defaults shown")
	}
	for kind, rawColor := range storedColors {
		if _, err := kind.prefix(); err != nil {
			cfg.Diagnostics = append(cfg.Diagnostics, err.Error())
			continue
		}
		var color string
		if json.Unmarshal(rawColor, &color) != nil {
			cfg.Diagnostics = append(cfg.Diagnostics, fmt.Sprintf("%s color needs repair; default shown", kind))
			continue
		}
		cfg.KindColors[kind] = color
		if color != "" && NormalizeColor(color) == "" {
			cfg.Diagnostics = append(cfg.Diagnostics, fmt.Sprintf("%s color needs a palette preset or hex color", kind))
		}
	}
	for kind, data := range stored.Kinds {
		if _, err := kind.prefix(); err != nil {
			cfg.Diagnostics = append(cfg.Diagnostics, err.Error())
			continue
		}
		var list []json.RawMessage
		if err := json.Unmarshal(data, &list); err != nil {
			cfg.Diagnostics = append(cfg.Diagnostics, fmt.Sprintf("%s types need repair", kind))
			continue
		}
		seen := map[string]bool{}
		for _, entry := range list {
			var def TypeDefinition
			decodeErr := json.Unmarshal(entry, &def)
			if decodeErr != nil {
				var fields map[string]json.RawMessage
				if json.Unmarshal(entry, &fields) == nil && fields["color"] != nil {
					delete(fields, "color")
					core, _ := json.Marshal(fields)
					decodeErr = json.Unmarshal(core, &def)
					def.Color = ""
					cfg.Diagnostics = append(cfg.Diagnostics, fmt.Sprintf("%s type color needs repair; default shown", kind))
				}
			}
			if err := decodeErr; err != nil || !typeIDPattern.MatchString(def.ID) || strings.TrimSpace(def.Name) == "" || strings.TrimSpace(def.Description) == "" || seen[def.ID] {
				cfg.Diagnostics = append(cfg.Diagnostics, fmt.Sprintf("%s has an unreadable or duplicate type; original retained", kind))
				continue
			}
			if def.Color != "" && NormalizeColor(def.Color) == "" {
				cfg.Diagnostics = append(cfg.Diagnostics, fmt.Sprintf("%s type %s needs a palette preset or hex color", kind, def.ID))
			}
			seen[def.ID] = true
			cfg.Kinds[kind] = append(cfg.Kinds[kind], def)
		}
	}
	fillTypeColors(&cfg, TypeConfiguration{})
	return cfg, raw, nil
}
func (s *Store) TypeConfiguration(ctx context.Context, user string) (TypeConfiguration, error) {
	var cfg TypeConfiguration
	err := s.lockedContext(ctx, func(root *os.Root) error {
		var err error
		cfg, _, err = s.typeConfigurationRoot(ctx, root, user, true)
		return err
	})
	if err != nil {
		return cfg, err
	}
	// Usage is a display hint. Read the catalogue after releasing the write lock;
	// otherwise each record read sees our own lock as an overlapping writer and
	// needlessly rechecks the entire retained batch history. Saves check actual
	// current assignments under their write lock before removing a type.
	cfg.Usage = map[Destination]map[string]int{}
	for _, kind := range destinations {
		cfg.Usage[kind] = map[string]int{}
	}
	catalog, err := s.Scan(ctx)
	if err != nil {
		cfg.Diagnostics = append(cfg.Diagnostics, "Type usage could not be counted: "+err.Error())
		return cfg, nil
	}
	for _, issue := range catalog.Diagnostics {
		cfg.Diagnostics = append(cfg.Diagnostics, issue.Path+": "+issue.Message)
	}
	for _, record := range catalog.Records {
		if typ := record.Types[cfg.User]; typ != "" {
			cfg.Usage[record.Kind][typ]++
		}
	}
	return cfg, nil
}

var typeIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

func checkTypeConfiguration(cfg, previous TypeConfiguration) error {
	if !validTypeUser(cfg.User) {
		return fmt.Errorf("choose one user for type settings")
	}
	for kind, color := range cfg.KindColors {
		if _, err := kind.prefix(); err != nil {
			return err
		}
		if NormalizeColor(color) == "" && color != previous.KindColors[kind] {
			return fmt.Errorf("%s color must be a palette preset or #RRGGBB hex color", kind)
		}
	}
	for kind, defs := range cfg.Kinds {
		if _, err := kind.prefix(); err != nil {
			return err
		}
		seen := map[string]bool{}
		names := map[string]bool{}
		for _, def := range defs {
			if NormalizeColor(def.Color) == "" {
				unchanged := false
				for _, old := range previous.Kinds[kind] {
					if old.ID == def.ID && old.Color == def.Color {
						unchanged = true
					}
				}
				if !unchanged {
					return fmt.Errorf("type %s color must be a palette preset or #RRGGBB hex color", def.ID)
				}
			}
			key := strings.ToLower(strings.TrimSpace(def.Name))
			if !typeIDPattern.MatchString(def.ID) || key == "" || strings.TrimSpace(def.Description) == "" {
				return fmt.Errorf("each type needs a stable lowercase ID, a name, and a short definition")
			}
			if seen[def.ID] || names[key] {
				return fmt.Errorf("%s has a duplicate type ID or name", kind)
			}
			seen[def.ID] = true
			names[key] = true
		}
	}
	return nil
}
func typeConfigurationRequest(cfg TypeConfiguration, version string, replacements map[Destination]map[string]string, key string) BatchRequest {
	clean := TypeConfiguration{User: cfg.User, Kinds: map[Destination][]TypeDefinition{}, KindColors: maps.Clone(cfg.KindColors)}
	for kind, defs := range cfg.Kinds {
		clean.Kinds[kind] = append([]TypeDefinition{}, defs...)
	}
	raw, _ := json.MarshalIndent(clean, "", "  ")
	raw = append(raw, '\n')
	change := &typeConfigurationChange{User: cfg.User, After: raw, Configuration: clean}
	intent := map[string]any{"user": cfg.User, "version": version, "configuration": cfg.Kinds, "replacements": replacements}
	if cfg.KindColors != nil {
		intent["kind_colors"] = cfg.KindColors
	}
	request := BatchRequest{Key: key, typeConfiguration: change, Changes: []Change{{Fields: intent}}}
	request.prepare = func(root *os.Root) ([]Change, error) {
		before, readErr := readFile(root, typeConfigurationPath(cfg.User))
		if readErr != nil && !errors.Is(readErr, fs.ErrNotExist) {
			return nil, readErr
		}
		if dataRevision(before) != version {
			return nil, fmt.Errorf("type settings changed; reopen them before saving: %w", ErrConflict)
		}
		change.Before = before
		changes := []Change{}
		var previous TypeConfiguration
		_ = json.Unmarshal(before, &previous)
		fillTypeColors(&clean, previous)
		if err := checkTypeConfiguration(clean, previous); err != nil {
			return nil, err
		}
		change.Configuration = clean
		change.After, _ = json.MarshalIndent(clean, "", "  ")
		change.After = append(change.After, '\n')
		for _, kind := range destinations {
			allowed := map[string]bool{}
			for _, def := range cfg.Kinds[kind] {
				allowed[def.ID] = true
			}
			removed := map[string]bool{}
			for _, def := range previous.Kinds[kind] {
				if !allowed[def.ID] {
					removed[def.ID] = true
				}
			}
			// Unknown old assignments are preserved unless explicitly reassigned.
			for id := range replacements[kind] {
				if !allowed[id] {
					removed[id] = true
				}
			}
			if len(removed) == 0 {
				continue
			}
			entries, err := fs.ReadDir(root.FS(), string(kind))
			if err != nil {
				return nil, err
			}
			for _, entry := range entries {
				if !entry.IsDir() || !ValidID(entry.Name()) || kindOf(entry.Name()) != kind {
					continue
				}
				id := entry.Name()
				doc, err := readLockedCopy(root, id, filepath.Join(string(kind), entry.Name(), "record.md"))
				if err != nil {
					return nil, err
				}
				assigned := documentTypes(doc)
				old := assigned[cfg.User]
				if !removed[old] {
					continue
				}
				replacement, explicit := replacements[kind][old]
				if !explicit {
					return nil, fmt.Errorf("%s type %q is still used; choose a replacement or explicitly clear its assignments", kind, old)
				}
				if replacement != "" && !allowed[replacement] {
					return nil, fmt.Errorf("replacement type %q is not in the new %s list", replacement, kind)
				}
				if err := doc.SetType(cfg.User, replacement); err != nil {
					return nil, err
				}
				changes = append(changes, Change{ID: id, Revision: doc.Revision, Fields: map[string]any{"types": metadataField(&doc.Frontmatter, "types")}, preserveUnchanged: true})
			}
		}
		if err := ensureDirectory(root, ".types"); err != nil {
			return nil, err
		}
		if err := syncPath(root, "."); err != nil {
			return nil, err
		}
		return changes, nil
	}
	return request
}
func (s *Store) SaveTypeConfiguration(ctx context.Context, cfg TypeConfiguration, version string, replacements map[Destination]map[string]string, key string) (Receipt, error) {
	cfg.User = strings.TrimSpace(cfg.User)
	if key == "" {
		return Receipt{}, fmt.Errorf("a stable settings request key is required")
	}
	return s.ApplyBatch(ctx, typeConfigurationRequest(cfg, version, replacements, key))
}
func documentTypes(doc *Document) map[string]string {
	result := map[string]string{}
	if doc == nil {
		return result
	}
	node := dereference(metadataField(&doc.Frontmatter, "types"))
	if node == nil || node.Kind != yaml.MappingNode {
		return result
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		var user, typ string
		if node.Content[i].Decode(&user) == nil && node.Content[i+1].Decode(&typ) == nil && validTypeUser(user) && strings.TrimSpace(typ) != "" {
			result[user] = strings.TrimSpace(typ)
		}
	}
	return result
}
func (d *Document) Types() map[string]string   { return documentTypes(d) }
func (d *Document) TypeFor(user string) string { return documentTypes(d)[user] }
func (s *Store) validateTypeChange(ctx context.Context, root *os.Root, before, after *Document, kind Destination, override *typeConfigurationChange) error {
	oldGlobal, newGlobal := metadataField(&before.Frontmatter, "type"), metadataField(&after.Frontmatter, "type")
	if !sameFieldValue(oldGlobal, newGlobal) && newGlobal != nil && newGlobal.ShortTag() != "!!null" {
		return fmt.Errorf("type assignments belong to a user; use types: {user: type_id}, or the tool's type and user fields")
	}
	oldNode, newNode := metadataField(&before.Frontmatter, "types"), metadataField(&after.Frontmatter, "types")
	if sameFieldValue(oldNode, newNode) {
		return nil
	}
	if newNode != nil && newNode.ShortTag() != "!!null" && newNode.Kind != yaml.MappingNode {
		return fmt.Errorf("types must map user IDs to type IDs")
	}
	old, next := documentTypes(before), documentTypes(after)
	selected := TypeUser(ctx)
	users := map[string]bool{}
	for user := range old {
		users[user] = true
	}
	for user := range next {
		users[user] = true
	}
	for user := range users {
		if old[user] == next[user] {
			continue
		}
		if selected != "" && selected != user {
			return fmt.Errorf("this operation selects user %q; leave %q's type unchanged", selected, user)
		}
		if next[user] == "" {
			continue
		}
		var cfg TypeConfiguration
		var err error
		if override != nil && override.User == user {
			cfg = override.Configuration
		} else {
			cfg, _, err = s.typeConfigurationRoot(ctx, root, user, true)
		}
		if err != nil {
			return err
		}
		allowed := []string{}
		matched := ""
		for _, def := range cfg.Kinds[kind] {
			allowed = append(allowed, def.ID)
			if strings.EqualFold(strings.TrimSpace(next[user]), def.ID) {
				matched = def.ID
			}
		}
		if matched == "" {
			for _, def := range cfg.Kinds[kind] {
				if strings.EqualFold(strings.TrimSpace(next[user]), def.Name) {
					matched = def.ID
					break
				}
			}
		}
		if matched == "" {
			return fmt.Errorf("type %q is not allowed for user %q in %s; allowed types: %s. Leave the type unset when none fits", next[user], user, kind, strings.Join(allowed, ", "))
		}
		next[user] = matched
		if next[user] != documentTypes(after)[user] {
			if node := metadataField(newNode, user); node != nil {
				assignNode(node, yamlValue(matched))
			}
		}
	}
	// Refuse malformed new values rather than silently dropping them on decoding.
	if newNode != nil && newNode.Kind == yaml.MappingNode {
		seen := map[string]bool{}
		for i := 0; i+1 < len(newNode.Content); i += 2 {
			var user, typ string
			key, val := newNode.Content[i], newNode.Content[i+1]
			if key.Decode(&user) != nil || !validTypeUser(user) || seen[user] || val.ShortTag() == "!!null" || val.Decode(&typ) != nil || strings.TrimSpace(typ) == "" {
				if sameFieldValue(metadataField(oldNode, key.Value), val) {
					continue
				}
				return fmt.Errorf("each types entry needs one user and a nonempty type ID; remove a user's entry to clear it")
			}
			seen[user] = true
		}
	}

	return nil
}
func typeFields(doc *Document, user string, typ *string, fields map[string]any) error {
	if typ == nil {
		return nil
	}
	if !validTypeUser(user) {
		return fmt.Errorf("supply the selected user when assigning a type")
	}
	if doc == nil {
		doc = &Document{}
	}
	if err := doc.SetType(user, *typ); err != nil {
		return err
	}
	fields["types"] = metadataField(&doc.Frontmatter, "types")
	return nil
}
func TypeInstructions(cfg TypeConfiguration) string {
	var out strings.Builder
	fmt.Fprintf(&out, "Type choices for user %q. Use only these IDs. Omit type if none fits. Do not invent types or edit settings. Types classify this user's view; they do not change ownership.\n", cfg.User)
	for _, kind := range destinations {
		fmt.Fprintf(&out, "%s:\n", kind)
		for _, def := range cfg.Kinds[kind] {
			fmt.Fprintf(&out, "- %s (%s): %s\n", def.ID, def.Name, def.Description)
		}
		if len(cfg.Kinds[kind]) == 0 {
			out.WriteString("- No types configured. Omit type.\n")
		}
	}
	return out.String()
}
func TypeUsers(ctx context.Context, s *Store) ([]string, error) {
	catalog, err := s.Scan(ctx)
	if err != nil {
		return nil, err
	}
	users := map[string]bool{}
	for _, r := range catalog.Records {
		if validTypeUser(r.User) {
			users[r.User] = true
		}
		for u := range r.Types {
			users[u] = true
		}
	}
	root, err := os.OpenRoot(s.Directory())
	if err != nil {
		return nil, err
	}
	defer root.Close()
	entries, readErr := fs.ReadDir(root.FS(), ".types")
	if readErr != nil && !errors.Is(readErr, fs.ErrNotExist) {
		return nil, readErr
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := readFile(root, filepath.Join(".types", entry.Name()))
		if err != nil {
			continue
		}
		var cfg TypeConfiguration
		if json.Unmarshal(data, &cfg) == nil && validTypeUser(cfg.User) && entry.Name() == dataRevision([]byte(cfg.User))+".json" {
			users[cfg.User] = true
		}
	}
	result := []string{}
	for user := range users {
		result = append(result, user)
	}
	sort.Strings(result)
	return result, nil
}

// SetType edits one user's assignment while preserving every other YAML value.
func (d *Document) SetType(user, typ string) error {
	if !validTypeUser(user) {
		return fmt.Errorf("choose one user before assigning a type")
	}
	old := dereference(metadataField(&d.Frontmatter, "types"))
	node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	if old != nil && old.ShortTag() != "!!null" {
		if old.Kind != yaml.MappingNode {
			return fmt.Errorf("types needs repair before changing a classification; original data retained")
		}
		cloned, issues := normalizeNode(old)
		d.warnings = append(d.warnings, issues...)
		node = cloned
	}
	for i := len(node.Content) - 2; i >= 0; i -= 2 {
		var key string
		if node.Content[i].Decode(&key) == nil && key == user {
			node.Content = append(node.Content[:i], node.Content[i+2:]...)
		}
	}
	if strings.TrimSpace(typ) != "" {
		node.Content = append(node.Content, yamlValue(user), yamlValue(strings.TrimSpace(typ)))
	}
	return d.Set("types", node)
}
