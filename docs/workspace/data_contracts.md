# Workspace Data Contracts

## Configuration

Workspace config keys:

- `SOVA_WORKSPACE_BOT_TOKEN`
- `SOVA_WORKSPACE_LEGACY_SOURCE`
- `SOVA_WORKSPACE_CHAT_ID`
- `SOVA_WORKSPACE_TOPIC_INBOX_ID`
- `SOVA_WORKSPACE_TOPIC_TASKS_ID`
- `SOVA_WORKSPACE_TOPIC_NOTES_ID`
- `SOVA_WORKSPACE_TOPIC_EXPERIENCE_ID`
- `SOVA_WORKSPACE_TOPIC_USEFUL_ID`
- `SOVA_WORKSPACE_TOPIC_TEMPLATES_ID`
- `SOVA_WORKSPACE_TOPIC_COLLECTIONS_ID`
- optional publish formatter:
  - `SOVA_GEMINI_API_KEY`
  - `SOVA_GEMINI_MODEL`

Control config keys:

- `SOVA_CONTROL_BOT_TOKEN`
- `SOVA_CONTROL_CHAT_ID`
- `SOVA_CONTROL_TOPIC_STATUS_ID`
- `SOVA_CONTROL_TOPIC_ERRORS_ID`
- `SOVA_CONTROL_TOPIC_RUNS_ID`
- `SOVA_CONTROL_TOPIC_REVIEW_ID`
- `SOVA_CONTROL_TOPIC_TEST_LAB_ID`
- `SOVA_CONTROL_TOPIC_WORKSPACE_ID`
- `SOVA_CONTROL_TOPIC_NEST_ID`
- `SOVA_CONTROL_TOPIC_IDEAS_ID`

No secret values are committed. `.env.example` only documents the keys.
Workspace sources are configured through `SOVA_WORKSPACE_*` keys and must stay
out of `SOVA_NEST_TELEGRAM_ALLOWED_CHATS`, which belongs to the study digest
branch.

`workspace bootstrap-topics` writes an env-style helper file with numeric IDs
only:

```text
.state/artifacts/workspace/bootstrap/workspace_control_topic_ids.env
```

The file is safe to copy from manually, but it is generated state and should not
be committed.

## SQLite Tables

`workspace_topics` caches compact legacy forum topic metadata:

- `source_ref`
- `chat_id`
- `topic_id`
- `top_message_id`
- `title`
- `pinned`
- `closed`
- `hidden`
- `created_at`
- `discovered_at`

`workspace_audit_runs` records each durable audit:

- `id`
- `source_ref`
- `status`
- `dry_run`
- `started_at`
- `finished_at`
- `artifact_dir`
- `summary`
- `error`

`workspace_audit_records` stores compact per-message decisions:

- source identity: `source_ref`, `chat_id`, `message_id`, `message_link`
- topic metadata: `source_topic`, `topic_id`, `top_message_id`
- message metadata: `message_date`, `edit_date`, `media_type`, `pinned`,
  `long_message`, `edited`
- audit output: `short_summary`, `detected_type`, `model_decision`,
  `confidence`, `suggested_target`, `reason`

These records preserve source IDs and links. They do not replace immutable raw
Telegram storage.

`workspace_messages` stores compact live Bot API message metadata for
`InSync v1.0`:

- source identity: `chat_id`, `message_id`, `topic_id`, `source_link`
- sender metadata: `from_user_id`, `from_is_bot`
- body fields: `text`, `caption`, `media_type`
- relationship fields: `forwarded`, `forward_chat_id`,
  `forward_message_id`, `reply_to_message_id`
- timing fields: `date`, `edit_date`, `created_at`, `updated_at`

It is not raw Telegram storage and does not store full Bot API payloads.

`workspace_clusters` and `workspace_cluster_messages` store logical clusters:

- cluster identity: `id`, `chat_id`, `topic_id`, `status`
- ordered parts: `cluster_id`, `chat_id`, `message_id`, `position`, `role`
- every part keeps its own Telegram source IDs and link through
  `workspace_messages`

Cluster order follows Telegram message order. The live bot may auto-attach
reply-linked messages and immediately following forwarded/media messages, but
manual `/cluster` commands remain the correction path.

`workspace_tasks` stores bot-created task cards:

- source mapping: `source_chat_id`, `source_message_id`, `source_link`,
  `source_cluster_id`
- card mapping: `card_chat_id`, `card_topic_id`, `card_message_id`
- user-visible data: `text`, `emoji`, `status`, `deferred_until`

Task cards live in `Задачи`. The visible card text does not include a "Задача"
heading or source link. Defer presets (`На неделю`, `На месяц`) resolve to
08:00 in the configured project timezone; explicit user-entered dates keep the
entered time or use the date-only default.

`workspace_derived_messages` maps source messages/clusters to bot-created
derived messages. Published derived messages can be marked `needs_review` when
their source is edited, instead of silently rewriting final material.

`workspace_topic_indexes` stores bot-created index/backlog message IDs, such as
the delayed-task backlog in `Задачи`. The task backlog intentionally lists only
`deferred` tasks and links each item back to its original task card; open tasks
stay visible as individual cards.

`workspace_documents`, `workspace_document_parts`, and
`workspace_document_types` store Stage 6 note/template/collection metadata:

- `doc_type`: `note`, `template`, or `collection`
- `status`: `active`, `published`, `archived`, or `needs_review`
- document title/category and optional target message IDs. Target IDs are used
  for collection-card messages and published Useful material.
- template type rows: stable type name, emoji, position, and active/archived
  status. Empty active template types still render in the template index.
- per-part source mapping: `source_chat_id`, `source_message_id`,
  `source_cluster_id`, `source_link`
- compact part text only; raw Telegram JSON remains outside prompt context

The live bot maintains index messages for active notes in `Заметки`, templates
in `Заготовки`, collection-card links in `Коллекции`, and published Useful
links in `Полезное`. Notes render as a bold first-part link plus bracketed
part links. Template indexes render active type headings and bold prompt links.
Collection indexes are one flat list of collection-card links; each collection
card stores its own description and item links. Published material is not
silently rewritten on source edit: published documents and derived rows are
marked `needs_review`.

## Audit Artifacts

Durable audits write:

```text
.state/artifacts/workspace/audit/<run-id>/workspace_audit_summary.md
.state/artifacts/workspace/audit/<run-id>/workspace_review_candidates.csv
.state/artifacts/workspace/audit/<run-id>/workspace_review_candidates.md
.state/artifacts/workspace/audit/<run-id>/workspace_control_card_drafts.md
.state/artifacts/workspace/audit/<run-id>/workspace_topic_pin_drafts.md
```

`workspace_control_card_drafts.md` and `workspace_topic_pin_drafts.md` are
Control-review surfaces only. They are not proof of approval and must not be
treated as published Workspace content.

## Review Candidate Columns

Both CSV and Markdown review files expose:

```text
source_topic
message_date
message_link
short_summary
detected_type
model_decision
confidence
suggested_target
reason
media_type
user_decision
user_comment
```

Allowed `user_decision` values:

```text
take
archive
trash
study
control
collection
later
```

The user-filled review file becomes the input for Stage 2 migration preview.

## Migration Preview

Stage 2 writes:

```text
.state/artifacts/workspace/migration_preview/<run-id>/workspace_migration_preview.md
.state/artifacts/workspace/migration_preview/<run-id>/workspace_migration_preview.csv
```

The preview contains compact source-preserving rows only:

```text
source_topic
message_date
message_link
short_summary
detected_type
audit_decision
confidence
user_decision
user_comment
final_action
target
reason
```

`final_action` values include `migrate`, `route_to_study`,
`route_to_control`, `archive`, `trash`, `later`, `skip_done_task`, and
`pending_review`. A preview with `pending_review` rows is not ready for
publication.
