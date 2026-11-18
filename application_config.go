package songya

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ApplicationConfiguration belongs to the administrator UI. Agent clients keep
// their own independently configured list of accessible store directories.
type ApplicationConfiguration struct {
	Stores   map[string]string `json:"stores"`
	Password string            `json:"password"`
}

func LoadApplicationConfiguration(path string) (ApplicationConfiguration, error) {
	var config ApplicationConfiguration
	data, err := os.ReadFile(path)
	if err != nil {
		return config, err
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return config, err
	}
	if config.Password == "" {
		return config, fmt.Errorf("configure the administrator password")
	}
	if len(config.Stores) == 0 {
		return config, fmt.Errorf("configure at least one store")
	}
	for name, directory := range config.Stores {
		if name == "" || strings.ContainsAny(name, "/\\") || name == "." || name == ".." {
			return config, fmt.Errorf("invalid store name %q", name)
		}
		if strings.TrimSpace(directory) == "" {
			return config, fmt.Errorf("store %q needs a directory", name)
		}
		if directory == "~" || strings.HasPrefix(directory, "~/") {
			home, err := os.UserHomeDir()
			if err != nil {
				return config, err
			}
			if directory == "~" {
				directory = home
			} else {
				directory = filepath.Join(home, strings.TrimPrefix(directory, "~/"))
			}
		} else if !filepath.IsAbs(directory) {
			directory = filepath.Join(filepath.Dir(path), directory)
		}
		config.Stores[name] = filepath.Clean(directory)
	}
	return config, nil
}
