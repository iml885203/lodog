package run

import "runtime/debug"

// BuildCommit is the git commit this binary was built from, with "-dirty"
// appended when the working tree had uncommitted changes, or "" when the
// binary carries no version control information (go build -buildvcs=false, a
// binary built outside a checkout). A run records it in meta.yaml so a score
// can be traced to the lodog that produced it.
func BuildCommit() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	return commitOf(info)
}

func commitOf(info *debug.BuildInfo) string {
	commit, dirty := "", false
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			commit = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if commit != "" && dirty {
		commit += "-dirty"
	}
	return commit
}
