package mcpserver

import (
	"runtime/debug"
	"testing"
)

func TestSongyaVersionIdentifiesRunningModule(t *testing.T) {
	for _, test := range []struct {
		name string
		info *debug.BuildInfo
		want string
	}{
		{"unavailable", nil, "unknown"},
		{"standalone", &debug.BuildInfo{Main: debug.Module{Path: "github.com/aristath/songya", Version: "v1.2.3"}}, "v1.2.3"},
		{"checkout", &debug.BuildInfo{Main: debug.Module{Path: "github.com/aristath/songya"}, Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "songya-revision"}}}, "songya-revision"},
		{"modified checkout", &debug.BuildInfo{Main: debug.Module{Path: "github.com/aristath/songya"}, Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "songya-revision"}, {Key: "vcs.modified", Value: "true"}}}, "songya-revision+modified"},
		{"embedded", &debug.BuildInfo{Main: debug.Module{Path: "clara"}, Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "clara-revision"}}, Deps: []*debug.Module{{Path: "github.com/aristath/songya", Version: "v1.2.3"}}}, "v1.2.3"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := songyaVersion(test.info); got != test.want {
				t.Fatalf("got %q, want %q", got, test.want)
			}
		})
	}
}
