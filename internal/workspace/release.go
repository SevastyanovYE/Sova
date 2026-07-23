package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/SevastyanovYE/Sova/internal/buildinfo"
	"github.com/SevastyanovYE/Sova/internal/config"
	"github.com/SevastyanovYE/Sova/internal/nest"
	"github.com/SevastyanovYE/Sova/internal/releaseinfo"
	sqlitestore "github.com/SevastyanovYE/Sova/internal/storage/sqlite"
)

type DeploymentReceiptOptions struct {
	Environment string
	Version     string
	Commit      string
	Checks      string
	Execute     bool
	Now         time.Time
}

type ReleaseAnnouncementOptions struct {
	RepoDir     string
	Environment string
	Version     string
	Commit      string
	BaseCommit  string
	Execute     bool
	Now         time.Time
}

type ReleaseAnnouncementResult struct {
	Text      string
	Version   string
	Commit    string
	MessageID int
	Sent      bool
}

func RecordDeploymentReceipt(ctx context.Context, store *sqlitestore.Store, opts DeploymentReceiptOptions) (sqlitestore.WorkspaceDeploymentReceipt, error) {
	if store == nil {
		return sqlitestore.WorkspaceDeploymentReceipt{}, fmt.Errorf("store is required")
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	receipt := sqlitestore.WorkspaceDeploymentReceipt{
		Environment: valueOrReleaseDefault(opts.Environment, "production"),
		Version:     strings.TrimSpace(opts.Version),
		Commit:      strings.TrimSpace(opts.Commit),
		Checks:      strings.TrimSpace(opts.Checks),
		VerifiedAt:  now,
	}
	if receipt.Version == "" {
		receipt.Version = buildinfo.Current().Version
	}
	if receipt.Commit == "" {
		receipt.Commit = buildinfo.Current().Commit
	}
	if receipt.Checks == "" {
		return sqlitestore.WorkspaceDeploymentReceipt{}, fmt.Errorf("a compact checks summary is required")
	}
	if !opts.Execute {
		return receipt, nil
	}
	embedded := buildinfo.Current()
	if embedded.Commit == "unknown" || strings.HasSuffix(embedded.Version, "-dev") {
		return sqlitestore.WorkspaceDeploymentReceipt{}, fmt.Errorf("production receipt requires an embedded release version and commit")
	}
	if receipt.Version != embedded.Version || receipt.Commit != embedded.Commit {
		return sqlitestore.WorkspaceDeploymentReceipt{}, fmt.Errorf("deployment receipt must match embedded version %s and commit %s", embedded.Version, embedded.Commit)
	}
	if err := store.RecordWorkspaceDeploymentReceipt(ctx, receipt, now); err != nil {
		return sqlitestore.WorkspaceDeploymentReceipt{}, err
	}
	return receipt, nil
}

func AnnounceRelease(ctx context.Context, cfg config.Config, store *sqlitestore.Store, opts ReleaseAnnouncementOptions) (ReleaseAnnouncementResult, error) {
	if store == nil {
		return ReleaseAnnouncementResult{}, fmt.Errorf("store is required")
	}
	repoDir := strings.TrimSpace(opts.RepoDir)
	if repoDir == "" {
		repoDir = "."
	}
	version := strings.TrimSpace(opts.Version)
	if version == "" {
		loaded, err := releaseinfo.LoadTrimmed(filepath.Join(repoDir, "VERSION"))
		if err != nil {
			return ReleaseAnnouncementResult{}, err
		}
		version = loaded
	}
	commit := strings.TrimSpace(opts.Commit)
	if commit == "" {
		commit = buildinfo.Current().Commit
	}
	if !opts.Execute && commit == "unknown" {
		commit = "HEAD"
	}
	base := strings.TrimSpace(opts.BaseCommit)
	if base == "" {
		loaded, err := releaseinfo.LoadTrimmed(filepath.Join(repoDir, "RELEASE_BASE"))
		if err != nil {
			return ReleaseAnnouncementResult{}, err
		}
		base = loaded
	}
	changelog, err := os.ReadFile(filepath.Join(repoDir, "CHANGELOG.md"))
	if err != nil {
		return ReleaseAnnouncementResult{}, err
	}
	items, err := releaseinfo.ChangelogItems(string(changelog), version)
	if err != nil && !opts.Execute {
		items, err = releaseinfo.UnreleasedItems(string(changelog))
	}
	if err != nil {
		return ReleaseAnnouncementResult{}, err
	}
	stats, err := releaseinfo.ComputeGitStats(repoDir, base, commit)
	if err != nil {
		return ReleaseAnnouncementResult{}, err
	}
	text, err := releaseinfo.RenderAnnouncement(version, items, stats)
	if err != nil {
		return ReleaseAnnouncementResult{}, err
	}
	result := ReleaseAnnouncementResult{Text: text, Version: version, Commit: commit}
	if !opts.Execute {
		return result, nil
	}
	if !cfg.WorkspaceConfigured() {
		return ReleaseAnnouncementResult{}, fmt.Errorf("workspace group is not fully configured")
	}
	embedded := buildinfo.Current()
	if embedded.Commit == "unknown" || strings.HasSuffix(embedded.Version, "-dev") {
		return ReleaseAnnouncementResult{}, fmt.Errorf("release announcement requires an embedded release version and commit")
	}
	if version != embedded.Version || commit != embedded.Commit {
		return ReleaseAnnouncementResult{}, fmt.Errorf("release announcement must match embedded version %s and commit %s", embedded.Version, embedded.Commit)
	}
	if err := releaseinfo.VerifyTag(repoDir, "v"+version, commit); err != nil {
		return ReleaseAnnouncementResult{}, err
	}
	environment := valueOrReleaseDefault(opts.Environment, "production")
	if _, ok, err := store.WorkspaceDeploymentReceipt(ctx, environment, version, commit); err != nil {
		return ReleaseAnnouncementResult{}, err
	} else if !ok {
		return ReleaseAnnouncementResult{}, fmt.Errorf("no successful deployment receipt for %s %s at %s", environment, version, commit)
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	announcement, reserved, err := store.ReserveWorkspaceReleaseAnnouncement(ctx, environment, version, commit, now)
	if err != nil {
		return ReleaseAnnouncementResult{}, err
	}
	if !reserved {
		return ReleaseAnnouncementResult{}, fmt.Errorf("release announcement already exists with status %s; reconcile it instead of sending again", announcement.Status)
	}
	message, sendErr := nest.New(cfg.Workspace.BotToken).SendMessageResult(ctx, nest.SendMessageRequest{
		ChatID:          cfg.Workspace.ChatID,
		MessageThreadID: cfg.Workspace.Topics.Inbox,
		Text:            text,
	})
	if sendErr != nil {
		_ = store.MarkWorkspaceReleaseAnnouncementUnknown(context.WithoutCancel(ctx), announcement.ID, sendErr.Error(), time.Now().UTC())
		return ReleaseAnnouncementResult{}, fmt.Errorf("release announcement delivery is unknown and will not be retried automatically: %w", sendErr)
	}
	if err := store.MarkWorkspaceReleaseAnnouncementSent(ctx, announcement.ID, message.MessageID, time.Now().UTC()); err != nil {
		_ = store.MarkWorkspaceReleaseAnnouncementUnknown(context.WithoutCancel(ctx), announcement.ID, err.Error(), time.Now().UTC())
		return ReleaseAnnouncementResult{}, fmt.Errorf("release announcement was sent but could not be recorded; manual reconciliation required: %w", err)
	}
	result.MessageID, result.Sent = message.MessageID, true
	return result, nil
}

func valueOrReleaseDefault(value, fallback string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return fallback
}
