package songya

import (
	_ "embed"
	"encoding/json"
	"hash/fnv"
	"regexp"
	"strings"
)

// The official Catppuccin Latte and Mocha palette is kept locally so
// rendering and color validation do not depend on a network service.
//
//go:embed palette.json
var paletteJSON []byte
var catppuccinPalette = func() map[string]map[string]string {
	var colors map[string]map[string]string
	_ = json.Unmarshal(paletteJSON, &colors)
	return colors
}()
var customColorPattern = regexp.MustCompile(`^#[0-9a-f]{3}([0-9a-f]{3})?$`)
var accentColors = []string{"rosewater", "flamingo", "pink", "mauve", "red", "maroon", "peach", "yellow", "green", "teal", "sky", "sapphire", "blue", "lavender"}

func NormalizeColor(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value != "crust" && value != "mantle" && value != "base" && catppuccinPalette["mocha"][value] != "" {
		return value
	}
	if customColorPattern.MatchString(value) {
		if len(value) == 4 {
			value = "#" + strings.Repeat(value[1:2], 2) + strings.Repeat(value[2:3], 2) + strings.Repeat(value[3:4], 2)
		}
		return value
	}
	return ""
}
func DefaultKindColor(kind Destination) string {
	switch kind {
	case Entities:
		return "green"
	case Memories:
		return "peach"
	case Topics:
		return "lavender"
	case Stories:
		return "red"
	}
	return "text"
}
func DefaultTypeColor(kind Destination, id string) string {
	if kind == Entities {
		if color := map[string]string{"person": "mauve", "group": "lavender", "organization": "blue", "place": "teal", "animal": "peach", "plant": "green", "brand": "flamingo", "object": "sapphire", "food": "yellow", "software": "sky", "creative_work": "pink", "event": "maroon"}[id]; color != "" {
			return color
		}
	}
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(string(kind) + ":" + id))
	return accentColors[int(hash.Sum32())%len(accentColors)]
}
func (cfg TypeConfiguration) KindColor(kind Destination) string {
	if color := NormalizeColor(cfg.KindColors[kind]); color != "" {
		return color
	}
	return DefaultKindColor(kind)
}
func (cfg TypeConfiguration) RecordColor(record Record) string {
	if id := record.Types[cfg.User]; id != "" {
		for _, def := range cfg.Kinds[record.Kind] {
			if def.ID == id {
				if color := NormalizeColor(def.Color); color != "" {
					return color
				}
				return DefaultTypeColor(record.Kind, id)
			}
		}
	}
	return cfg.KindColor(record.Kind)
}

// Missing colors are presentation defaults. No record content or classification
// changes when existing settings acquire their color choices.
func fillTypeColors(cfg *TypeConfiguration, previous TypeConfiguration) {
	if cfg.KindColors == nil {
		cfg.KindColors = map[Destination]string{}
	}
	for _, kind := range destinations {
		if cfg.KindColors[kind] == "" {
			cfg.KindColors[kind] = previous.KindColor(kind)
		}
		for i := range cfg.Kinds[kind] {
			def := &cfg.Kinds[kind][i]
			if def.Color == "" {
				for _, old := range previous.Kinds[kind] {
					if old.ID == def.ID {
						def.Color = old.Color
						break
					}
				}
			}
			if def.Color == "" {
				def.Color = DefaultTypeColor(kind, def.ID)
			}
			if color := NormalizeColor(def.Color); color != "" {
				def.Color = color
			}
		}
		if color := NormalizeColor(cfg.KindColors[kind]); color != "" {
			cfg.KindColors[kind] = color
		}
	}
}
