package buildinfo

import "testing"

func TestCurrentNormalizesEmptyValues(t *testing.T) {
	oldVersion, oldCommit := Version, Commit
	t.Cleanup(func() { Version, Commit = oldVersion, oldCommit })
	Version, Commit = " ", ""
	info := Current()
	if info.Version != "0.1.0-dev" || info.Commit != "unknown" {
		t.Fatalf("unexpected info: %+v", info)
	}
}
