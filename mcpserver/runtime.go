package mcpserver

import (
	"os"
	"runtime/debug"
	"time"
)

var processStarted = time.Now().UTC()
var processVersion = func() string {
	info, _ := debug.ReadBuildInfo()
	return songyaVersion(info)
}()

// Read the Songya module, not the embedding application's version.
func songyaVersion(info *debug.BuildInfo) string {
	if info == nil {
		return "unknown"
	}
	if info.Main.Path == "github.com/aristath/songya" {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" {
				version := setting.Value
				for _, setting := range info.Settings {
					if setting.Key == "vcs.modified" && setting.Value == "true" {
						version += "+modified"
					}
				}
				return version
			}
		}
		return info.Main.Version
	}
	for _, dep := range info.Deps {
		if dep.Path == "github.com/aristath/songya" {
			if dep.Replace != nil {
				return dep.Replace.Path + "@" + dep.Replace.Version
			}
			return dep.Version
		}
	}
	return "unknown"
}

func runtimeStatus() map[string]any {
	return map[string]any{
		"version": processVersion, "pid": os.Getpid(), "started_at": processStarted,
		"capabilities": map[string]bool{
			"image_content": true, "text_pages": true,
			"attachment_resources": true, "attachment_resource_pages": true,
		},
	}
}
