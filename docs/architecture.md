# MVP Architecture

`sova serve` is a lightweight local controller. It keeps Bot API long polling
active, enqueue the daily overview, accepts text commands from the Nest `Status`
service topic, and accepts the pinned run button from the Nest `Chat` study
topic. One worker executes overview jobs serially.

```text
launchd
  -> sova serve
       -> Bot API controller
       -> daily scheduler
       -> Status progress updater
       -> SQLite job/run state
       -> single overview worker
            -> MTProto incremental sync
            -> media extractors
            -> qwen3:14b bounded structured classification
            -> compact model-call metrics
            -> compact run bundle
            -> codex exec structured digest
            -> Nest publication
            -> calendar approval
            -> Google Calendar API
```

The controller and worker share a 15-minute run cooldown. Telegram Desktop
sessions are outside the architecture.

## Workspace branch

`Sova.Workspace` is a separate branch of product logic for the personal
`InSync v1.0` workspace. Stage 1 adds only non-destructive legacy audit support:
Workspace config, forum-topic discovery, SQLite `workspace_*` tables, and review
artifacts. It does not post, migrate, delete, or edit Telegram messages.

Shared Telegram/SQLite/config code remains in core packages; Nest commands,
topics, cooldowns, digest publishing, and Calendar approval behavior remain
owned by `Sova.Nest`.
