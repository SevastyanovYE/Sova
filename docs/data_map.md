# Data Map

Read this file before opening runtime data.

| Need | Read first | Full storage |
| --- | --- | --- |
| Current project state | `memory/current_state.md` | Git history and SQLite runs |
| Architecture decision | `memory/important_decisions.md` | Decision records |
| Text MVP follow-ups | `docs/text_mvp_followups.md` | Git history and future issue tracker |
| Recent Telegram content | generated `.state/index/telegram-recent.md` | `.state/raw/telegram/*.jsonl` |
| Run status or failure | generated `.state/index/runs.md` | SQLite `overview_runs`, JSONL logs |
| Qwen latency/fallbacks | generated `.state/index/qwen-performance.md` | SQLite `model_calls` |
| Qwen model benchmark | generated `.state/index/qwen-benchmark.md` | `.state/artifacts/qwen-benchmark-*.jsonl` |
| Qwen labeled eval | generated `.state/index/qwen-eval.md` | `.state/artifacts/qwen-eval/*.jsonl` |
| Extracted knowledge | generated `.state/index/facts.md` | SQLite facts/artifacts |
| Calendar candidates | generated `.state/index/calendar.md` | SQLite event candidates/events |
| Workspace foundation | `docs/workspace/overview.md` | Workspace config and future stage docs |
| Workspace audit rules | `docs/workspace/audit.md` | `.state/artifacts/workspace/audit/` |
| Workspace data contracts | `docs/workspace/data_contracts.md` | SQLite `workspace_*` tables |
| Workspace migration preview | `docs/workspace/audit.md` | `.state/artifacts/workspace/migration_preview/` |
| Workspace/Control topic IDs | `docs/workspace/data_contracts.md` | `.state/artifacts/workspace/bootstrap/` |
| Workspace live commands/indexes | `docs/workspace/overview.md` and `memory/current_state.md` | SQLite `workspace_messages`, `workspace_tasks`, `workspace_documents`, `workspace_topic_indexes` |
| A particular file | artifact index entry | `.state/media/` or `.state/artifacts/` |

Generated indexes are compact navigation surfaces. They may be rebuilt from
SQLite and immutable raw records and must not become the canonical data store.
