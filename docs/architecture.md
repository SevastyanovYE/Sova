# MVP Architecture

`sova serve-all` is the memory-conscious production entry point. It runs the
Nest and Workspace long-polling controllers in one foreground process while
keeping their existing Bot API clients and SQLite handles. If either controller
stops unexpectedly, the shared context cancels the other and the service exits
for its supervisor to restart. `sova serve` and `sova workspace serve` remain
available as separate compatibility entry points.

```text
alwaysdata Service / local supervisor
  -> sova serve-all
       -> Nest Bot API controller
       -> daily scheduler
       -> Status progress updater
       -> SQLite job/run state
       -> single overview worker
            -> MTProto incremental sync
            -> media metadata/placeholders
            -> bounded Google API classification/event route
            -> compact model-call metrics
            -> compact run bundle
            -> Gemini structured digest
            -> Nest publication
            -> calendar approval
            -> Google Calendar API
       -> Workspace Bot API controller
            -> reminders, documents, publish, optional search
```

The controller and worker share a 15-minute run cooldown. Telegram Desktop
sessions are outside the architecture.

The alwaysdata launcher uses a statically linked `linux/amd64` binary, embedded
timezone data, `SIGHUP`/`SIGTERM`/`SIGINT` cancellation, a fresh heartbeat, and
a read-only SQLite `quick_check`. No production serve mode starts Ollama, a
local model, Codex CLI, Docker, systemd, or a GUI.

The daily scheduler is enabled by default for compatibility. Its on/off state
is stored in SQLite and can be changed from the Nest `Status` topic with
`/daily on|off|status`. The scheduler reads the durable state before enqueueing
each daily job and fails closed if that state cannot be read. This switch does
not affect manual CLI runs, `/run`, or the pinned `Chat` button.

## Workspace branch

`Sova.Workspace` is a separate branch of product logic for the personal
`InSync v1.0` workspace. Stage 1 adds only non-destructive legacy audit support:
Workspace config, forum-topic discovery, SQLite `workspace_*` tables, and review
artifacts. It does not post, migrate, delete, or edit Telegram messages.

Shared Telegram/SQLite/config code remains in core packages; Nest commands,
topics, cooldowns, digest publishing, and Calendar approval behavior remain
owned by `Sova.Nest`.
