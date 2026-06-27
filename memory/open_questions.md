# Open Questions

- Exact daily run time if `08:00 Europe/Moscow` should change.
- Next model iteration after MVP: try `qwen3:8b` as the main candidate with a
  stricter `has_event` prompt/post-filter, then re-run the labeled 100-message
  eval. Keep `qwen3:14b` as runtime default until that comparison improves event
  false positives without losing recall.
