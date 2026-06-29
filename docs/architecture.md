# MVP Architecture

`sova serve` is a lightweight local controller. It keeps Bot API long polling
active, enqueue the daily overview, accepts text commands from the Nest `Status`
service topic, and accepts the pinned run button from the Nest `Chat` study
topic. One worker executes overview jobs serially.

```text
launchd
  -> sova serve
       -> Bot API controller
       -> daily scheduler
       -> Status progress updater
       -> SQLite job/run state
       -> single overview worker
            -> MTProto incremental sync
            -> media extractors
            -> qwen3:14b bounded structured classification
            -> compact model-call metrics
            -> compact run bundle
            -> codex exec structured digest
            -> Nest publication
            -> calendar approval
            -> Google Calendar API
```

The controller and worker share a 15-minute run cooldown. Telegram Desktop
sessions are outside the architecture.
