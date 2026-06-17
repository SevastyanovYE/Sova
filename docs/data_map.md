# Data Map

Read this file before opening runtime data.

| Need | Read first | Full storage |
| --- | --- | --- |
| Current project state | `memory/current_state.md` | Git history and SQLite runs |
| Architecture decision | `memory/important_decisions.md` | Decision records |
| Recent Telegram content | generated `.state/index/telegram-recent.md` | `.state/raw/telegram/*.jsonl` |
| Run status or failure | generated `.state/index/runs.md` | SQLite `overview_runs`, JSONL logs |
| Extracted knowledge | generated `.state/index/facts.md` | SQLite facts/artifacts |
| Calendar candidates | generated `.state/index/calendar.md` | SQLite event candidates/events |
| A particular file | artifact index entry | `.state/media/` or `.state/artifacts/` |

Generated indexes are compact navigation surfaces. They may be rebuilt from
SQLite and immutable raw records and must not become the canonical data store.

