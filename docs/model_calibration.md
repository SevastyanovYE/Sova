# Qwen Calibration And Tuning

## Goal

Keep local `qwen3:14b` useful without letting it dominate the whole overview
run. For MVP, Qwen performs bounded classification and event extraction only;
Codex writes the final digest from a compact bundle.

The target is not maximum model accuracy at any cost. The target is:

- do not miss important study items when the model is uncertain;
- keep a large text-only digest under roughly 15 minutes end-to-end;
- keep the Qwen classification stage around 6 minutes or less;
- degrade to conservative fallback instead of failing the run.

## Runtime Defaults

- Model: `qwen3:14b`.
- Temperature: `0`.
- Ollama `think:false`.
- `num_ctx`: `4096`.
- Classification `num_predict`: `1024`.
- Event extraction `num_predict`: `1536`.
- One request at a time.
- Classification batch target: up to 16 messages and about 3200 approximate
  input characters.
- Per classification batch budget: 75 seconds.
- Total classification budget: 6 minutes.
- Event extraction budget: 2 minutes total, 45 seconds per batch.

Classification asks the model only for:

```json
{"decisions":[{"id":"...","keep":true,"importance":2,"has_event":true}]}
```

Sova fills deterministic `reason` and `tags` locally after schema validation.
This keeps the model output short and reduces timeout pressure.

## Calibration Inputs

Use `sova qwen-smoke` to verify Ollama and the output schema:

```bash
go run ./cmd/sova qwen-smoke
```

Use `qwen-calibrate` with exactly one input source:

```bash
go run ./cmd/sova qwen-calibrate --input examples.jsonl
go run ./cmd/sova qwen-calibrate --run-id 6
go run ./cmd/sova qwen-calibrate --sample-db 96 --seed 42
go run ./cmd/sova qwen-calibrate --run-id 6 --model qwen3:8b
```

The `--run-id` mode uses Telegram messages created during that overview run.
The `--sample-db` mode uses a deterministic SQLite sample. Both modes retrieve
only compact message fields from SQLite and do not read raw JSONL.

The JSONL input format is:

```json
{"id":"msg-1","source_ref":"telegram:chat/100","kind":"text","text":"Экзамен завтра в 10:00 в 504","extracted_text":"","attachment_count":0}
```

For images, voice, and documents, the model receives extracted text, not raw
binary files. Raw files stay in `.state/media/`.

## Benchmark Command

Recommended bounded local check:

```bash
go run ./cmd/sova qwen-calibrate --sample-db 96 --batch-sizes 8,16,24,32 --max-duration 10m
```

For a failed/slow run:

```bash
go run ./cmd/sova qwen-calibrate --run-id RUN_ID --batch-sizes 8,16,24,32 --max-duration 10m
```

To compare retained local models on the same real run:

```bash
go run ./cmd/sova qwen-benchmark --run-id RUN_ID --models qwen3:14b,qwen3:8b --batch-sizes 8,16,24 --max-duration 30m
```

To compare model quality against a reviewed labeled set of Telegram message IDs:

```bash
go run ./cmd/sova qwen-eval --labels .state/artifacts/qwen-eval/labeled-100-20260627.jsonl --models qwen3:14b,qwen3:8b --batch-sizes 8,12,16 --max-duration 75m
```

Reports are written to:

```text
.state/artifacts/qwen-calibration-*.jsonl
.state/artifacts/qwen-benchmark-*.jsonl
.state/artifacts/qwen-eval/*.jsonl
```

Calibration records contain:

- batch size;
- input message count;
- approximate input characters;
- prompt characters;
- prompt/eval token counts when Ollama reports them;
- duration;
- JSON validity;
- kept/important/event counts;
- model error, if any.

No Telegram text or prompt body is printed in the terminal report.
Benchmark also rebuilds `.state/index/qwen-benchmark.md`.
Labeled eval rebuilds `.state/index/qwen-eval.md`.

## Labeled Eval 2026-06-27

Dataset:

- 100 reviewed Telegram message IDs stored at
  `.state/artifacts/qwen-eval/labeled-100-20260627.jsonl`.
- Labels contain IDs and expected `keep`, `importance`, and `has_event`, but no
  Telegram text.
- Mix: official study posts, homework/deadlines, schedule changes, credits,
  calendar-like messages, context-only replies, media placeholders with text,
  and noisy free chat.
- Expected positives: 72 kept, 59 important, 41 calendar-event messages.

Historical all-model run, batch size 16. The smaller rejected models were
removed locally after this eval and should only be re-pulled for a new explicit
comparison:

| Model | Valid batches | Errors | Timeouts | Duration | Important P/R | Event P/R | Pred events |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| `qwen3:14b` | 3/7 | 4 | 3 | 8m 22s | 0.95/0.32 | 0.75/0.37 | 20/41 |
| `qwen3:8b` | 7/7 | 0 | 0 | 6m 56s | 0.85/0.88 | 0.61/1.00 | 67/41 |
| `qwen3:4b` | 7/7 | 0 | 0 | 3m 57s | 0.80/0.76 | 0.51/0.93 | 75/41 |
| `gemma3:4b` | 4/7 | 3 | 0 | 3m 56s | 0.81/0.59 | 0.56/0.66 | 48/41 |
| `llama3.2:3b` | 2/7 | 5 | 0 | 1m 54s | 0.50/0.05 | 0.33/0.02 | 3/41 |

Batch-size follow-up for the two plausible models:

| Model | Batch | Valid batches | Errors | Timeouts | Duration | Important P/R | Event P/R | Pred events |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| `qwen3:14b` | 8 | 11/13 | 2 | 0 | 11m 35s | 0.91/0.53 | 0.81/0.71 | 36/41 |
| `qwen3:14b` | 12 | 3/9 | 6 | 5 | 10m 40s | 0.50/0.02 | 0.50/0.02 | 2/41 |
| `qwen3:8b` | 8 | 11/13 | 2 | 0 | 5m 18s | 0.83/0.75 | 0.63/0.88 | 57/41 |
| `qwen3:8b` | 12 | 7/9 | 2 | 0 | 4m 34s | 0.91/0.68 | 0.66/0.80 | 50/41 |

Conclusion for MVP close:

- Keep runtime default at `qwen3:14b` for now to avoid changing behavior at the
  end of the MVP cycle.
- Do not treat `qwen3:14b` as a strong quality baseline at batch 16: it timed
  out and produced many missing decisions on the labeled set. Batch 8 is safer
  but too slow for frequent experimentation.
- `qwen3:8b` is the best next candidate: batch 16 was stable, faster than 14b,
  and had full event recall, but produced many false-positive event flags.
- `qwen3:4b` is too aggressive on event flags for Calendar approval.
- `gemma3:4b` and `llama3.2:3b` are rejected for MVP due to incomplete/invalid
  batches and missed important/event messages.

Prompt/threshold backlog for the next model iteration:

- Tighten `has_event`: require a self-contained date/time/deadline or explicit
  schedule change; context-only replies like "в 17.05" should be lower
  confidence unless enough context is present in the same message.
- Split classification into two stages if needed: cheap relevance first, then
  event detection only for kept/important messages.
- Add local post-filtering for Calendar candidates: messages with only links,
  hashtags, media placeholders, or vague context should not create candidates
  without explicit date/time.
- Re-run labeled eval after prompt changes, primarily on `qwen3:8b` batch 16 and
  `qwen3:14b` batch 8.

## Operational Metrics

Overview runs also write compact model-call metrics to SQLite `model_calls`.
The agent-readable index is:

```text
.state/index/qwen-performance.md
.state/index/qwen-benchmark.md
.state/index/qwen-eval.md
```

This index is the first place to inspect for slow batches, timeouts, and fallback
counts. Do not open raw logs for performance triage unless a targeted question
requires it.

## Tuning Ladder

1. Prompt/schema tuning.
2. Batch and character-budget tuning.
3. Threshold tuning from reviewed examples.
4. Small labeled dataset of 100-300 examples.
5. Fine-tune/LoRA only if prompt and threshold tuning are still weak.

Do not fine-tune on unreviewed Telegram dumps. Labels must include source refs,
expected relevance, expected importance, expected event extraction, and notes.
