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
- `Status`: progress, health, failures, reauthentication, and run status.
- `Chat`: user-owned free-form messages and command input. No automatic digest,
  calendar, or status output is posted here. A manually pinnable control
  message with the run button is allowed. Human-facing topic intro messages are
  intentionally formatted for Telegram and may be pinned manually. `serve`
  listens for callbacks from the pinned button but does not create a fresh
  control message on each startup.

## Telegram sync

The allowlist is the only source selector. Each synced message preserves:

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
