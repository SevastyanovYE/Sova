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

## Nest topics

- `Digest`: automated overview output.
- `Calendar`: event candidates, approval buttons, and calendar results.
- `Status`: health, failures, reauthentication, and run status.
- `Chat`: user-owned free-form messages and command input. No automatic digest,
  calendar, or status output is posted here.

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
`Calendar` topic with approve/reject buttons.

Candidate statuses:

- `pending`: waiting for user action.
- `approved`: reserved intermediate state before event creation.
- `rejected`: user rejected the candidate.
- `created`: user approved and Google Calendar returned an event id.
- `failed`: approval was attempted but Google Calendar creation failed.

Google Calendar events are created only after approval and use reminders at
10080, 4320, 1440, and 60 minutes before the event.
