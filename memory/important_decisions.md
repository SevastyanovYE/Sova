# Important Decisions

- Raw data stays outside model context; agents navigate through compact indexes.
- SQLite does not enter prompts directly; only targeted query results do.
- Production Nest performs bounded structured classification and event
  extraction through the ordered Google-model route; Qwen remains transitional
  experiment tooling only.
- Codex receives a compact run bundle and produces the final digest.
- No Telegram Desktop `tdata` fallback.
- The Nest `Status` topic is the service topic for text commands and operations.
- The Nest `Chat` topic is user-controlled for study materials, manual notes,
  and the pinned run button; it receives no automated pipeline output.
- The Nest Chat control button is created explicitly for manual pinning; `serve`
  must not send a new control message on every startup.
- All overview triggers share a 15-minute cooldown.
- Sova.Workspace is a separate product branch from Sova.Nest. Shared core code
  is allowed, but Workspace config, commands, topics, docs, tables, and tests
  must make the boundary explicit.
- Sova.Nest overview sync uses only `SOVA_NEST_TELEGRAM_ALLOWED_CHATS`.
  Workspace/personal Telegram sources must use `SOVA_WORKSPACE_*` config and
  must not be added to the Nest study digest allowlist.
- Workspace Stage 1 is audit-only and non-destructive. It may cache topic
  metadata and write review artifacts, but it must not migrate, mass post,
  delete, or edit Telegram messages.
- Workspace Stage 2 produces a compact migration preview from user review
  decisions and then stops for explicit approval before any publication.
- Workspace topic bootstrap may create only missing target forum topics and a
  local numeric ID helper file; help messages, pinned messages, migration posts,
  deletion, and editing remain separate approval-gated actions.
- Workspace task backlog is a tracked bot-created index message. If editing the
  known index fails, `serve` must not create a fresh backlog on every callback;
  cleanup/reseed should repair a bad stored index deliberately.
- Workspace note publication uses a provider boundary. When
  `SOVA_GEMINI_API_KEY` is configured, live preview calls Gemini
  `generateContent`; when Gemini config is empty, the path falls back to a
  local meaning-preserving mock formatter. Base formatting may not invent new
  facts; a separate trusted user revision may explicitly authorize additions,
  deletion, replacement, or substantial rewriting within its stated scope.
- Workspace document indexes are bot-maintained navigation surfaces, not raw
  content stores. Template types may exist empty, collections are indexed as
  links to collection-card messages, and published notes leave the active Notes
  index while retaining source IDs/links for review handling.
- Ambiguous Telegram sends in reminders, Publish, and release announcements are
  durable `unknown` states and are never resent automatically.
- Quote wizard progress after the required text is durable in
  `workspace_quotes`; quote delivery also uses `sending`/`unknown` handoff so a
  restart cannot create a duplicate in `Опыт`.
- Semantic-search fallback changes only the Gemini API key/project. Documents
  and queries remain on `gemini-embedding-2` at 768 dimensions; incompatible
  embedding spaces are never mixed.
- Semantic-search MTProto backfill is page-bounded and checkpointed by scan
  generation/cursor. Incremental sync combines a recent window with a rotating
  historical audit cursor so old edits/deletions are eventually revisited.
- Release announcements are dry-run by default and require a production
  deployment receipt and exact tag for the same embedded version and commit;
  the version is announceable only once per environment even if its tag moves.
