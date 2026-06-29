# Important Decisions

- Raw data stays outside model context; agents navigate through compact indexes.
- SQLite does not enter prompts directly; only targeted query results do.
- Qwen performs bounded structured classification and extraction.
- Codex receives a compact run bundle and produces the final digest.
- No Telegram Desktop `tdata` fallback.
- The Nest `Status` topic is the service topic for text commands and operations.
- The Nest `Chat` topic is user-controlled for study materials, manual notes,
  and the pinned run button; it receives no automated pipeline output.
- The Nest Chat control button is created explicitly for manual pinning; `serve`
  must not send a new control message on every startup.
- All overview triggers share a 15-minute cooldown.
