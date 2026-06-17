# MVP Architecture

`sova serve` is a lightweight local controller. It will keep Bot API long
polling active, enqueue the daily overview, and accept a run command from the
Nest `Chat` topic. One worker executes overview jobs serially.

```text
launchd
  -> sova serve
       -> Bot API controller
       -> daily scheduler
       -> SQLite job/run state
       -> single overview worker
            -> MTProto incremental sync
            -> media extractors
            -> qwen3:14b structured classification
            -> compact run bundle
            -> codex exec structured digest
            -> Nest publication
            -> calendar approval
            -> Google Calendar API
```

The controller and worker share a 15-minute run cooldown. Telegram Desktop
sessions are outside the architecture.

