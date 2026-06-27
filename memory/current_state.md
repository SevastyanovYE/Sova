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
  and Nest Digest publication are wired. Qwen runs with compact output,
  `think:false`, bounded batch/stage budgets, per-batch decision persistence,
  conservative timeout/error fallback, and compact performance metrics. The
  production classification batch target is 16 messages after `qwen3:14b`
  timed out at larger batch sizes.
- `sova serve` uses Bot API long polling for `/run`, an existing "Создать
  обзор" button, Status topic progress updates, Calendar date-edit callbacks,
  and the local daily scheduler. Control/button messages are created explicitly
  through `nest-seed-topics`, `/button`, `/start`, or `/help`; `serve` no longer
  sends a new Chat button on every startup. Short service messages in
  Chat/Calendar/Status use Telegram HTML formatting; final digests stay plain
  text. Polling uses IPv4 and bounded exponential backoff for temporary Telegram
  network failures.
- Codex CLI discovery supports both `PATH` and the standard macOS Codex app
  location. A Codex failure degrades to a fallback digest instead of losing
  synced messages; `sova retry-run --id` can recover older Codex/Qwen failures.
- Nest digests use Telegram-friendly plain text with compact headings, bullets,
  direct provenance URLs, and at most two relevant emoji.
- Overview run 5 was recovered from 42 stored messages and published
  successfully after its original empty Qwen response.
- Compact indexes exist for Telegram recent content, overview runs, and calendar
  setup state under `.state/index/`. Qwen model-call metrics are indexed at
  `.state/index/qwen-performance.md`; model comparison summaries are written to
  `.state/index/qwen-benchmark.md` and labeled eval summaries to
  `.state/index/qwen-eval.md`.
- Qwen runtime remains `qwen3:14b` for MVP close. A labeled 100-message eval on
  2026-06-27 showed `qwen3:8b` is the best next candidate after prompt/event
  threshold tuning; `qwen3:4b`, `gemma3:4b`, and `llama3.2:3b` are not suitable
  for this MVP pipeline.
- Google Calendar approval flow is implemented: event-like messages become
  Calendar topic candidates with approve/reject/date-edit buttons, and approve
  creates a real Google Calendar event with 7d/3d/1d/1h reminders after
  browser-based `sova google-login` with a temporary localhost OAuth callback.
- The target Google Calendar ID, OAuth Desktop credentials, and local Google
  OAuth token are configured per user report after successful
  `sova google-login`.
