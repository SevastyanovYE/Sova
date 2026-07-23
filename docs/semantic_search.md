# Semantic search

`/search <запрос>` is an Inbox-only semantic search across three isolated
scopes: the current InSync group, the legacy InSync group, and Sova.Nest.
Results are merged only after embedding and contain source, topic when known,
date, compact snippet, and a direct Telegram link.

## Index lifecycle

The initial checkpoint is explicit:

```bash
go run ./cmd/sova workspace search-index --full-scan
```

The command serializes access to the existing MTProto session with an advisory
lock, reads history in bounded pages, and persists a generation plus oldest-ID
cursor after every page. A failed or limit-bounded invocation resumes that
generation on the next run instead of starting over. Completion reconciles
deletions against the scan generation and embeds documents sequentially. It
refuses to mark a source complete if `--limit` is reached. Only after a successful pass should
`SOVA_SEARCH_ENABLED=true` be set.

While `workspace serve` is running, a five-minute recent-history pass catches
new messages, edits, and recent deletions. A persisted rotating audit cursor
also revisits one older page per pass, so edits and deletions outside the recent
window are eventually reconciled. New and edited user messages in the current Workspace are also queued immediately through Bot API updates. Search
commands themselves, service messages, file bodies, OCR, and audio transcripts
are excluded.

## Embeddings and fallback

Both documents and queries use `gemini-embedding-2` with 768 float32
dimensions. Documents use `title: ... | text: ...`; queries use
`task: search result | query: ...`. SQLite performs exact cosine search and
returns at most ten results. The prefixes and reduced dimensionality follow the
official [Gemini embeddings documentation](https://ai.google.dev/gemini-api/docs/embeddings).

Fallback changes only credentials, never the model:

1. `SOVA_GEMINI_API_KEY` (`primary`);
2. `SOVA_SEARCH_FALLBACK_GEMINI_API_KEY` (`fallback`).

This preserves one compatible vector space. Failed document embeddings remain
in the retry queue. Diagnostics store only `primary` or `fallback`, compact
redacted errors, and operational timestamps. Query text and result lists are
not persisted.
