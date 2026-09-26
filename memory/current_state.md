# Current State

- Sova 0.3.1 commit `9b2fe4a4` was deployed to GCP production on
  2026-09-26. The September fixes cover digest lead deduplication, per-group
  template numbering, actionable Useful deletion failures with one-attempt
  confirmations, atomic Calendar token persistence, and compact three-topic
  Control. The release passed the exact artifact checksum, strict doctors,
  model smoke, systemd/journal checks, and an independent post-deploy
  healthcheck plus SQLite `quick_check`. Production reports `active/running`,
  zero restarts, and no error-level journal entries since deployment. The
  rollback inputs are
  `/var/backups/sova/sova-before-0.3.1-20260926T090545Z.sqlite` and
  `/var/backups/sova/sova-binary-before-0.3.1-20260926T090545Z`.
  Read-only Telegram inspection confirmed `invalid_grant` on Calendar candidate #18
  on September 16; Google Auth Platform still shows External / Testing for the
  Sova Calendar project. Reauthorization and production token installation remain
  outstanding. Full incident evidence: `docs/september_2026_fixes.md`.

- Production moved to a Google Cloud Compute Engine `e2-micro` in
  `us-central1-a` on 2026-08-07. It uses Debian 12, a 10 GB `pd-standard` disk,
  a custom dual-stack subnet with no external IPv4, external IPv6 restricted
  to the administrator's `/128` for SSH, no VM service account, no scheduled
  snapshots, no Ops Agent, and one enabled `sova.service` running
  `sova serve-all`. The unit reports active, one process, zero restarts, a
  healthy heartbeat, and about 23 MB current RSS after cutover.
- The final old-host SQLite snapshot passed SHA-256, `quick_check`, WAL
  checkpoint `0|0|0`, strict doctors, semantic row-count comparison, and real
  Gemini smoke after restore on GCP. A separate post-cutover application backup
  was downloaded to local `.state/migration`, checksum-verified, and
  restore-tested. After user acceptance, the old VPS runtime, data, backups,
  service units, migration temporaries, and dedicated `sova` system account
  were removed. The original server-access ZIP and the verified local migration
  backups remain available off-server.
- The daily overview setting survived the move as
  `daily_overview_enabled=false`. `/daily on|off|status` remains available in
  the Nest `Status` topic, while `/run` and the Chat button remain independent.
- Sova 0.3.1 commit `9b2fe4a4` is active through the single GCP systemd unit.
  GCP keeps checksum-verified SQLite and previous-binary backups under
  `/var/backups/sova`; obsolete staging directories and duplicate server-side
  migration backups were removed.
- The alwaysdata staging tree and the dedicated temporary migration SSH key were
  removed after GCP acceptance. Its paused Service registration still needs to
  be deleted from the alwaysdata administration panel because the browser
  session expired during cleanup; it cannot start because its command target no
  longer exists.
- alwaysdata support replied that they were "pretty confident" their storage
  supports the requested SQLite locks, atomic rename, and `fsync`. Offline
  doctors and the restore/semantic-count checks passed, but Gemini model smoke
  returned HTTP 403 from both the alwaysdata SSH host and a temporary real
  Service host. The same key/models pass from the old US VPS and the new GCP
  VM, so alwaysdata outbound Gemini access remains unsuitable. The temporary
  test Service and its files were removed after the failure.
- The 0.2.0 migration build adds a static `linux/amd64` package, one foreground
  `serve-all` process, `SIGHUP`/`SIGTERM`/`SIGINT` shutdown, heartbeat plus
  read-only DB healthcheck, `DELETE` journal-mode configuration, and verified
  backup/restore tooling. GCP passed the real service-host model smoke and
  resource measurement before production activation.
- Production additive migrations and SQLite `quick_check` succeeded. A verified
  post-cutover SQLite backup remains under `/var/backups/sova/`; the production
  binary and protected environment remain under `/opt/sova/` and `/etc/sova/`.
- Existing Workspace command-help messages were registered and edited in place:
  Inbox `497`, Tasks `498`, Notes `499`, Experience `500`, Useful `501`,
  Templates `502`, and Collections `503`. No replacement command-help messages
  were sent. Experience has one new pinned, tracked quote index at message
  `1061`.
- The production semantic-search corpus full scan is complete and ready for
  `/search`: the completed checkpoint had current InSync 948 ready documents,
  old InSync 2166, and Sova.Nest 78; incremental sync continues to grow the
  corpus (already above 3190, with no pending embeddings). MTProto outer-page
  continuation now uses `offset_id`; the server validated pagination beyond
  both the 100-message API page and the 500-message checkpoint page.
- Search uses the official synchronous `batchEmbedContents` endpoint in
  sequential batches of 20 and the same `gemini-embedding-2`/768-dimensional
  space for both keys. The fallback setting is a second Gemini key/project, not
  a second embedding model, so all vectors remain compatible.
- Production Google Calendar OAuth client and token files are installed with
  mode `0600`. The migration-candidate production doctor no longer requires Go,
  ffmpeg, tesseract, Ollama, or Codex CLI on the server.
- Workspace has Inbox-only `/quote`, `/quote show`, and `/quote edit` flows.
  Quotes are rendered as native Telegram blockquotes with an italic optional
  author, stored in `workspace_quotes`, linked from a dynamic `Опыт` index, and
  moved to `needs_review` when their source message changes. Wizard/edit state
  survives restart; ambiguous Telegram delivery/edit is not repeated without
  an explicit reconciliation command. Existing published quote messages are
  restyled in place by a versioned renderer migration.
- Deferred tasks have a durable reminder outbox and a startup/minute worker.
  Each schedule generation sends at most one reminder, retries confirmed
  failures, never blindly retries ambiguous sends, reopens the task, and
  refreshes its card and backlog.
- Initial task cards, Nest digests, and Nest Calendar candidate cards also use
  durable send claims. Confirmed Bot API rejection can retry; ambiguous send
  outcomes become `unknown` and require manual reconciliation, preventing a
  blind duplicate after restart. Overview runs older than the two-hour worker
  lease no longer block every future run.
- Publish now uses the shared redacted Google REST client, source-part coverage,
  safe Telegram HTML validation/splitting, a freer explicitly trusted revision
  field, and durable preview/final outboxes that resume without duplicate sends.
- Semantic search storage and routing exist for current InSync, legacy InSync,
  and Sova.Nest. `workspace search-index --full-scan` is the readiness gate;
  `/search` remains disabled until that pass succeeds and
  `SOVA_SEARCH_ENABLED=true` is configured. Both API-key routes use the same
  `gemini-embedding-2` 768-dimensional space.
  MTProto full scans are bounded/checkpointed; background sync combines recent
  refresh with a rotating historical edit/delete audit.
- `workspace seed-command-help` now stores one tracked `command_help` message
  per Workspace topic, pins it, and edits the same message on later runs. Inbox
  lists `/quote` and `/search`; Experience has dedicated quote instructions.

- Repository has a baseline commit and a working Go + SQLite MVP foundation.
- Runtime: the legacy host uses two services; the alwaysdata target uses one Go
  process in `sova serve-all` with the existing Nest and Workspace controllers.
- Overview triggers: daily schedule, Nest service commands, pinned Chat button,
  and manual CLI.
- The daily scheduled trigger can be persisted on/off from the Nest `Status`
  topic with `/daily on|off|status`; manual `/run`, the pinned Chat button, and
  CLI runs remain available while it is off.
- Shared overview cooldown: 15 minutes across all triggers.
- Nest topics: Digest, Calendar, Status, Chat. Automated digest/status output
  does not go to Chat.
- Sova Nest overview sync reads only `SOVA_NEST_TELEGRAM_ALLOWED_CHATS`.
  Workspace/personal Telegram sources stay in `SOVA_WORKSPACE_*` config and are
  not part of the study digest allowlist.
- Production Nest classification/event extraction starts with
  `gemini-3.5-flash-lite`. The digest route now starts with
  `gemini-3-flash-preview`; `gemini-3.5-flash` and
  `gemini-3.1-flash-lite` remain configured as fallbacks. The change was made
  after `gemini-3.5-flash` repeatedly exceeded the production smoke deadline;
  both the classification/event and digest smoke stages passed after rotation.
  Historical Ollama/Qwen commands remain for reproducibility but are not used
  by the production overview.
- Telegram auth: dedicated MTProto project session only.
- Telegram sync verified end-to-end for two Sova Nest study sources: dry-run
  writes nothing, sync stores 200 messages, repeat sync dedupes to zero new
  messages, media metadata and one service message are handled.
- `sova run --trigger manual` now calls Telegram sync and completes successfully
  when there are no new messages.
- Google classification, compact run bundle generation, Gemini digest generation,
  and Nest Digest publication are wired. Classification and event extraction
  use separate bounded batches, exact structured-response validation,
  sequential model fallback, split retry, local keep-all/no-event terminal
  behavior, per-attempt telemetry, and per-decision winning model persistence.
- `sova serve` uses Bot API long polling for text commands in the Status service
  topic, an existing "Создать обзор" button in the Chat study topic, Status
  progress updates, Calendar date-edit callbacks, and the local daily scheduler.
  Control/button messages are created explicitly through `nest-seed-topics`,
  `/button`, `/start`, or `/help`; `serve` no longer sends a new Chat button on
  every startup. Short service messages in Calendar/Status and final digests use
  constrained Telegram HTML. Bot API TCP dialing uses three bounded attempts
  with context-aware backoff; failures proven to happen before request delivery
  remain safely retryable, while post-connect ambiguity is not resent blindly.
- Codex CLI discovery remains only as historical tooling. Production generation
  uses Gemini and degrades to a provenance-preserving fallback without losing
  synced messages; `sova retry-run --id` can recover older Codex/Qwen failures
  through the current Google route.
- Nest digests use a bold title, an italic one- or two-sentence synthesis of the
  main developments, and an optional `ПРИМЕЧАНИЯ` section with at most five
  concrete notes. Each note links its first two or three words directly to the
  saved Telegram source; separate `ГЛАВНОЕ`, `КАЛЕНДАРЬ`, and `ИСТОЧНИКИ`
  sections are omitted.
- Overview run 5 was recovered from 42 stored messages and published
  successfully after its original empty Qwen response.
- Compact indexes exist for Telegram recent content, overview runs, and calendar
  setup state under `.state/index/`. Model-call metrics are indexed at
  `.state/index/model-performance.md`; `.state/index/qwen-performance.md` is a
  one-release compatibility alias. Historical model comparison summaries are written to
  `.state/index/qwen-benchmark.md` and labeled eval summaries to
  `.state/index/qwen-eval.md`.
- Historical local Qwen evaluation remains reproducible but does not select the
  production route. A labeled 100-message eval on
  2026-06-27 showed `qwen3:8b` is the best next candidate after prompt/event
  threshold tuning; `qwen3:4b`, `gemma3:4b`, and `llama3.2:3b` are not suitable
  for this MVP pipeline.
- Google Calendar approval flow is implemented: event-like messages become
  Calendar topic candidates with approve/reject/date-edit buttons, and approve
  creates a real Google Calendar event with 7d/3d/1d/1h reminders after
  browser-based `sova google-login` with a temporary localhost OAuth callback.
  Approval reserves a deterministic external event ID before the provider call;
  retries reconcile that ID, and stale reject/date-edit callbacks cannot race
  an in-flight approval.
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
  `/doc new|append|rename|delete-part|delete|publish|show`, reply `/publish`,
  `/template new|append|rename|type|move|rename-type|delete-type|show`,
  `/collection new|add|rename|description|delete|rename-item|delete-item|move-item|order-item|show`,
  and diagnostic reply `/id`. Commands may be sent from the matching topic or
  Inbox; source messages are read from the matching topic (`Заметки`,
  `Заготовки`, `Коллекции`) unless a valid reply is provided. Note/template
  append resolves by ID or exact title and asks for clarification when a title
  is ambiguous.
- Note indexes render the first note line as a bold clickable title with a
  stable pleasant emoji; later note parts render as bracketed links. Collection
  indexes prefer the bot-created collection-card message link over the first
  item link.
- Note publish MVP exists: `/doc publish` or reply `/publish` assembles ordered
  note parts, uses a provider boundary, calls Gemini `generateContent` when
  `SOVA_GEMINI_API_KEY` is configured, falls back to a local
  meaning-preserving mock formatter only when Gemini config is empty, sends
  preview messages to Inbox, and supports approve/cancel/edit buttons. Approve
  posts final material to `Полезное`, persists source-to-derived published
  mappings, updates document target IDs, and updates a Useful index message.
  Repeat publish warns when a note already has a target unless `force` is
  passed.
- Publish previews now separate `✨ ИИ-правка` from `📝 Вручную`. Manual
  edit waits durably for one full plain-text replacement in Inbox, creates a
  replacement preview without calling Gemini, blocks approval of the old
  preview, survives restart, and still requires final approval.
- `/useful archive` remains index-only. Inbox `/useful delete` now requires a
  typed confirmation, deletes only an owned bot publication in the configured
  Useful topic, archives its document, closes provenance/final rows, removes
  matching search state, and refreshes the pinned index. The requested legacy
  target `https://t.me/c/4301779750/18/750` resolves locally to document `#40`;
  production deletion is intentionally deferred until deployment.
- Dynamic Workspace indexes/backlogs now render numbered oldest-first entries.
  Note subparts keep their bracketed `[Часть …]` lines; template type headings
  remain unnumbered headings and their documents carry the sequence numbers.
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
  type by buttons/text, and `/template delete-type` asks whether to move
  templates to `Остальное` or archive them with the type. The `Заготовки` index
  renders type headings plus bold prompt links. `Остальные` is normalized to
  `Остальное`.
- Stage 6 document commands no longer support reverse `/new ...` aliases; new
  items are created through their command namespace (`/doc new`,
  `/template new`, `/collection new`). `workspace seed-command-help` sends
  current command reference messages into each Workspace topic.
- Stage 6 collection indexes now render as one flat list of collection-card
  links and quote-formatted descriptions. Each collection remains its own bot
  message with description, ordered item links, and edit/delete/reorder/move
  commands for items. Item commands accept reply-to-item, item number, or item
  title where applicable.
- Published notes leave the active Notes index after approve. Later edits to a
  published note source mark the Workspace document and derived published rows
  `needs_review`; the Useful index shows that review marker.
- On 2026-07-09, `workspace seed-command-help` sent current command reference
  messages into real `InSync v1.0` topics. Message IDs: Inbox `356`,
  `Задачи` `357`, `Заметки` `358`, `Опыт` `359`, `Полезное` `360`,
  `Заготовки` `361`, `Коллекции` `362`.
- Later on 2026-07-09, command help was resent after removing `/new ...`
  aliases, adding `/id`, documenting collection item reply/title handling, and
  noting real Gemini publish formatting. Message IDs: Inbox `497`, `Задачи`
  `498`, `Заметки` `499`, `Опыт` `500`, `Полезное` `501`, `Заготовки` `502`,
  `Коллекции` `503`.
