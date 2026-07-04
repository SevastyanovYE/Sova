# Workspace Legacy Audit

The Stage 1 audit turns the old `InSync` group into a reviewable archive map. It
does not migrate content.

## Inputs

- `SOVA_WORKSPACE_LEGACY_SOURCE`: old `InSync` source, accepted in the same
  forms as `SOVA_NEST_TELEGRAM_ALLOWED_CHATS`, but kept separate from the Sova
  Nest study digest allowlist.
- Dedicated MTProto session from `SOVA_TELEGRAM_SESSION_PATH`.
- Previously indexed Telegram messages in SQLite.
- Optional cached forum topics from `sova workspace discover`.

The audit reads SQLite records and compact topic metadata. Raw JSONL and full
logs are not prompt context.

## Outputs

Normal audit writes:

```text
.state/artifacts/workspace/audit/<run-id>/workspace_audit_summary.md
.state/artifacts/workspace/audit/<run-id>/workspace_review_candidates.csv
.state/artifacts/workspace/audit/<run-id>/workspace_review_candidates.md
```

It also stores compact records in `workspace_audit_runs` and
`workspace_audit_records`.

`--dry-run` writes nothing durable and prints the summary.

## Classification

Stage 1 uses a deterministic heuristic fallback. This is deliberate so the audit
can run before a larger Workspace LLM classifier is configured. The summary
labels this clearly.

Primary content types stay within the MVP taxonomy:

- `task`
- `deferred_task`
- `draft_note`
- `note_document`
- `useful_material`
- `experience`
- `idea`
- `template_document`
- `collection_item`
- `external_branch_reference`

Disposition labels:

- `take`
- `archive`
- `review`
- `skip_done_task`
- `route_to_study`
- `route_to_control`
- `trash`

Review files include only uncertain rows, primarily `review` decisions and
low-confidence items. Clear archive/take routes are counted in the summary but
not forced onto the manual review list.

## Filtering Choices

The review CSV exposes `user_decision` for rows that still need judgment:

- `take`: migrate to the suggested Workspace topic after Control review.
- `archive`: keep only in the legacy archive; do not migrate.
- `trash`: exclude punctuation/noise/useless material from migration.
- `study`: route outside Workspace to the study branch.
- `control`: route project/service material to Sova.Control.
- `collection`: migrate as a collection item.
- `later`: keep for a later manual pass.

Automated audit decisions may also use internal dispositions such as
`skip_done_task`, `route_to_study`, and `route_to_control`; these are shown in
the preview but are not values the user needs to type into the CSV.

## Stage 2 Review Preview

After the user fills `user_decision` and optional `user_comment` in the review
CSV, run:

```bash
go run ./cmd/sova workspace review-preview
```

The command uses the latest successful durable audit by default. It can also be
pointed at a specific run/file:

```bash
go run ./cmd/sova workspace review-preview --audit-run 12 --review-csv path/to/workspace_review_candidates.csv
```

It writes a compact migration preview and stops for approval. It does not post,
migrate, delete, or edit Telegram messages.

## Legacy Topic Rules

- Media, audio, documents, web pages, and other unsupported attachments always
  stay as review candidates for the user.
- Messages that contain only punctuation, such as a single `.`, `-`, or similar
  placeholder, become `trash`.
- `Задачи`: only messages linked from pinned material and the latest 10 legacy
  task-topic messages are automatic migration candidates. Older task-topic
  messages are archived unless media/manual-review rules apply.
- `Заметки`: fully audited; pinned, long, edited, tagged, project, and study
  material is prioritized. Material linked from the pinned note index remains
  reviewable because it may need to become note cards, useful material, or stay
  as notes.
- `Заготовки`: only messages linked from pinned material and the latest 10
  legacy template-topic messages are automatic migration candidates. Prompt
  bundles with headings like `12. Учитель` are split into Control-review card
  drafts and need approval before real Workspace publication.
- `Полезное`: usually `useful_material` and `take`.
- `Goловная боль`: archive, except project/Sova material routes to Control.
- `Учёба`: routes to a future Study branch.
- `Журнал причёсок` and `BTBW`: archive.
- `Рецепты`: `collection_item` for `Коллекции`.

User tags that force migration into the new InSync v1.0:

- `#мюсли`: `Заметки`
- `#идеи`: `Заметки`
- `#опыт`: `Опыт`
- `#знания`: `Полезное`
- `#связи`: `Заметки`
- `#поэзия`: `Коллекции`
- `#аниме`: `Коллекции`

These tag rules apply before legacy topic reduction, so tagged messages are not
archived merely because they are old task/template/topic material.

## Stop Points

Stop and ask the user when:

- Telegram credentials, session login, or source IDs are missing.
- Forum topics cannot be discovered safely.
- The audit summary and review files are ready.
- The migration preview is ready for user approval.
- Any next step would post, migrate, delete, or edit real InSync v1.0 messages.
  Sova.Control may be used as a format/style/test-lab review surface before
  approval.
