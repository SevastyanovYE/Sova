# Nest Model Routing

## Production contract

Nest uses `SOVA_GEMINI_API_KEY` and tries `SOVA_NEST_GOOGLE_MODELS`
sequentially. The default order is:

1. `gemini-3.5-flash-lite`
2. `gemma-4-31b-it`
3. `gemini-3.1-flash-lite`
4. `gemma-4-26b-a4b-it`

This is a configured product order based on published model characteristics,
not a local benchmark. Historical `qwen-*` commands and their optional local
configuration remain for reproducibility, but the production overview runtime
does not call Ollama.
The cross-family ranking is an explicit product judgment for compact
classification and structured extraction, based on Google's published
[latest-model guidance](https://ai.google.dev/gemini-api/docs/latest-model),
[Gemini Flash-Lite description](https://deepmind.google/models/gemini/flash-lite/),
and [Gemma 4 model card](https://ai.google.dev/gemma/docs/core/model_card_4),
not on a Sova benchmark.

## Data sent to Google

Classification and calendar extraction are separate stages. Messages are
grouped by Telegram source, ordered chronologically, and assigned opaque ids.
Prompts contain bounded text, time, kind, attachment presence, and a sender
label when available. They do not contain Telegram chat/message ids or direct
links.

- Classification: at most 32 messages, approximately 24,000 characters, and
  1,200 characters per target message; 4,096 output tokens; 30-second model
  timeout.
- Events: candidates marked by classification or the local date/time heuristic;
  at most 12 targets, approximately 12,000 characters, and 2,000 characters per
  target; up to two preceding same-source messages as context; 8,192 output
  tokens; 45-second model timeout.

Both stages use temperature zero. Gemini models request minimal thinking. Model
responses must cover every opaque input id exactly once.

The final digest is a separate structured Google API stage. It tries
`SOVA_GEMINI_MODEL`, then the configured Gemini fallback models, validates the
bounded Telegram digest contract, and records `model_digest` telemetry. It does
not invoke Codex CLI.

## Failure handling

Network errors, deadlines, `404`, `408`, `429`, `5xx`, empty/malformed JSON,
unknown/duplicate ids, and incomplete results advance to the next model. A
definitive model `404` disables that model for the rest of the overview run.
Authentication/authorization errors, generic contract `400`, cancellation, and
safety blocks stop the remote route.

After all models fail, a batch is split until four classification messages or
one event target remain. The terminal classification fallback is local
`keep-all`; terminal event fallback creates no event.

## Observability and privacy

Every remote attempt stores provider, actual model, batch/attempt number,
duration, status/error class, token counts, and finish reason in `model_calls`.
Every classification decision stores its actual provider/model or local
fallback. `.state/index/model-performance.md` is the compact inspection surface;
`.state/index/qwen-performance.md` is a one-release compatibility alias.

Prompts, raw model responses, Telegram text, API keys, and session data must not
be stored in model telemetry. `model-smoke --all` checks connectivity and schema
completeness only; it is not a quality benchmark.
