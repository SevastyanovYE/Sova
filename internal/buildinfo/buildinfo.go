package buildinfo

import "strings"

// Version and Commit are replaced by -ldflags for production builds.
var (
	Version = "0.1.0-dev"
	Commit  = "unknown"
)

type Info struct {
	Version string
	Commit  string
}

func Current() Info {
	version := strings.TrimSpace(Version)
	if version == "" {
		version = "0.1.0-dev"
	}
	commit := strings.TrimSpace(Commit)
	if commit == "" {
		commit = "unknown"
	}
	return Info{Version: version, Commit: commit}
}
