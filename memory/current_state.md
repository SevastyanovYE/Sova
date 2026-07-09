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
- On 2026-07-07, `workspace cleanup-test-tasks --execute` deleted 14
  bot-created test task cards (`44`, `46`, `47`, `50`..`60`) and the delayed
  task backlog message `48`; 10 non-terminal matching tasks were marked
  `cancelled` in SQLite. User-authored source messages were intentionally not
  deleted.
- On 2026-07-07, `workspace seed-topic-pins --target all` sent human-friendly
  pin draft messages into the real `InSync v1.0` and `Sova.Control` topics.
  Workspace message IDs: Inbox `115`, `Задачи` `116`, `Заметки` `117`, `Опыт`
  `118`, `Полезное` `119`, `Заготовки` `120`, `Коллекции` `121`, cluster help
  in Inbox `122`. Control message IDs: Status `38`, Errors `39`, Runs `40`,
  Review `41`, Test Lab `42`, Workspace `43`, Nest `44`, Ideas `45`. The
  command still does not pin messages automatically.
- Later on 2026-07-07, the pin draft text was restyled to remove the old
  `Закреп:` prefix and add emoji headings. New Workspace message IDs:
  Inbox `141`, `Задачи` `142`, `Заметки` `143`, `Опыт` `144`, `Полезное`
  `145`, `Заготовки` `146`, `Коллекции` `147`, cluster help in Inbox `148`.
  New Control message IDs: Status `46`, Errors `47`, Runs `48`, Review `49`,
  Test Lab `50`, Workspace `51`, Nest `52`, Ideas `53`.
- Stage 6 document index seed exists. `workspace seed-document-indexes` created
  active index messages: `Заметки` `149`, `Заготовки` `150`, `Коллекции` `151`.
  These messages are tracked in `workspace_topic_indexes` and are edited by
  live `/note`, `/template`, and `/collection` commands.
- Workspace live bot foundation is implemented separately from Nest:
  `workspace serve` polls only `SOVA_WORKSPACE_BOT_TOKEN`, stores compact
  live message metadata, supports logical clusters, handles edited messages,
  and leaves Nest `sova serve` unchanged.
- Workspace cluster MVP storage exists in SQLite (`workspace_messages`,
  `workspace_clusters`, `workspace_cluster_messages`) with source IDs/links,
  ordered parts, reply attachment, narrow immediate forwarded/media attachment,
  and manual `/cluster show|merge|split|attach|detach|help` commands. Manual
  `merge` and `attach` accept numeric message IDs and `https://t.me/c/...`
  links, including reply-plus-link forms.
- Workspace task MVP foundation exists: `#task` and `#tasks` create separate
  bot task cards in `Задачи` with Done/Cancel/Defer buttons, random-ish
  pleasant emoji, no visible source link, same-topic custom defer date input,
  no-year and explicit-year date parsing, deferred-only backlog index tracking,
  links from backlog entries back to original task cards, buttons retained after
  deferring, broader emoji rotation, paced card sends for multi-task input, and
  edit-sync updates for open/deferred source tasks. `На неделю` and `На месяц`
  defer presets now use 08:00 in the configured project timezone.
- Stage 6 manual document foundation exists: `workspace_documents` and
  `workspace_document_parts` store notes, templates, and collection items with
  source IDs/links plus optional target message IDs. Live Workspace bot supports
  `/doc new|append|rename|rename-part|delete-part|delete|publish|show`,
  reply `/publish`, `/template new|append|rename|type|show`, `/new collection`,
  and `/collection add|rename|show`. Commands may be sent from the matching
  topic or Inbox; source messages are read from the matching topic (`Заметки`,
  `Заготовки`, `Коллекции`) unless a valid reply is provided. Note/template
  append resolves by ID or exact title and asks for clarification when a title
  is ambiguous.
- Note indexes render the first note line as a bold clickable title with a
  stable pleasant emoji; later note parts render as bracketed links. Collection
  indexes prefer the bot-created collection-card message link over the first
  item link.
- Note publish MVP exists: `/doc publish` or reply `/publish` assembles ordered
  note parts, uses a provider boundary, falls back to a local meaning-preserving
  mock formatter when Gemini config is empty, sends preview messages to Inbox,
  and supports approve/cancel/edit buttons. Approve posts final material to
  `Полезное`, persists source-to-derived published mappings, updates document
  target IDs, and updates a Useful index message. Repeat publish warns when a
  note already has a target unless `force` is passed.
- Repeated edits of bot-maintained indexes/backlogs treat Telegram
  `message is not modified` as a no-op success instead of creating duplicate
  index messages. This was verified for document indexes `149`, `150`, `151`
  with `workspace seed-document-indexes`, which returned `unchanged`.
- Workspace derived message mappings exist in SQLite so source edits can update
  active task cards and mark already published derived messages as
  `needs_review` instead of silently rewriting final material.
- Stage 6 template types are now first-class rows in
  `workspace_document_types`. `/template type <name>` creates an empty active
  type, `/template new <title>` asks for the type, `/template move` changes the
  type by buttons/text, and the `Заготовки` index renders type headings plus
  bold prompt links. `Остальные` is normalized to `Остальное`.
- Stage 6 document commands have reverse `/new` aliases for notes, templates,
  and collections (`/new doc`, `/new template`, `/new collection`) while the
  direct forms still work. `workspace seed-command-help` sends current command
  reference messages into each Workspace topic.
- Stage 6 collection indexes now render as one flat list of collection-card
  links. Each collection remains its own bot message with description, ordered
  item links, and edit/delete/reorder/move commands for items.
- Published notes leave the active Notes index after approve. Later edits to a
  published note source mark the Workspace document and derived published rows
  `needs_review`; the Useful index shows that review marker.
- On 2026-07-09, `workspace seed-command-help` sent current command reference
  messages into real `InSync v1.0` topics. Message IDs: Inbox `356`,
  `Задачи` `357`, `Заметки` `358`, `Опыт` `359`, `Полезное` `360`,
  `Заготовки` `361`, `Коллекции` `362`.
