# Telegram flattened task icon investigation

No production code was changed for this visual artifact.

The saved task emoji remains correct and reopening the chat restores its normal
shape. That points more strongly to Telegram's live-render/cache path than to
corrupted task data. Current likelihood order:

1. `answerCallbackQuery` happens after slower edit/send work, leaving the
   client in an intermediate callback state longer than necessary.
2. A keyboard-only state change is implemented through full
   `editMessageText`, forcing Telegram to re-render the emoji and markup.
3. Completed-card formatting places the emoji inside `<s>`, whose strike
   rendering can visually compress some glyphs.
4. Variation selectors and newer emoji briefly use a fallback font during the
   live update.
5. Ignored edit/callback errors make the artifact hard to correlate with a
   particular request or client state.

Useful next diagnostic, without changing behavior: capture the Telegram client
version, callback/edit timing, API responses, the exact Unicode code points,
and before/after screenshots for one reproduction. If the stored message text
and code points are identical before and after reopening, the remaining fix is
likely client-side or a narrower markup/edit workaround.
