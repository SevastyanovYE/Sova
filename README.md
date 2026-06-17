# Sova

Sova is a local-first study information pipeline:

```text
Telegram -> extractors -> Qwen -> Codex digest -> Sova Nest -> approval -> Google Calendar
```

The current repository contains the MVP foundation: project context rules,
SQLite runtime state, a shared overview cooldown, and configuration for the
four Nest topics.

## Requirements

- Go 1.25+
- SQLite
- Codex CLI
- ffmpeg
- Tesseract with `rus` and `eng`
- Ollama with `qwen3:14b` (required by the processing milestone)

## Quick start

```bash
cp .env.example .env
go mod download
go run ./cmd/sova init
go run ./cmd/sova doctor
go run ./cmd/sova telegram-status
go run ./cmd/sova sync --dry-run
```

Test the shared cooldown:

```bash
go run ./cmd/sova run --trigger manual
go run ./cmd/sova run --trigger manual
```

The second command must be rejected until 15 minutes have elapsed.

After `telegram-status` reports an authorized session, `sync` reads only
`SOVA_TELEGRAM_ALLOWED_CHATS`, appends new raw records under
`.state/raw/telegram/`, persists message indexes in SQLite, and regenerates
`.state/index/telegram-recent.md`.

Run the local controller:

```bash
go run ./cmd/sova serve
```

`serve` keeps Bot API long polling active, accepts `/run` and the "Создать обзор"
button only from the Nest `Chat` topic, and schedules the daily overview at
`SOVA_DAILY_RUN_TIME`. Overview output goes to the `Digest` topic; failures and
health/status messages go to the `Status` topic.

Rebuild compact indexes without running the pipeline:

```bash
go run ./cmd/sova index
```

Authorize Google Calendar before approving event candidates:

```bash
go run ./cmd/sova google-login
```

Overview runs publish event candidates to the Nest `Calendar` topic with
Approve/Reject buttons. Approve creates a Google Calendar event only after this
local OAuth token exists. Events use custom reminders: 7 days, 3 days, 1 day,
and 1 hour.

## Data boundary

Private runtime data is stored under `.state/` and `.sessions/`; both are
ignored by git. See `docs/data_map.md` before opening any large data source.
