# Current State

- Repository has a baseline commit and a working Go + SQLite MVP foundation.
- Runtime: local Mac, Go, SQLite, one overview worker in `sova serve`.
- Overview triggers: daily schedule, Nest Chat command/button, and manual CLI.
- Shared overview cooldown: 15 minutes across all triggers.
- Nest topics: Digest, Calendar, Status, Chat. Automated digest/status output
  does not go to Chat.
- Local model: Ollama `qwen3:14b`.
- Telegram auth: dedicated MTProto project session only.
- Telegram sync verified end-to-end for two allowlisted sources: dry-run writes
  nothing, sync stores 200 messages, repeat sync dedupes to zero new messages,
  media metadata and one service message are handled.
- `sova run --trigger manual` now calls Telegram sync and completes successfully
  when there are no new messages.
- Qwen classification, compact run bundle generation, Codex digest generation,
  and Nest Digest publication are wired for the next run that has new text
  messages.
- `sova serve` uses Bot API long polling for `/run`, a "Создать обзор" button,
  and the local daily scheduler.
- Compact indexes exist for Telegram recent content, overview runs, and calendar
  setup state under `.state/index/`.
- Google Calendar real event creation is still blocked on OAuth Desktop
  credentials and target calendar ID.
