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
go run ./cmd/sova workspace seed-topic-pins --target all --dry-run
go run ./cmd/sova workspace seed-topic-pins --target all
go run ./cmd/sova workspace seed-document-indexes --dry-run
go run ./cmd/sova workspace seed-document-indexes
go run ./cmd/sova workspace cleanup-test-tasks
go run ./cmd/sova workspace cleanup-test-tasks --execute
go run ./cmd/sova workspace serve
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

`seed-topic-pins` sends human-friendly pin draft messages into configured
topics. `--target workspace` covers `InSync v1.0`, `--target control` covers
`Sova.Control`, and `--target all` sends both sets. It does not pin messages
automatically; use the sent messages as the human-reviewable starting point for
topic pins.

`cleanup-test-tasks` is dry-run by default. With `--execute`, it deletes
matching bot-created task cards, removes the delayed-task backlog message when
`--delete-backlog` is true, and marks matching Workspace tasks cancelled in
SQLite. It does not delete user-authored source messages.

`seed-document-indexes` creates or updates the active Stage 6 index messages in
`Заметки`, `Заготовки`, and `Коллекции`. The live bot edits these same messages
after `/note`, `/template`, and `/collection` commands.

`serve` runs the live Workspace bot for `InSync v1.0`. It is separate from
Nest `serve`: it polls `SOVA_WORKSPACE_BOT_TOKEN`, writes live Workspace
messages/clusters/tasks to SQLite, listens for edited messages, handles task
callbacks in `Задачи`, and accepts manual cluster/document commands:

```text
/cluster show
/cluster merge
/cluster split
/cluster attach
/cluster detach
/cluster help
/doc new
/doc append
/doc rename
/doc rename-part
/doc delete-part
/doc delete
/doc publish
/publish
/template new
/template append
/template rename
/template type
/template show
/new collection
/collection add
/collection rename
/collection show
```

Cluster auto-attachment is intentionally narrow. Replies attach explicitly to
the replied message's cluster. Forwarded or media messages attach only when they
immediately follow the user's previous message in the same topic; there is no
wide time-window grouping rule. Manual `merge` and `attach` accept numeric
message IDs and `https://t.me/c/.../.../...` links, including the reply-plus-link
form.

Stage 6 document commands read source messages from their matching Workspace
topic: notes from `Заметки`, templates from `Заготовки`, collection items from
`Коллекции`. The command itself may be sent in that topic or from `Inbox`; a
reply overrides the “latest message” lookup while still preserving Telegram
IDs/links.

```text
/doc new Название
/doc append 3
/doc append Название заметки | Часть 2
/doc rename
/doc rename-part
/doc delete-part
/doc delete
/doc publish
/template new Категория | Документ | Часть
/template append Название шаблона | Название части
/new collection Название
/collection add Название коллекции | Название элемента
```

`/doc show`, `/template show`, and `/collection show` refresh the relevant
index message and also send the human-readable index into the interaction
topic when no ID/title is passed. `/collection show` links to collection
messages when they exist, not only to individual items.

`/doc publish` and reply `/publish` assemble the ordered note parts, send a
preview to `Inbox`, and expose approve/cancel/edit buttons. With empty Gemini
config the publish provider uses a local meaning-preserving mock formatter; the
final approved material is posted to `Полезное`, source-to-derived mappings are
persisted, and a Useful index message is updated.
