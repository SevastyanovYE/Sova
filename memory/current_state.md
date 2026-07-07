# Current State

- Repository has a baseline commit and a working Go + SQLite MVP foundation.
- Runtime: local Mac, Go, SQLite, one overview worker in `sova serve`.
- Overview triggers: daily schedule, Nest service commands, pinned Chat button,
  and manual CLI.
- Shared overview cooldown: 15 minutes across all triggers.
- Nest topics: Digest, Calendar, Status, Chat. Automated digest/status output
  does not go to Chat.
- Sova Nest overview sync reads only `SOVA_NEST_TELEGRAM_ALLOWED_CHATS`.
  Workspace/personal Telegram sources stay in `SOVA_WORKSPACE_*` config and are
  not part of the study digest allowlist.
- Local model: Ollama `qwen3:14b`.
- Telegram auth: dedicated MTProto project session only.
- Telegram sync verified end-to-end for two Sova Nest study sources: dry-run
  writes nothing, sync stores 200 messages, repeat sync dedupes to zero new
  messages, media metadata and one service message are handled.
- `sova run --trigger manual` now calls Telegram sync and completes successfully
  when there are no new messages.
- Qwen classification, compact run bundle generation, Codex digest generation,
  and Nest Digest publication are wired. Qwen runs with compact output,
  `think:false`, bounded batch/stage budgets, per-batch decision persistence,
  conservative timeout/error fallback, and compact performance metrics. The
  production classification batch target is 16 messages after `qwen3:14b`
  timed out at larger batch sizes.
- `sova serve` uses Bot API long polling for text commands in the Status service
  topic, an existing "Создать обзор" button in the Chat study topic, Status
  progress updates, Calendar date-edit callbacks, and the local daily scheduler.
  Control/button messages are created explicitly through `nest-seed-topics`,
  `/button`, `/start`, or `/help`; `serve` no longer sends a new Chat button on
  every startup. Short service messages in Calendar/Status use Telegram HTML
  formatting; final digests stay plain text. Bot API polling uses the default
  TCP dialer with bounded exponential backoff for temporary Telegram network
  failures.
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
- The main README uses generated PNG diagrams under `docs/assets/readme/` and
  Russian sentence-case headings. Follow-up conclusions for this text MVP branch
  are captured in `docs/text_mvp_followups.md`.
- Sova.Workspace Stage 1 foundation exists separately from Sova.Nest:
  Workspace/Control config keys, `workspace doctor`, read-only forum-topic
  discovery, heuristic legacy audit, SQLite `workspace_*` tables, and review
  artifacts under `.state/artifacts/workspace/audit/<run-id>/`.
- Workspace Stage 1 is non-destructive: it does not migrate, mass post, delete,
  or edit Telegram messages. The audit currently uses a deterministic heuristic
  fallback and marks uncertain material for manual review.
- Sova.Workspace Stage 2 review integration is implemented:
  `workspace review-preview` reads the user-filled review CSV, merges decisions
  with stored audit records, writes compact migration preview Markdown/CSV under
  `.state/artifacts/workspace/migration_preview/`, and stops for approval.
- Workspace/Control topic bootstrap is implemented:
  `workspace bootstrap-topics` resolves `InSync v1.0` and `Sova.Control` via
  the dedicated MTProto session, checks bot access, creates only missing target
  forum topics, and writes numeric IDs to
  `.state/artifacts/workspace/bootstrap/workspace_control_topic_ids.env`.
  Creation was not completed in the current Codex sandbox because DNS lookup for
  `api.telegram.org` failed and MTProto calls timed out.
- Sova.Workspace legacy sync is separated from Sova.Nest sync:
  `workspace sync-legacy` indexes only `SOVA_WORKSPACE_LEGACY_SOURCE` and does
  not update the Nest `.state/index/telegram-recent.md`. The Nest recent index
  is filtered to Nest study source refs from `SOVA_NEST_TELEGRAM_ALLOWED_CHATS`.
- Workspace audit run 3 processed the currently indexed old InSync batch
  (1016 messages, including new messages added after the previous pass) with
  tighter user rules: media stays review, punctuation-only placeholders go to
  trash, old `Задачи`/`Заготовки` are mostly archived, latest 10 candidates are
  auto-take, and Control-review card/topic-pin drafts are written under
  `.state/artifacts/workspace/audit/20260703T204807Z/`. Its migration preview
  at `.state/artifacts/workspace/migration_preview/20260703T204818Z/` was
  superseded by run 4 after the user filled the review table.
- Workspace audit run 4 reprocessed the indexed 1016-message old InSync batch
  after the user filled the review table. User-filled decisions were normalized
  into `.state/artifacts/workspace/user_review/workspace_review_candidates_run4_filled.csv`.
  Review preview for audit 4 has `pending=0`, `migration=95`, and
  `external_routes=85`; latest preview artifacts are under
  `.state/artifacts/workspace/migration_preview/20260704T203209Z/`.
- Workspace audit tag rules now force migration before legacy topic reduction:
  `#мюсли`, `#идеи`, and `#связи` go to `Заметки`, `#опыт` to `Опыт`,
  `#знания` to `Полезное`, and `#поэзия`/`#аниме` to `Коллекции`.
- Workspace legacy full scan was completed after network/sandbox restrictions
  were lifted. SQLite now contains 2324 visible old InSync messages
  (`1..2437`), including the 1308 older messages that were missing after run 4.
  Telegram returned `FLOOD_WAIT (18)` once after a large dry-run; waiting and
  retrying completed the actual sync.
- Workspace audit run 5 processed the remaining older 1308-message batch and
  wrote review artifacts under
  `.state/artifacts/workspace/audit/20260704T204004Z/`. It has 374 review
  candidates. Its initial Stage 2 preview is under
  `.state/artifacts/workspace/migration_preview/20260704T204044Z/` with
  `migration=71`, `external_routes=44`, and `pending=374`; the review table
  should be filled before publication.
- `workspace bootstrap-topics --dry-run --timeout 2m` verified that all target
  `InSync v1.0` topics and all `Sova.Control` topics already exist.
- On 2026-07-07, `workspace sync-legacy --limit 300` inserted 8 newer old
  InSync messages (`2438..2445`). SQLite now contains 2332 visible old InSync
  messages (`1..2445`).
- Workspace audit run 6 processed all 2332 currently indexed old InSync
  messages. User decisions were merged from run 4, the filled run 5 Numbers
  table, and the 8 new messages were forced to `take` per user instruction.
  The merged CSV is
  `.state/artifacts/workspace/user_review/workspace_review_candidates_run6_filled.csv`.
  Preview artifacts are under
  `.state/artifacts/workspace/migration_preview/20260707T081735Z/` with
  `migration=170`, `external_routes=137`, and `pending=0`.
- `workspace seed-topic-pins` was added and used to send raw first-pass pin
  messages into the real `InSync v1.0` topics. Sent message IDs:
  Inbox `29`, `Задачи` `30`, `Заметки` `31`, `Опыт` `32`, `Полезное` `33`,
  `Заготовки` `34`, `Коллекции` `35`. The command does not pin messages
  automatically.
