# Sova Agent Contract

## Scope

These rules apply to the entire repository.

## Product boundary

Sova is a local-first study information pipeline. The MVP reads allowlisted
Telegram sources, extracts useful information, builds a digest, publishes it to
the Sova Nest supergroup, and creates Google Calendar events only after approval.

## Always read

1. `memory/index.md`
2. `memory/current_state.md`
3. The smallest relevant document linked from `docs/data_map.md`

Do not load raw JSONL, full logs, SQLite dumps, transcripts, OCR output, or
binary files by default. Search indexes first and retrieve only targeted data.

## Data rules

- Raw Telegram records and downloaded files are immutable.
- Derived artifacts must preserve source IDs and Telegram message links.
- SQLite and JSONL are storage, not prompt context.
- Markdown contains compact rules, indexes, decisions, and current state.
- Secrets, sessions, raw data, logs, and generated artifacts stay untracked.

## Runtime rules

- Go is the orchestration language.
- SQLite is the local state store.
- All overview triggers share one 15-minute cooldown.
- Telegram Desktop `tdata` must never be imported or accessed.
- Use a dedicated MTProto project session created by explicit login.
- Automated bot output must not be posted to the Nest `Chat` topic.
- Commands may be accepted from the Nest `Chat` topic.

## Verification

Run:

```bash
go test ./...
go vet ./...
git diff --check
```

For implementation steps, use a separate reviewer agent when requested by the
active delivery plan. Reviewer summaries must cite files or tests, not dump raw
logs into the main context.

