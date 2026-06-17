# Current State

- Repository foundation is being implemented.
- Runtime: local Mac, Go, SQLite, one worker.
- Overview triggers: daily schedule, Nest button, and manual CLI.
- Shared overview cooldown: 15 minutes.
- Nest topics: Digest, Calendar, Status, Chat.
- Local model: Ollama `qwen3:14b`.
- Telegram auth: dedicated MTProto project session only.
- Google Calendar: real event creation after Nest approval.
- Nest Bot API verified: bot token works and four topics are configured.
- Qwen smoke test verified: `qwen3:14b` returns valid classification JSON.
- Telegram MTProto project session is created; next step is `sova sync --dry-run`
  and then `sova sync` for allowlisted sources.
