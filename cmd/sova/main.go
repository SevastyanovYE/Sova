package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/SevastyanovYE/Sova/internal/config"
	"github.com/SevastyanovYE/Sova/internal/controller"
	"github.com/SevastyanovYE/Sova/internal/doctor"
	"github.com/SevastyanovYE/Sova/internal/gcalendar"
	"github.com/SevastyanovYE/Sova/internal/indexes"
	"github.com/SevastyanovYE/Sova/internal/nest"
	"github.com/SevastyanovYE/Sova/internal/overview"
	"github.com/SevastyanovYE/Sova/internal/qwen"
	sqlitestore "github.com/SevastyanovYE/Sova/internal/storage/sqlite"
	"github.com/SevastyanovYE/Sova/internal/telegrammt"
)

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		printUsage()
		return nil
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := cfg.ValidateFoundation(); err != nil {
		return err
	}

	switch args[0] {
	case "init":
		return initState(cfg)
	case "doctor":
		fmt.Print(doctor.Format(doctor.Run(ctx, cfg)))
		return nil
	case "run":
		return runOverview(ctx, cfg, args[1:])
	case "status":
		return printStatus(ctx, cfg)
	case "index":
		return rebuildIndexes(ctx, cfg)
	case "serve":
		return controller.Serve(ctx, cfg)
	case "nest-check":
		return nestCheck(ctx, cfg, args[1:])
	case "telegram-status":
		return telegramStatus(ctx, cfg)
	case "telegram-login":
		return telegramLogin(ctx, cfg)
	case "telegram-login-qr":
		return telegramLoginQR(ctx, cfg)
	case "sync":
		return telegramSync(ctx, cfg, args[1:])
	case "qwen-smoke":
		return qwenSmoke(ctx, cfg)
	case "qwen-calibrate":
		return qwenCalibrate(ctx, cfg, args[1:])
	case "google-login":
		return googleLogin(ctx, cfg)
	case "help", "-h", "--help":
		printUsage()
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func initState(cfg config.Config) error {
	for _, path := range []string{
		cfg.StateDir,
		filepath.Join(cfg.StateDir, "raw", "telegram"),
		filepath.Join(cfg.StateDir, "artifacts"),
		filepath.Join(cfg.StateDir, "media"),
		filepath.Join(cfg.StateDir, "logs"),
		filepath.Join(cfg.StateDir, "index"),
		filepath.Dir(cfg.TelegramSessionPath),
		".secrets",
	} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return err
		}
	}
	store, err := sqlitestore.Open(cfg.DatabasePath)
	if err != nil {
		return err
	}
	defer store.Close()
	fmt.Printf("initialized state at %s\n", cfg.StateDir)
	return nil
}

func runOverview(ctx context.Context, cfg config.Config, args []string) error {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	trigger := flags.String("trigger", "manual", "scheduled, nest_button, or manual")
	if err := flags.Parse(args); err != nil {
		return err
	}
	result, err := overview.Run(ctx, cfg, *trigger, overview.ProductionOptions())
	if err != nil {
		return overview.FormatRunError(err, cfg.Timezone)
	}
	fmt.Printf("overview run %d finished via %s; %s\n", result.RunID, result.Trigger, result.Summary)
	if result.BundlePath != "" {
		fmt.Println("bundle:", result.BundlePath)
	}
	if result.DigestPath != "" {
		fmt.Println("digest:", result.DigestPath)
	}
	return nil
}

func printStatus(ctx context.Context, cfg config.Config) error {
	store, err := sqlitestore.Open(cfg.DatabasePath)
	if err != nil {
		return err
	}
	defer store.Close()
	runRecord, ok, err := store.LatestRun(ctx)
	if err != nil {
		return err
	}
	if !ok {
		fmt.Println("no overview runs")
		return nil
	}
	fmt.Printf("run=%d trigger=%s status=%s started=%s summary=%q\n",
		runRecord.ID, runRecord.Trigger, runRecord.Status,
		runRecord.StartedAt.In(mustLocation(cfg.Timezone)).Format(time.RFC3339), runRecord.Summary)
	return nil
}

func rebuildIndexes(ctx context.Context, cfg config.Config) error {
	store, err := sqlitestore.Open(cfg.DatabasePath)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := indexes.Rebuild(ctx, cfg, store, time.Now().UTC()); err != nil {
		return err
	}
	fmt.Println(indexes.Summary(cfg))
	return nil
}

func nestCheck(ctx context.Context, cfg config.Config, args []string) error {
	flags := flag.NewFlagSet("nest-check", flag.ContinueOnError)
	sendStatus := flags.Bool("send-status", false, "send a test message to the Status topic")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if !cfg.NestReady() {
		return fmt.Errorf("Nest is not fully configured")
	}
	if err := nest.CheckTopics(cfg); err != nil {
		return err
	}
	client := nest.New(cfg.NestBotToken)
	user, err := client.GetMe(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("nest bot ok: @%s id=%d\n", user.Username, user.ID)
	fmt.Printf("topics ok: digest=%d calendar=%d status=%d chat=%d\n",
		cfg.NestTopics.Digest, cfg.NestTopics.Calendar, cfg.NestTopics.Status, cfg.NestTopics.Chat)
	if *sendStatus {
		if err := client.SendMessage(ctx, nest.SendMessageRequest{
			ChatID:          cfg.NestChatID,
			MessageThreadID: cfg.NestTopics.Status,
			Text:            "Sova status check: Bot API connection is working.",
		}); err != nil {
			return err
		}
		fmt.Println("sent test message to Status topic")
	}
	return nil
}

func telegramStatus(ctx context.Context, cfg config.Config) error {
	status, err := telegrammt.New(cfg).AuthStatus(ctx)
	if err != nil {
		return err
	}
	if status.Authorized {
		fmt.Println("telegram session authorized")
		return nil
	}
	if status.SessionExists {
		fmt.Println("telegram session exists but is not authorized; run `sova telegram-login`")
		return nil
	}
	fmt.Println("telegram session missing; run `sova telegram-login`")
	return nil
}

func telegramLogin(ctx context.Context, cfg config.Config) error {
	return telegrammt.New(cfg).Login(ctx, os.Stdin, os.Stdout)
}

func telegramLoginQR(ctx context.Context, cfg config.Config) error {
	return telegrammt.New(cfg).LoginQR(ctx, os.Stdin, os.Stdout)
}

func telegramSync(ctx context.Context, cfg config.Config, args []string) error {
	flags := flag.NewFlagSet("sync", flag.ContinueOnError)
	limit := flags.Int("limit", 100, "maximum recent messages to fetch per allowlisted source")
	dryRun := flags.Bool("dry-run", false, "fetch and report without writing SQLite, raw JSONL, or indexes")
	if err := flags.Parse(args); err != nil {
		return err
	}
	store, err := sqlitestore.Open(cfg.DatabasePath)
	if err != nil {
		return err
	}
	defer store.Close()

	result, err := telegrammt.New(cfg).Sync(ctx, store, telegrammt.SyncOptions{
		LimitPerSource: *limit,
		DryRun:         *dryRun,
	})
	if err != nil {
		return err
	}
	mode := "sync"
	if *dryRun {
		mode = "sync dry-run"
	}
	for _, source := range result.Sources {
		fmt.Printf("%s: %s (%s) fetched=%d new=%d inserted=%d\n",
			mode, source.SourceRef, source.Title, source.Fetched, source.New, source.Inserted)
	}
	if !*dryRun {
		fmt.Printf("updated %s\n", filepath.Join(cfg.StateDir, "index", "telegram-recent.md"))
	}
	return nil
}

func qwenSmoke(ctx context.Context, cfg config.Config) error {
	client := qwen.New(cfg.OllamaURL, cfg.OllamaModel)
	result, raw, err := client.ClassifyBatch(ctx, []qwen.MessageInput{
		{ID: "smoke-1", SourceRef: "synthetic", Kind: "text", Text: "Экзамен по ОММ завтра в 10:00 в аудитории 504."},
		{ID: "smoke-2", SourceRef: "synthetic", Kind: "text", Text: "ахахах мем смешной"},
	})
	if err != nil {
		return fmt.Errorf("%w; raw response: %s", err, raw)
	}
	for _, decision := range result.Decisions {
		fmt.Printf("%s keep=%t importance=%d event=%t reason=%s\n",
			decision.ID, decision.Keep, decision.Importance, decision.HasEvent, decision.Reason)
	}
	return nil
}

func qwenCalibrate(ctx context.Context, cfg config.Config, args []string) error {
	flags := flag.NewFlagSet("qwen-calibrate", flag.ContinueOnError)
	inputPath := flags.String("input", "", "JSONL message examples")
	outputPath := flags.String("out", "", "output JSONL calibration report")
	batchSizesRaw := flags.String("batch-sizes", "", "comma-separated batch sizes, default 4,8,12,16,24")
	maxChars := flags.Int("max-chars", 24000, "maximum approximate chars per request")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *inputPath == "" {
		return fmt.Errorf("--input is required; see docs/model_calibration.md for JSONL format")
	}
	inputs, err := qwen.LoadJSONL(*inputPath)
	if err != nil {
		return err
	}
	batchSizes, err := qwen.ParseBatchSizes(*batchSizesRaw)
	if err != nil {
		return err
	}
	out := *outputPath
	if out == "" {
		out = qwen.DefaultOutputPath(cfg.StateDir)
	}
	results, err := qwen.RunCalibration(ctx, qwen.New(cfg.OllamaURL, cfg.OllamaModel), inputs, batchSizes, *maxChars, out)
	if err != nil {
		return err
	}
	for _, result := range results {
		fmt.Printf("batch=%d messages=%d chars=%d valid=%t kept=%d important=%d events=%d duration=%dms error=%q\n",
			result.BatchSize, result.InputMessages, result.InputChars, result.JSONValid,
			result.Kept, result.Important, result.Events, result.DurationMillis, result.Error)
	}
	fmt.Println("wrote calibration report:", out)
	return nil
}

func googleLogin(ctx context.Context, cfg config.Config) error {
	return gcalendar.Login(ctx, cfg, os.Stdin, os.Stdout)
}

func mustLocation(name string) *time.Location {
	location, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return location
}

func printUsage() {
	fmt.Println(`Sova MVP

Usage:
  sova init
  sova doctor
  sova run [--trigger manual|scheduled|nest_button]
  sova status
  sova index
  sova serve
  sova nest-check [--send-status]
  sova telegram-status
  sova telegram-login
  sova telegram-login-qr
  sova sync [--limit 100] [--dry-run]
  sova qwen-smoke
  sova qwen-calibrate --input examples.jsonl
  sova google-login`)
}
