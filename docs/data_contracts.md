# Data Contracts

## Stable identifiers

Use stable IDs for runs, messages, artifacts, facts, approvals, and events.
Telegram source identity is `(chat_id, message_id)`.

## Overview run

An overview run has:

- `id`
- `trigger`: `scheduled`, `nest_button`, or `manual`
- `status`: `running`, `success`, or `failed`
- `started_at`, `finished_at`
- short `summary` and optional `error`

All triggers reserve a run through the same SQLite transaction. A new run is
rejected when another run is active or the latest run started less than 15
minutes ago.

If final Codex generation is unavailable, a new run publishes a compact
provenance-preserving fallback digest and records the degraded mode in its
summary. Legacy runs that failed specifically at the Codex step may be retried
from their saved compact bundle without repeating Telegram sync.

An incomplete, malformed, timed out, or otherwise unavailable Qwen
classification batch is conservatively retained for the final digest and the
run records Qwen fallbacks instead of failing. Classification decisions are
stored after each processed batch so a later failure does not discard earlier
work. Codex still filters the compact retained messages when writing the final
digest.

Qwen model-call metrics are compact derived records. They may include run id,
stage, batch index, message count, approximate input characters, duration,
success/fallback counts, model name, and a short error string. They must not
store Telegram message text, prompts, raw responses, secrets, or session data.

Overview progress may be published to the Nest Status topic as one edited
status message. Progress text is operational status, not prompt context. Short
controlled bot messages may use Telegram HTML formatting; generated digests stay
plain text so long messages can be split safely.

## Nest topics

- `Digest`: automated overview output.
- `Calendar`: event candidates, approval/edit buttons, and calendar results.
- `Status`: service topic for text commands, progress, health, failures,
  reauthentication, cooldown/fallback notices, and run status.
- `Chat`: user-owned study/materials topic and manual conversation. No
  automatic digest, calendar, or status output is posted here. A manually
  pinnable control message with the run button is allowed. Human-facing topic
  intro messages are intentionally formatted for Telegram and may be pinned
  manually. `serve` listens for callbacks from the pinned button but does not
  create a fresh control message on each startup.

## Telegram sync

The Sova Nest study allowlist is the only source selector for overview sync.
It is configured with `SOVA_NEST_TELEGRAM_ALLOWED_CHATS`; personal Workspace
sources must not be placed there. Each synced message preserves:

- stable source ref: `telegram:<peer_kind>:<chat_id>`
- Telegram identity: `(chat_id, message_id)`
- message date, kind, text/caption, media type, and source link when available
- append-only raw JSONL record under `.state/raw/telegram/`

The compact review surface is `.state/index/telegram-recent.md`; agents should
read it before retrieving targeted raw records.

## Calendar approval

Event candidates are derived artifacts linked to `(run_id, chat_id, message_id)`
and the original Telegram source link. Candidates are published only to the Nest
`Calendar` topic with approve/reject/edit-date buttons. Event extraction may
return partial results; valid candidates are retained and missing input IDs are
treated as non-events.

Candidate statuses:

- `pending`: waiting for user action.
- `approved`: reserved intermediate state before event creation.
- `rejected`: user rejected the candidate.
- `created`: user approved and Google Calendar returned an event id.
- `failed`: approval was attempted but Google Calendar creation failed.

Google Calendar events are created only after approval and use reminders at
10080, 4320, 1440, and 60 minutes before the event.

Before approval, a pending candidate date may be edited from the Calendar topic
with the formats `YYYY-MM-DD` or `YYYY-MM-DD HH:MM`. Date-only edits preserve
the existing event time and duration.

## Workspace audit

Workspace Stage 1 uses dedicated `workspace_*` tables and generated artifacts
under `.state/artifacts/workspace/audit/<run-id>/`.

Forum topic discovery stores only compact topic metadata in `workspace_topics`.
Durable audits store compact per-message records in `workspace_audit_runs` and
`workspace_audit_records`. Each audit record preserves `source_ref`, `chat_id`,
`message_id`, and `message_link`; it does not replace immutable raw Telegram
records.

Review artifacts contain uncertain rows only. User decisions are captured in
empty `user_decision` and `user_comment` columns for Stage 2 merge/preview.

Workspace Stage 1 must not migrate, mass post, delete, or edit Telegram
messages.

## Workspace live bot

`sova workspace serve` is separate from Nest `sova serve`. It polls only
`SOVA_WORKSPACE_BOT_TOKEN` for the configured `InSync v1.0` chat and uses
Workspace topic IDs from `SOVA_WORKSPACE_*`.

Live Workspace messages are stored as compact metadata in `workspace_messages`,
not as raw Bot API JSON. Each message preserves Telegram `(chat_id, message_id)`,
topic ID, source link, text/caption, media type, forward/reply metadata, and
edit timestamp when present.

Logical message clusters are stored in `workspace_clusters` and
`workspace_cluster_messages`. Replies attach explicitly. Forwarded/media
messages attach only when they immediately follow the user's previous message in
the same topic; the system does not use a broad time-window grouping rule.

Task cards are stored in `workspace_tasks` and published to the Workspace
`Задачи` topic with Done/Cancel/Defer buttons. Source-to-card mappings are also
recorded in `workspace_derived_messages` so edited source messages can update
open/deferred task cards or mark already published derived material as
`needs_review`.

Stage 6 document metadata is stored in `workspace_documents` and
`workspace_document_parts`. Notes, templates, and collection items keep compact
titles/categories plus per-part Telegram source IDs/links; active indexes are
tracked in `workspace_topic_indexes`.
