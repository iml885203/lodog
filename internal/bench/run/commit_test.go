package run

import (
	"runtime/debug"
	"testing"
)

func TestTheBuildCommitMarksAnUncommittedTree(t *testing.T) {
	setting := func(k, v string) debug.BuildSetting { return debug.BuildSetting{Key: k, Value: v} }
	cases := []struct {
		name     string
		settings []debug.BuildSetting
		want     string
	}{
		{"clean", []debug.BuildSetting{setting("vcs.revision", "abc123"), setting("vcs.modified", "false")}, "abc123"},
		{"dirty", []debug.BuildSetting{setting("vcs.revision", "abc123"), setting("vcs.modified", "true")}, "abc123-dirty"},
		{"no vcs", []debug.BuildSetting{setting("-compiler", "gc")}, ""},
	}
	for _, tc := range cases {
		if got := commitOf(&debug.BuildInfo{Settings: tc.settings}); got != tc.want {
			t.Errorf("%s: commit = %q, want %q", tc.name, got, tc.want)
		}
	}
}
