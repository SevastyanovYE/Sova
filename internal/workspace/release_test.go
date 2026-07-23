package workspace

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SevastyanovYE/Sova/internal/buildinfo"
	sqlitestore "github.com/SevastyanovYE/Sova/internal/storage/sqlite"
)

func TestDeploymentReceiptExecuteRequiresEmbeddedReleaseIdentity(t *testing.T) {
	store, err := sqlitestore.Open(filepath.Join(t.TempDir(), "release.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	oldVersion, oldCommit := buildinfo.Version, buildinfo.Commit
	defer func() { buildinfo.Version, buildinfo.Commit = oldVersion, oldCommit }()
	buildinfo.Version, buildinfo.Commit = "0.1.0-dev", "unknown"
	_, err = RecordDeploymentReceipt(context.Background(), store, DeploymentReceiptOptions{
		Version: "0.1.0", Commit: "abc", Checks: "tests", Execute: true, Now: time.Now(),
	})
	if err == nil || !strings.Contains(err.Error(), "embedded release") {
		t.Fatalf("error = %v", err)
	}

	buildinfo.Version, buildinfo.Commit = "0.1.0", "abc"
	receipt, err := RecordDeploymentReceipt(context.Background(), store, DeploymentReceiptOptions{
		Checks: "tests, doctors", Execute: true, Now: time.Now(),
	})
	if err != nil || receipt.Version != "0.1.0" || receipt.Commit != "abc" {
		t.Fatalf("receipt=%+v err=%v", receipt, err)
	}
}
