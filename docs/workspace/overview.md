# Sova.Workspace Overview

`Sova.Workspace` is the personal `InSync v1.0` branch of Sova. It is separate
from the existing `Sova.Nest` study digest/calendar MVP.

## Boundary

- `Sova.Core`: shared Telegram, SQLite, model, job, config, and formatting code.
- `Sova.Nest`: current study digest and calendar approval behavior.
- `Sova.Workspace`: personal thought workspace, legacy InSync audit, tasks,
  notes, templates, collections, and later migration into `InSync v1.0`.
- `Sova.Control`: service/control supergroup for status, errors, runs, review,
  test lab, Workspace/Nest progress, and ideas.

The Workspace MVP is staged around explicit stop points. Stage 1 is
non-destructive audit/indexing. Stage 2 merges user review decisions into a
migration preview and stops for approval. Bootstrap can create only missing
target forum topics and write a local ID file, but it does not migrate content
or post help messages.

## Target Workspace Topics

`InSync v1.0` starts with these topics:

- `Inbox`
- `Задачи`
- `Заметки`
- `Опыт`
- `Полезное`
- `Заготовки`
- `Коллекции`

The Workspace bot can create missing topics with `workspace bootstrap-topics`
when it is an admin and the MTProto session can resolve the group.

## Target Control Topics

`Sova.Control` starts with:

- `Status`
- `Errors`
- `Runs`
- `Review`
- `Test Lab`
- `Workspace`
- `Nest`
- `Ideas`

The Control bot can create missing topics with `workspace bootstrap-topics`
when it is an admin and the MTProto session can resolve the group.

## Workspace Commands

```bash
go run ./cmd/sova workspace doctor
go run ./cmd/sova workspace discover --dry-run
go run ./cmd/sova workspace discover
go run ./cmd/sova workspace sync-legacy --dry-run
go run ./cmd/sova workspace sync-legacy --full-scan --dry-run
go run ./cmd/sova workspace sync-legacy --full-scan --timeout 5m
go run ./cmd/sova workspace sync-legacy
go run ./cmd/sova workspace audit --dry-run
go run ./cmd/sova workspace audit
go run ./cmd/sova workspace review-preview
go run ./cmd/sova workspace bootstrap-topics --dry-run
go run ./cmd/sova workspace bootstrap-topics
go run ./cmd/sova workspace seed-topic-pins --dry-run
go run ./cmd/sova workspace seed-topic-pins
```

`discover` reads forum topic metadata for the old `InSync` source through the
dedicated MTProto project session. Without `--dry-run`, it stores compact topic
metadata in SQLite.

`sync-legacy` indexes only `SOVA_WORKSPACE_LEGACY_SOURCE`. It does not read
`SOVA_NEST_TELEGRAM_ALLOWED_CHATS` and does not update the Nest recent-content
index. Use it when old InSync has new personal messages after the last
Workspace pass. `--backfill` fetches older messages before the oldest stored
message; `--full-scan` ignores the cursor and rescans the currently visible
legacy history from newest to oldest. `--timeout` bounds the MTProto scan so a
network stall fails with a clear deadline error instead of hanging indefinitely.

`audit` reads already indexed Telegram messages for the configured legacy
source. Without `--dry-run`, it stores compact audit records and writes review
artifacts. It does not post to Telegram.

`review-preview` reads `workspace_review_candidates.csv`, merges filled
`user_decision` values with stored audit records, and writes a migration preview
under `.state/artifacts/workspace/migration_preview/`. It does not publish.

`bootstrap-topics` resolves `InSync v1.0` and `Sova.Control` through the
dedicated MTProto session, reads existing forum topics, creates only missing
target topics through Bot API, and writes an env-style ID file under
`.state/artifacts/workspace/bootstrap/`. It requires network access to Telegram.

`seed-topic-pins` sends the raw first-pass pin text into each configured
`InSync v1.0` topic. It does not pin messages automatically; use the sent
messages as the human-reviewable starting point for future topic pins.
