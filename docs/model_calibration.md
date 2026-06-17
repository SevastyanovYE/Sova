# Qwen Calibration And Tuning

## Goal

Find safe local processing limits for `qwen3:14b` before wiring it into the
daily Telegram pipeline.

The question is not only "how many messages fit". The useful limit depends on:

- total text length;
- number of messages;
- noise level;
- attachments already extracted into text;
- required output schema;
- latency and JSON validity.

## First stage: calibration

Use `sova qwen-smoke` to verify Ollama and the output schema.

Use `sova qwen-calibrate --input <jsonl>` once real message examples exist.
The input JSONL format is:

```json
{"id":"msg-1","source_ref":"telegram:chat/100","kind":"text","text":"Экзамен завтра в 10:00 в 504","extracted_text":"","attachment_count":0}
```

For images, voice, and documents, the model receives extracted text, not the
raw binary file. Raw files stay in `.state/media/` and are opened only by
specialized extractors or context-isolated agents.

Calibration records:

- batch size;
- approximate input characters;
- duration;
- JSON validity;
- count of useful/important/event candidates;
- model error, if any.

## Working defaults

- Model: `qwen3:14b`.
- Temperature: `0`.
- One Ollama request at a time.
- Start batches: `4,8,12,16,24`.
- Treat a batch as unsafe if JSON is invalid, latency is too high, or obvious
  facts disappear.

## Tuning ladder

1. **Prompt tuning**: improve instructions and examples.
2. **Threshold tuning**: tune relevance/importance cutoffs from labeled data.
3. **Dataset growth**: collect 100-300 real examples with expected labels.
4. **Fine-tune/LoRA** only if prompt and threshold tuning are still weak.

Do not fine-tune on unreviewed Telegram dumps. Labels must include source refs,
expected relevance, expected importance, expected event extraction, and notes.

