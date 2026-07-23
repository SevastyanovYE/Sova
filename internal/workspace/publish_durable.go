package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/SevastyanovYE/Sova/internal/config"
	"github.com/SevastyanovYE/Sova/internal/googleai"
	"github.com/SevastyanovYE/Sova/internal/nest"
	sqlitestore "github.com/SevastyanovYE/Sova/internal/storage/sqlite"
)

type publishMessageTelegram interface {
	SendMessageResult(context.Context, nest.SendMessageRequest) (nest.Message, error)
}

// sendDurablePublishMessage implements the outbox hand-off. Only pending is
// safe to send. The sending marker is committed first; a crash or ambiguous
// transport response leaves sending/unknown, which this function never resends.
func sendDurablePublishMessage(ctx context.Context, store *sqlitestore.Store, client publishMessageTelegram, item sqlitestore.WorkspacePublishMessage, request nest.SendMessageRequest, now time.Time) (nest.Message, bool, error) {
	if item.Status != "pending" {
		return nest.Message{}, false, nil
	}
	if err := store.MarkWorkspacePublishMessageSending(ctx, item.ID, request.ChatID, request.MessageThreadID, now); err != nil {
		return nest.Message{}, false, err
	}
	message, err := client.SendMessageResult(ctx, request)
	if err != nil {
		telemetryErr := publishTelemetryError(err)
		persistCtx, cancel := workspaceTaskReminderPersistenceContext(ctx)
		defer cancel()
		if workspaceTaskReminderSendIsAmbiguous(err) {
			if persistErr := store.MarkWorkspacePublishMessageUnknown(persistCtx, item.ID, telemetryErr, now); persistErr != nil {
				return nest.Message{}, false, fmt.Errorf("publish send ambiguous; persist unknown: %w", persistErr)
			}
		} else if persistErr := store.MarkWorkspacePublishMessagePending(persistCtx, item.ID, telemetryErr, now); persistErr != nil {
			return nest.Message{}, false, fmt.Errorf("publish send rejected; restore pending: %w", persistErr)
		}
		return nest.Message{}, false, errors.New(telemetryErr)
	}
	persistCtx, cancel := workspaceTaskReminderPersistenceContext(ctx)
	defer cancel()
	if err := store.MarkWorkspacePublishMessageSent(persistCtx, item.ID, message.MessageID, now); err != nil {
		// The row intentionally stays in sending. Telegram accepted the request,
		// so another attempt could duplicate the visible material.
		return nest.Message{}, false, fmt.Errorf("Telegram accepted publish message %d but its id was not persisted: %w", message.MessageID, err)
	}
	return message, true, nil
}

// recoverInterruptedWorkspacePublishRuns handles the two pre-approval stages
// before normal publishing recovery runs. Model generation is safe to repeat
// because it has no Telegram side effect. A persisted pending preview send is
// also safe; a persisted sending marker is not, and is therefore made unknown
// and fails the preview without an automatic resend.
func recoverInterruptedWorkspacePublishRuns(ctx context.Context, cfg config.Config, store *sqlitestore.Store, client publishMessageTelegram, now time.Time) error {
	runs, err := store.InterruptedWorkspacePublishRuns(ctx, 100)
	if err != nil {
		return err
	}
	var failures []string
	for _, initial := range runs {
		if err := recoverInterruptedWorkspacePublishRun(ctx, cfg, store, client, initial, now); err != nil {
			failures = append(failures, fmt.Sprintf("run %d: %s", initial.ID, compactWorkspaceLine(err.Error(), 160)))
		}
	}
	_ = writeWorkspacePublishRunsIndex(context.WithoutCancel(ctx), cfg, store)
	if len(failures) > 0 {
		return fmt.Errorf("%s", strings.Join(failures, "; "))
	}
	return nil
}

func recoverInterruptedWorkspacePublishRun(ctx context.Context, cfg config.Config, store *sqlitestore.Store, client publishMessageTelegram, run sqlitestore.WorkspacePublishRun, now time.Time) error {
	if run.Status == "generating" {
		doc, err := store.WorkspaceDocumentByID(ctx, run.DocumentID)
		if err != nil {
			_ = store.FailWorkspacePublishRun(context.WithoutCancel(ctx), run.ID, publishTelemetryError(err), now)
			return err
		}
		parts, err := store.WorkspaceDocumentParts(ctx, run.DocumentID)
		if err != nil {
			_ = store.FailWorkspacePublishRun(context.WithoutCancel(ctx), run.ID, publishTelemetryError(err), now)
			return err
		}
		if len(parts) == 0 {
			err := fmt.Errorf("note has no parts")
			_ = store.FailWorkspacePublishRun(context.WithoutCancel(ctx), run.ID, publishTelemetryError(err), now)
			return err
		}
		result, err := NewNotePublishProvider(cfg).FormatNote(ctx, NotePublishRequest{
			Title: doc.Title, Parts: parts, Revision: run.Revision,
		})
		if err != nil {
			_ = store.FailWorkspacePublishRun(context.WithoutCancel(ctx), run.ID, publishTelemetryError(err), now)
			return err
		}
		messages, err := preparePublishPreviewMessages(result.Messages, run.Revision)
		if err != nil {
			_ = store.FailWorkspacePublishRun(context.WithoutCancel(ctx), run.ID, publishTelemetryError(err), now)
			return err
		}
		if err := store.SetWorkspacePublishPreview(ctx, run.ID, result.Model, messages, now); err != nil {
			_ = store.FailWorkspacePublishRun(context.WithoutCancel(ctx), run.ID, publishTelemetryError(err), now)
			return err
		}
		if err := store.SetWorkspacePublishRouteSummary(ctx, run.ID, result.RouteSummary, now); err != nil {
			_ = store.FailWorkspacePublishRun(context.WithoutCancel(ctx), run.ID, publishTelemetryError(err), now)
			return err
		}
		run, err = store.WorkspacePublishRunByID(ctx, run.ID)
		if err != nil {
			return err
		}
	}
	if run.Status != "preview_sending" {
		return fmt.Errorf("workspace publish run %d cannot recover from %s", run.ID, run.Status)
	}
	interrupted, err := store.MarkInterruptedWorkspacePublishSendsUnknown(ctx, run.ID, now)
	if err != nil {
		return err
	}
	run, err = store.WorkspacePublishRunByID(ctx, run.ID)
	if err != nil {
		return err
	}
	if interrupted > 0 || workspacePublishMessagesContainStatus(run.Messages, "unknown") {
		err := fmt.Errorf("preview delivery is ambiguous after restart; automatic resend is disabled")
		_ = store.FailWorkspacePublishRun(context.WithoutCancel(ctx), run.ID, publishTelemetryError(err), now)
		return err
	}
	for index, item := range run.Messages {
		if item.Kind != "preview" || item.Status != "pending" {
			continue
		}
		displayText, err := publishPreviewDisplayText(item.Text, run.Revision, index == len(run.Messages)-1)
		if err != nil {
			_ = store.FailWorkspacePublishRun(context.WithoutCancel(ctx), run.ID, publishTelemetryError(err), now)
			return err
		}
		request := nest.SendMessageRequest{
			ChatID: run.PreviewChatID, MessageThreadID: run.PreviewTopicID,
			Text: displayText, ParseMode: "HTML",
		}
		if index == len(run.Messages)-1 {
			request.ReplyMarkup = PublishPreviewMarkup(run.DocumentID)
		}
		if _, _, err := sendDurablePublishMessage(ctx, store, client, item, request, now); err != nil {
			_ = store.FailWorkspacePublishRun(context.WithoutCancel(ctx), run.ID, publishTelemetryError(err), now)
			return err
		}
	}
	if _, err := store.ActivateWorkspacePublishPreview(ctx, run.ID, now); err != nil {
		_ = store.FailWorkspacePublishRun(context.WithoutCancel(ctx), run.ID, publishTelemetryError(err), now)
		return err
	}
	return nil
}

func workspacePublishMessagesContainStatus(messages []sqlitestore.WorkspacePublishMessage, status string) bool {
	for _, message := range messages {
		if message.Kind == "preview" && message.Status == status {
			return true
		}
	}
	return false
}

func writeWorkspacePublishRunsIndex(ctx context.Context, cfg config.Config, store *sqlitestore.Store) error {
	if strings.TrimSpace(cfg.StateDir) == "" {
		return nil
	}
	summaries, err := store.WorkspacePublishRunSummaries(ctx, 50)
	if err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("# Workspace publish runs\n\n")
	b.WriteString("Compact operational state only. Preview, revision, prompt, source text, and raw provider responses are intentionally omitted.\n\n")
	b.WriteString("| Run | Document | Stage | Model | Preview | Final | Blocked | Updated | Error |\n")
	b.WriteString("| ---: | ---: | --- | --- | ---: | ---: | ---: | --- | --- |\n")
	for _, summary := range summaries {
		fmt.Fprintf(&b, "| %d | %d | %s | %s | %d | %d | %d | %s | %s |\n",
			summary.ID, summary.DocumentID, workspaceIndexCell(summary.Status), workspaceIndexCell(summary.Model),
			summary.PreviewSent, summary.FinalSent, summary.Blocked,
			summary.UpdatedAt.UTC().Format(time.RFC3339), workspaceIndexCell(strings.TrimSpace(summary.RouteSummary+" "+summary.LastError)))
	}
	dir := filepath.Join(cfg.StateDir, "index")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, "workspace-runs-*.md")
	if err != nil {
		return err
	}
	tmp := file.Name()
	defer os.Remove(tmp)
	if _, err := file.WriteString(b.String()); err != nil {
		file.Close()
		return err
	}
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "workspace-runs.md"))
}

func workspaceIndexCell(value string) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	value = strings.ReplaceAll(value, "|", "/")
	if len([]rune(value)) > 160 {
		value = string([]rune(value)[:160])
	}
	return value
}

// publishTelemetryError keeps the compact index useful without copying a
// provider body, prompt, source fragment, endpoint (which may contain a bot
// token), or other raw content into durable operational telemetry.
func publishTelemetryError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "operation cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "operation deadline exceeded"
	}
	var apiErr *googleai.APIError
	if errors.As(err, &apiErr) {
		parts := []string{"Google publish request failed"}
		if strings.TrimSpace(apiErr.Model) != "" {
			parts = append(parts, "model="+workspaceIndexCell(apiErr.Model))
		}
		if apiErr.StatusCode != 0 {
			parts = append(parts, fmt.Sprintf("HTTP=%d", apiErr.StatusCode))
		}
		if strings.TrimSpace(apiErr.Status) != "" {
			parts = append(parts, "status="+workspaceIndexCell(apiErr.Status))
		}
		return strings.Join(parts, " ")
	}
	lower := strings.ToLower(err.Error())
	switch {
	case strings.Contains(lower, "bot api sendmessage failed:"):
		return "Telegram rejected publish message"
	case strings.Contains(lower, "decode gemini"), strings.Contains(lower, "publish json"):
		return "Google publish response was not valid structured JSON"
	case strings.Contains(lower, "source_part_id"), strings.Contains(lower, "source parts"):
		return "Google publish response failed source coverage validation"
	case strings.Contains(lower, "publish html"), strings.Contains(lower, "unsupported telegram html"):
		return "Google publish response failed Telegram HTML validation"
	case strings.Contains(lower, "final messages require manual reconciliation"):
		return workspaceIndexCell(err.Error())
	case strings.Contains(lower, "database"), strings.Contains(lower, "sqlite"), strings.Contains(lower, "constraint"):
		return "publish persistence failed"
	default:
		return "publish operation failed; inspect live service diagnostics"
	}
}
