# Text MVP follow-ups

This document is scoped only to the current Sova text MVP branch:
Telegram allowlisted sources -> SQLite/local indexes -> Qwen classification ->
Codex digest -> Sova Nest topics -> Google Calendar approval.

It is not the full Sova roadmap. Other project branches, such as broader
knowledge management, richer personal assistant behavior, or non-Telegram
integrations, may evolve separately.

## Current conclusion

The text MVP is usable enough to pause feature work and move to the next project
branch:

- Telegram sync is end-to-end verified for allowlisted sources.
- Overview runs share one cooldown and can be triggered manually, by Nest
  button, or by local schedule.
- Digest output, status/progress output, and calendar approval output are routed
  to separate Nest topics.
- The `Chat` topic stays user-controlled for study materials, manual notes, and
  the pinned run button; text commands live in the `Status` service topic.
  `serve` no longer posts a new button on every startup.
- Calendar candidates can be approved, rejected, or date-edited before approval.
- Codex/Qwen failures degrade to fallback behavior instead of losing messages.
- Runtime remains on `qwen3:14b`; `qwen3:8b` is the only retained alternative
  candidate for future tuning.

## Risks to revisit

- **Model latency and heat.** `qwen3:14b` is heavy on the local Mac. It is
  acceptable for occasional study digests, but not pleasant for frequent
  experimentation.
- **Model quality is not settled.** The labeled 100-message eval showed that
  `qwen3:8b` is faster and more stable, but too aggressive on `has_event`.
  `qwen3:14b` is safer at small batches but slow and not reliable at larger
  batches.
- **Calendar false positives.** Over-detecting events can make the `Calendar`
  topic noisy. Before switching models, tighten `has_event` prompting and add a
  deterministic post-filter for vague/context-only messages.
- **Eval observability.** `qwen-eval` currently writes final aggregate rows. A
  future pass should save and show per-batch progress so long model runs do not
  look stuck.
- **Daily scheduling depends on the laptop being awake.** The local scheduler in
  `serve` is enough for MVP, but reliable daily runs need launchd and a clear
  sleep/wake strategy.
- **Date inference remains contextual.** Forwarded messages and relative dates
  can still be wrong. Manual date editing covers the MVP case, but extraction
  should preserve uncertainty more explicitly.
- **Text-only coverage.** Voice, OCR, PDF, DOCX, XLSX, and other file extractors
  are intentionally out of scope for this text MVP. Media-only messages are
  represented by placeholders.

## Good next changes for this branch

1. Add per-batch progress and partial scoring to `qwen-eval`.
2. Tune `has_event` for `qwen3:8b`, then rerun the labeled eval against
   `qwen3:14b` batch 8 and `qwen3:8b` batch 16.
3. Add a Calendar post-filter before candidate creation:
   require explicit date/time/deadline or a very strong schedule-change signal.
4. Add launchd documentation for running `sova serve` after boot and around the
   daily run time.
5. Improve digest style with a few saved golden examples and snapshot tests for
   Telegram-friendly formatting.
6. Add file/voice/image extractors only after the text classifier and calendar
   event threshold are stable.

## Strong parts worth preserving

- Local-first storage and small compact indexes keep raw data out of prompt
  context.
- One worker and one cooldown keep the MVP understandable and safe.
- Topic separation in Sova Nest is a good product boundary: `Chat` for study
  materials and the pinned run button, `Digest` for final output, `Calendar`
  for approval, `Status` for commands and operations.
- Derived artifacts keep source IDs and Telegram links, which makes every
  digest item reviewable.
- The fallback path is valuable: slow or malformed model output should degrade,
  not erase a run.
- The labeled eval flow is worth keeping even if the dataset changes; it gives a
  concrete way to compare prompt/model changes without reading raw dumps.
