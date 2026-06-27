package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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
	case "retry-run":
		return retryOverview(ctx, cfg, args[1:])
	case "status":
		return printStatus(ctx, cfg)
	case "index":
		return rebuildIndexes(ctx, cfg)
	case "serve":
		return controller.Serve(ctx, cfg)
	case "nest-check":
		return nestCheck(ctx, cfg, args[1:])
	case "nest-seed-topics":
		return nestSeedTopics(ctx, cfg)
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
	case "qwen-benchmark":
		return qwenBenchmark(ctx, cfg, args[1:])
	case "qwen-eval":
		return qwenEval(ctx, cfg, args[1:])
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

func retryOverview(ctx context.Context, cfg config.Config, args []string) error {
	flags := flag.NewFlagSet("retry-run", flag.ContinueOnError)
	runID := flags.Int64("id", 0, "safely retryable failed overview run ID")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *runID <= 0 {
		return fmt.Errorf("--id must be a positive overview run ID")
	}
	result, err := overview.RetryFailedRun(ctx, cfg, *runID)
	if err != nil {
		return err
	}
	fmt.Printf("overview run %d recovered; %s\n", result.RunID, result.Summary)
	fmt.Println("digest:", result.DigestPath)
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

func nestSeedTopics(ctx context.Context, cfg config.Config) error {
	if !cfg.NestReady() {
		return fmt.Errorf("Nest is not fully configured")
	}
	if err := nest.CheckTopics(cfg); err != nil {
		return err
	}
	client := nest.New(cfg.NestBotToken)
	for _, request := range nestTopicIntroRequests(cfg) {
		if err := client.SendMessage(ctx, request); err != nil {
			return err
		}
	}
	fmt.Println("sent intro messages to Chat, Digest, Calendar, and Status topics")
	return nil
}

func nestTopicIntroRequests(cfg config.Config) []nest.SendMessageRequest {
	return []nest.SendMessageRequest{
		controller.ControlMessageRequest(cfg),
		{
			ChatID:          cfg.NestChatID,
			MessageThreadID: cfg.NestTopics.Digest,
			Text:            "<b>🦉 Sova Digest</b>\n\nЗдесь появляются финальные обзоры: главное из учебных Telegram-источников, календарные намёки и ссылки на исходные сообщения.\n\n<blockquote>Этот топик только для готовых автоматических дайджестов.</blockquote>",
			ParseMode:       "HTML",
		},
		{
			ChatID:          cfg.NestChatID,
			MessageThreadID: cfg.NestTopics.Calendar,
			Text:            "<b>📅 Sova Calendar</b>\n\nСюда приходят кандидаты в Google Calendar. У каждого будут кнопки <b>Approve</b>, <b>Reject</b> и <b>Изменить дату</b>.\n\nЕсли нажать <b>Изменить дату</b>, я попрошу новую дату: <code>2026-06-28</code> или <code>2026-06-28 11:00</code>.\n\n<blockquote>Реальное событие создаётся только после Approve.</blockquote>",
			ParseMode:       "HTML",
		},
		{
			ChatID:          cfg.NestChatID,
			MessageThreadID: cfg.NestTopics.Status,
			Text:            "<b>✅ Sova Status</b>\n\nЗдесь видно, что Sova делает прямо сейчас: sync, батчи модели, извлечение календаря, Codex и публикацию.\n\nЕсли обзор идёт долго, смотри сюда: я буду обновлять одно сообщение с примерным временем и результатом.",
			ParseMode:       "HTML",
		},
	}
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
	runID := flags.Int64("run-id", 0, "use Telegram messages created during this overview run")
	sampleDB := flags.Int("sample-db", 0, "use a deterministic sample of N stored Telegram text messages")
	seed := flags.Int64("seed", 42, "deterministic sample seed")
	outputPath := flags.String("out", "", "output JSONL calibration report")
	batchSizesRaw := flags.String("batch-sizes", "", "comma-separated batch sizes, default 8,16,24,32")
	maxChars := flags.Int("max-chars", 24000, "maximum approximate chars per request")
	maxDuration := flags.Duration("max-duration", 10*time.Minute, "maximum wall-clock duration for calibration")
	model := flags.String("model", cfg.OllamaModel, "Ollama model to benchmark")
	if err := flags.Parse(args); err != nil {
		return err
	}
	sourceCount := 0
	if *inputPath != "" {
		sourceCount++
	}
	if *runID > 0 {
		sourceCount++
	}
	if *sampleDB > 0 {
		sourceCount++
	}
	if sourceCount != 1 {
		return fmt.Errorf("choose exactly one input source: --input, --run-id, or --sample-db")
	}
	inputs, err := qwenCalibrationInputs(ctx, cfg, *inputPath, *runID, *sampleDB, *seed)
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
	calibrationCtx := ctx
	cancel := func() {}
	if *maxDuration > 0 {
		calibrationCtx, cancel = context.WithTimeout(ctx, *maxDuration)
	}
	defer cancel()
	selectedModel := strings.TrimSpace(*model)
	if selectedModel == "" {
		selectedModel = cfg.OllamaModel
	}
	fmt.Printf("qwen-calibrate: model=%s inputs=%d batch_sizes=%s\n", selectedModel, len(inputs), strings.TrimSpace(*batchSizesRaw))
	results, err := qwen.RunCalibration(calibrationCtx, qwen.New(cfg.OllamaURL, selectedModel), inputs, batchSizes, *maxChars, out)
	if err != nil {
		return err
	}
	for _, result := range results {
		fmt.Printf("model=%s batch=%d messages=%d chars=%d prompt_chars=%d prompt_tokens=%d eval_tokens=%d valid=%t kept=%d important=%d events=%d duration=%dms error=%q\n",
			result.Model, result.BatchSize, result.InputMessages, result.InputChars, result.PromptChars,
			result.PromptTokens, result.EvalTokens, result.JSONValid, result.Kept,
			result.Important, result.Events, result.DurationMillis, result.Error)
	}
	fmt.Println("wrote calibration report:", out)
	return nil
}

func qwenBenchmark(ctx context.Context, cfg config.Config, args []string) error {
	flags := flag.NewFlagSet("qwen-benchmark", flag.ContinueOnError)
	runID := flags.Int64("run-id", 0, "overview run ID to use as benchmark input")
	modelsRaw := flags.String("models", "qwen3:14b,qwen3:8b,qwen3:4b,gemma3:4b,llama3.2:3b", "comma-separated Ollama model names")
	batchSizesRaw := flags.String("batch-sizes", "8,16,24", "comma-separated batch sizes")
	maxChars := flags.Int("max-chars", 24000, "maximum approximate chars per request")
	maxDuration := flags.Duration("max-duration", 30*time.Minute, "maximum wall-clock duration for the full benchmark")
	outputPath := flags.String("out", "", "output JSONL benchmark report")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *runID <= 0 {
		return fmt.Errorf("--run-id is required")
	}
	inputs, err := qwenCalibrationInputs(ctx, cfg, "", *runID, 0, 42)
	if err != nil {
		return err
	}
	models := benchmarkModels(*modelsRaw)
	batchSizes, err := qwen.ParseBatchSizes(*batchSizesRaw)
	if err != nil {
		return err
	}
	out := strings.TrimSpace(*outputPath)
	if out == "" {
		out = filepath.Join(cfg.StateDir, "artifacts", "qwen-benchmark-"+time.Now().UTC().Format("20060102T150405Z")+".jsonl")
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
		return err
	}
	if err := os.Remove(out); err != nil && !os.IsNotExist(err) {
		return err
	}
	benchmarkCtx := ctx
	cancel := func() {}
	if *maxDuration > 0 {
		benchmarkCtx, cancel = context.WithTimeout(ctx, *maxDuration)
	}
	defer cancel()

	fmt.Printf("qwen-benchmark: run=%d inputs=%d models=%s batch_sizes=%s\n",
		*runID, len(inputs), strings.Join(models, ","), strings.TrimSpace(*batchSizesRaw))
	var allResults []qwen.CalibrationResult
	for _, model := range models {
		if benchmarkCtx.Err() != nil {
			break
		}
		results, err := qwen.RunCalibration(benchmarkCtx, qwen.New(cfg.OllamaURL, model), inputs, batchSizes, *maxChars, out)
		if err != nil {
			return err
		}
		allResults = append(allResults, results...)
		for _, result := range results {
			fmt.Printf("model=%s batch=%d valid=%t duration=%dms kept=%d important=%d events=%d error=%q\n",
				result.Model, result.BatchSize, result.JSONValid, result.DurationMillis,
				result.Kept, result.Important, result.Events, result.Error)
		}
	}
	indexPath, err := writeQwenBenchmarkIndex(cfg, *runID, out, allResults, time.Now().UTC())
	if err != nil {
		return err
	}
	fmt.Println("wrote benchmark report:", out)
	fmt.Println("updated benchmark index:", indexPath)
	return nil
}

type qwenEvalLabel struct {
	ID                 string `json:"id"`
	ExpectedKeep       bool   `json:"expected_keep"`
	ExpectedImportance int    `json:"expected_importance"`
	ExpectedHasEvent   bool   `json:"expected_has_event"`
	Group              string `json:"group,omitempty"`
	Note               string `json:"note,omitempty"`
}

type qwenEvalResult struct {
	Model              string `json:"model"`
	BatchSize          int    `json:"batch_size"`
	InputMessages      int    `json:"input_messages"`
	Batches            int    `json:"batches"`
	JSONValidBatches   int    `json:"json_valid_batches"`
	BatchErrors        int    `json:"batch_errors"`
	Timeouts           int    `json:"timeouts"`
	DurationMillis     int64  `json:"duration_ms"`
	PromptTokens       int    `json:"prompt_tokens"`
	EvalTokens         int    `json:"eval_tokens"`
	ExpectedKeep       int    `json:"expected_keep"`
	ExpectedImportant  int    `json:"expected_important"`
	ExpectedEvents     int    `json:"expected_events"`
	PredictedKeep      int    `json:"predicted_keep"`
	PredictedImportant int    `json:"predicted_important"`
	PredictedEvents    int    `json:"predicted_events"`
	KeepTP             int    `json:"keep_tp"`
	KeepFP             int    `json:"keep_fp"`
	KeepFN             int    `json:"keep_fn"`
	ImportantTP        int    `json:"important_tp"`
	ImportantFP        int    `json:"important_fp"`
	ImportantFN        int    `json:"important_fn"`
	EventTP            int    `json:"event_tp"`
	EventFP            int    `json:"event_fp"`
	EventFN            int    `json:"event_fn"`
	MissingDecisions   int    `json:"missing_decisions"`
	Error              string `json:"error,omitempty"`
}

func qwenEval(ctx context.Context, cfg config.Config, args []string) error {
	flags := flag.NewFlagSet("qwen-eval", flag.ContinueOnError)
	labelsPath := flags.String("labels", "", "JSONL labels with message ids and expected keep/importance/event")
	modelsRaw := flags.String("models", "qwen3:14b,qwen3:8b,qwen3:4b,gemma3:4b,llama3.2:3b", "comma-separated Ollama model names")
	batchSizesRaw := flags.String("batch-sizes", "16", "comma-separated batch sizes")
	maxChars := flags.Int("max-chars", 24000, "maximum approximate chars per request")
	maxDuration := flags.Duration("max-duration", 45*time.Minute, "maximum wall-clock duration for the full eval")
	outputPath := flags.String("out", "", "output JSONL eval report")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*labelsPath) == "" {
		return fmt.Errorf("--labels is required")
	}
	labels, inputs, err := qwenEvalInputs(ctx, cfg, *labelsPath)
	if err != nil {
		return err
	}
	models := benchmarkModels(*modelsRaw)
	batchSizes, err := qwen.ParseBatchSizes(*batchSizesRaw)
	if err != nil {
		return err
	}
	out := strings.TrimSpace(*outputPath)
	if out == "" {
		out = filepath.Join(cfg.StateDir, "artifacts", "qwen-eval", "qwen-eval-"+time.Now().UTC().Format("20060102T150405Z")+".jsonl")
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
		return err
	}
	if err := os.Remove(out); err != nil && !os.IsNotExist(err) {
		return err
	}
	file, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	encoder := json.NewEncoder(file)
	evalCtx := ctx
	cancel := func() {}
	if *maxDuration > 0 {
		evalCtx, cancel = context.WithTimeout(ctx, *maxDuration)
	}
	defer cancel()

	fmt.Printf("qwen-eval: labels=%d models=%s batch_sizes=%s\n", len(labels), strings.Join(models, ","), strings.TrimSpace(*batchSizesRaw))
	var results []qwenEvalResult
	for _, model := range models {
		for _, batchSize := range batchSizes {
			if evalCtx.Err() != nil {
				break
			}
			result := runQwenEval(evalCtx, qwen.New(cfg.OllamaURL, model), labels, inputs, batchSize, *maxChars)
			if err := encoder.Encode(result); err != nil {
				return err
			}
			results = append(results, result)
			fmt.Printf("model=%s batch=%d valid=%d/%d errors=%d duration=%dms keep=%d/%d important=%d/%d events=%d/%d event_tp=%d fp=%d fn=%d error=%q\n",
				result.Model, result.BatchSize, result.JSONValidBatches, result.Batches, result.BatchErrors,
				result.DurationMillis, result.PredictedKeep, result.ExpectedKeep, result.PredictedImportant,
				result.ExpectedImportant, result.PredictedEvents, result.ExpectedEvents, result.EventTP,
				result.EventFP, result.EventFN, result.Error)
		}
	}
	indexPath, err := writeQwenEvalIndex(cfg, *labelsPath, out, results, time.Now().UTC())
	if err != nil {
		return err
	}
	fmt.Println("wrote eval report:", out)
	fmt.Println("updated eval index:", indexPath)
	return nil
}

func qwenEvalInputs(ctx context.Context, cfg config.Config, labelsPath string) ([]qwenEvalLabel, []qwen.MessageInput, error) {
	labels, err := loadQwenEvalLabels(labelsPath)
	if err != nil {
		return nil, nil, err
	}
	store, err := sqlitestore.Open(cfg.DatabasePath)
	if err != nil {
		return nil, nil, err
	}
	defer store.Close()
	messages, err := store.RecentTelegramMessages(ctx, 10000)
	if err != nil {
		return nil, nil, err
	}
	byID := make(map[string]sqlitestore.TelegramRecentMessage, len(messages))
	for _, message := range messages {
		byID[recentMessageID(message)] = message
	}
	inputs := make([]qwen.MessageInput, 0, len(labels))
	for _, label := range labels {
		message, ok := byID[label.ID]
		if !ok {
			return nil, nil, fmt.Errorf("label id %q was not found in SQLite telegram_messages", label.ID)
		}
		input, ok := qwenInputFromRecent(message)
		if !ok {
			return nil, nil, fmt.Errorf("label id %q has no usable text", label.ID)
		}
		inputs = append(inputs, input)
	}
	return labels, inputs, nil
}

func loadQwenEvalLabels(path string) ([]qwenEvalLabel, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	var labels []qwenEvalLabel
	seen := map[string]struct{}{}
	for {
		var label qwenEvalLabel
		if err := decoder.Decode(&label); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		label.ID = strings.TrimSpace(label.ID)
		if label.ID == "" {
			return nil, fmt.Errorf("label id is required")
		}
		if _, ok := seen[label.ID]; ok {
			return nil, fmt.Errorf("duplicate label id %q", label.ID)
		}
		if label.ExpectedImportance < 0 || label.ExpectedImportance > 3 {
			return nil, fmt.Errorf("label %q importance must be 0..3", label.ID)
		}
		seen[label.ID] = struct{}{}
		labels = append(labels, label)
	}
	if len(labels) == 0 {
		return nil, fmt.Errorf("no labels in %s", path)
	}
	return labels, nil
}

func runQwenEval(ctx context.Context, client *qwen.Client, labels []qwenEvalLabel, inputs []qwen.MessageInput, batchSize int, maxChars int) qwenEvalResult {
	result := qwenEvalResult{
		Model:         client.Model(),
		BatchSize:     batchSize,
		InputMessages: len(inputs),
	}
	predictions := map[string]qwen.MessageDecision{}
	for offset := 0; offset < len(inputs); {
		if ctx.Err() != nil {
			appendEvalError(&result, ctx.Err().Error())
			break
		}
		batch := takeEvalBatch(inputs[offset:], batchSize, maxChars)
		if len(batch) == 0 {
			appendEvalError(&result, "empty eval batch")
			break
		}
		result.Batches++
		started := time.Now()
		response, _, metrics, err := client.ClassifyBatchWithMetrics(ctx, batch)
		result.DurationMillis += time.Since(started).Milliseconds()
		result.PromptTokens += metrics.PromptEvalCount
		result.EvalTokens += metrics.EvalCount
		if err != nil {
			result.BatchErrors++
			if isTimeoutError(err.Error()) {
				result.Timeouts++
			}
			appendEvalError(&result, err.Error())
			offset += len(batch)
			continue
		}
		result.JSONValidBatches++
		for _, decision := range response.Decisions {
			predictions[decision.ID] = decision
		}
		offset += len(batch)
	}
	scoreQwenEval(labels, predictions, &result)
	return result
}

func takeEvalBatch(inputs []qwen.MessageInput, size int, maxChars int) []qwen.MessageInput {
	if size <= 0 || size > len(inputs) {
		size = len(inputs)
	}
	out := make([]qwen.MessageInput, 0, size)
	for _, input := range inputs {
		if len(out) >= size {
			break
		}
		candidate := append(out, input)
		if maxChars > 0 && qwen.ApproxChars(candidate) > maxChars && len(out) > 0 {
			break
		}
		out = candidate
	}
	return out
}

func scoreQwenEval(labels []qwenEvalLabel, predictions map[string]qwen.MessageDecision, result *qwenEvalResult) {
	for _, label := range labels {
		expectedImportant := label.ExpectedImportance >= 2
		if label.ExpectedKeep {
			result.ExpectedKeep++
		}
		if expectedImportant {
			result.ExpectedImportant++
		}
		if label.ExpectedHasEvent {
			result.ExpectedEvents++
		}
		decision, ok := predictions[label.ID]
		if !ok {
			result.MissingDecisions++
			if label.ExpectedKeep {
				result.KeepFN++
			}
			if expectedImportant {
				result.ImportantFN++
			}
			if label.ExpectedHasEvent {
				result.EventFN++
			}
			continue
		}
		predictedImportant := decision.Importance >= 2
		if decision.Keep {
			result.PredictedKeep++
		}
		if predictedImportant {
			result.PredictedImportant++
		}
		if decision.HasEvent {
			result.PredictedEvents++
		}
		result.KeepTP += boolPairTP(label.ExpectedKeep, decision.Keep)
		result.KeepFP += boolPairFP(label.ExpectedKeep, decision.Keep)
		result.KeepFN += boolPairFN(label.ExpectedKeep, decision.Keep)
		result.ImportantTP += boolPairTP(expectedImportant, predictedImportant)
		result.ImportantFP += boolPairFP(expectedImportant, predictedImportant)
		result.ImportantFN += boolPairFN(expectedImportant, predictedImportant)
		result.EventTP += boolPairTP(label.ExpectedHasEvent, decision.HasEvent)
		result.EventFP += boolPairFP(label.ExpectedHasEvent, decision.HasEvent)
		result.EventFN += boolPairFN(label.ExpectedHasEvent, decision.HasEvent)
	}
}

func boolPairTP(expected, predicted bool) int {
	if expected && predicted {
		return 1
	}
	return 0
}

func boolPairFP(expected, predicted bool) int {
	if !expected && predicted {
		return 1
	}
	return 0
}

func boolPairFN(expected, predicted bool) int {
	if expected && !predicted {
		return 1
	}
	return 0
}

func appendEvalError(result *qwenEvalResult, value string) {
	value = compactCalibrationText(value, 220)
	if value == "" {
		return
	}
	if result.Error == "" {
		result.Error = value
		return
	}
	if !strings.Contains(result.Error, value) {
		result.Error = compactCalibrationText(result.Error+"; "+value, 600)
	}
}

func writeQwenEvalIndex(cfg config.Config, labelsPath string, artifact string, results []qwenEvalResult, generatedAt time.Time) (string, error) {
	path := filepath.Join(cfg.StateDir, "index", "qwen-eval.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("# Qwen Labeled Eval\n\n")
	b.WriteString("Generated: ")
	b.WriteString(generatedAt.In(mustLocation(cfg.Timezone)).Format(time.RFC3339))
	b.WriteString("\n\n")
	b.WriteString("Labels: `")
	b.WriteString(labelsPath)
	b.WriteString("`\n\nArtifact: `")
	b.WriteString(artifact)
	b.WriteString("`\n\n")
	b.WriteString("| Model | Batch | Valid batches | Errors | Timeouts | Duration ms | Important P/R | Event P/R | Pred important | Pred events |\n")
	b.WriteString("| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |\n")
	for _, result := range results {
		b.WriteString("| `")
		b.WriteString(result.Model)
		b.WriteString("` | ")
		b.WriteString(fmt.Sprintf("%d | %d/%d | %d | %d | %d | %s/%s | %s/%s | %d/%d | %d/%d |\n",
			result.BatchSize, result.JSONValidBatches, result.Batches, result.BatchErrors, result.Timeouts,
			result.DurationMillis, ratio(result.ImportantTP, result.ImportantTP+result.ImportantFP),
			ratio(result.ImportantTP, result.ImportantTP+result.ImportantFN),
			ratio(result.EventTP, result.EventTP+result.EventFP),
			ratio(result.EventTP, result.EventTP+result.EventFN),
			result.PredictedImportant, result.ExpectedImportant, result.PredictedEvents, result.ExpectedEvents))
	}
	return path, os.WriteFile(path, []byte(b.String()), 0o600)
}

func ratio(numerator, denominator int) string {
	if denominator == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.2f", float64(numerator)/float64(denominator))
}

func benchmarkModels(value string) []string {
	models := splitCSV(value)
	seen := map[string]struct{}{}
	out := []string{}
	for _, model := range append([]string{"qwen3:14b"}, models...) {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if _, ok := seen[model]; ok {
			continue
		}
		seen[model] = struct{}{}
		out = append(out, model)
	}
	return out
}

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func writeQwenBenchmarkIndex(cfg config.Config, runID int64, artifact string, results []qwen.CalibrationResult, generatedAt time.Time) (string, error) {
	path := filepath.Join(cfg.StateDir, "index", "qwen-benchmark.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	type aggregate struct {
		Batches   int
		Valid     int
		Timeouts  int
		Duration  int64
		Kept      int
		Important int
		Events    int
	}
	order := []string{}
	byModel := map[string]*aggregate{}
	for _, result := range results {
		model := result.Model
		if model == "" {
			model = "unknown"
		}
		if _, ok := byModel[model]; !ok {
			byModel[model] = &aggregate{}
			order = append(order, model)
		}
		agg := byModel[model]
		agg.Batches++
		if result.JSONValid {
			agg.Valid++
		}
		if isTimeoutError(result.Error) {
			agg.Timeouts++
		}
		agg.Duration += result.DurationMillis
		agg.Kept += result.Kept
		agg.Important += result.Important
		agg.Events += result.Events
	}
	var b strings.Builder
	b.WriteString("# Qwen Benchmark\n\n")
	b.WriteString("Generated: ")
	b.WriteString(generatedAt.In(mustLocation(cfg.Timezone)).Format(time.RFC3339))
	b.WriteString("\n\n")
	b.WriteString("Input source: overview run `")
	b.WriteString(fmt.Sprintf("%d", runID))
	b.WriteString("`\n\n")
	b.WriteString("Artifact: `")
	b.WriteString(artifact)
	b.WriteString("`\n\n")
	b.WriteString("| Model | Batches | Valid | Timeouts | Duration ms | Kept | Important | Events |\n")
	b.WriteString("| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |\n")
	for _, model := range order {
		agg := byModel[model]
		b.WriteString("| `")
		b.WriteString(model)
		b.WriteString("` | ")
		b.WriteString(fmt.Sprintf("%d | %d | %d | %d | %d | %d | %d |\n",
			agg.Batches, agg.Valid, agg.Timeouts, agg.Duration, agg.Kept, agg.Important, agg.Events))
	}
	return path, os.WriteFile(path, []byte(b.String()), 0o600)
}

func isTimeoutError(value string) bool {
	lower := strings.ToLower(value)
	return strings.Contains(lower, "timeout") || strings.Contains(lower, "deadline exceeded")
}

func qwenCalibrationInputs(ctx context.Context, cfg config.Config, inputPath string, runID int64, sampleDB int, seed int64) ([]qwen.MessageInput, error) {
	if inputPath != "" {
		return qwen.LoadJSONL(inputPath)
	}
	store, err := sqlitestore.Open(cfg.DatabasePath)
	if err != nil {
		return nil, err
	}
	defer store.Close()
	var recent []sqlitestore.TelegramRecentMessage
	switch {
	case runID > 0:
		runRecord, ok, err := store.RunByID(ctx, runID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("overview run %d not found", runID)
		}
		if runRecord.FinishedAt == nil {
			return nil, fmt.Errorf("overview run %d has no finish time", runID)
		}
		recent, err = store.TelegramMessagesCreatedBetween(ctx, runRecord.StartedAt, *runRecord.FinishedAt)
	case sampleDB > 0:
		recent, err = store.SampleTelegramTextMessages(ctx, sampleDB, seed)
	default:
		err = fmt.Errorf("no calibration input source selected")
	}
	if err != nil {
		return nil, err
	}
	inputs := qwenInputsFromRecent(recent)
	if len(inputs) == 0 {
		return nil, fmt.Errorf("selected source has no text messages for calibration")
	}
	return inputs, nil
}

func qwenInputsFromRecent(messages []sqlitestore.TelegramRecentMessage) []qwen.MessageInput {
	inputs := make([]qwen.MessageInput, 0, len(messages))
	for _, message := range messages {
		input, ok := qwenInputFromRecent(message)
		if ok {
			inputs = append(inputs, input)
		}
	}
	return inputs
}

func qwenInputFromRecent(message sqlitestore.TelegramRecentMessage) (qwen.MessageInput, bool) {
	text := compactCalibrationText(message.Text, 700)
	if text == "" || message.Kind == "service" {
		return qwen.MessageInput{}, false
	}
	kind := message.Kind
	attachmentCount := 0
	if message.MediaType != "" {
		kind += ":" + message.MediaType
		attachmentCount = 1
	}
	return qwen.MessageInput{
		ID:              recentMessageID(message),
		SourceRef:       message.SourceRef,
		Kind:            kind,
		Text:            text,
		AttachmentCount: attachmentCount,
	}, true
}

func recentMessageID(message sqlitestore.TelegramRecentMessage) string {
	return fmt.Sprintf("telegram:%d:%d", message.ChatID, message.MessageID)
}

func compactCalibrationText(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	if limit <= 0 || len([]rune(value)) <= limit {
		return value
	}
	runes := []rune(value)
	if limit <= 12 {
		return string(runes[:limit])
	}
	head := (limit - 5) * 2 / 3
	tail := limit - 5 - head
	return string(runes[:head]) + " ... " + string(runes[len(runes)-tail:])
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
	  sova retry-run --id RUN_ID
  sova status
  sova index
  sova serve
  sova nest-check [--send-status]
  sova nest-seed-topics
  sova telegram-status
  sova telegram-login
  sova telegram-login-qr
  sova sync [--limit 100] [--dry-run]
  sova qwen-smoke
  sova qwen-calibrate --input examples.jsonl
  sova qwen-calibrate --run-id RUN_ID
  sova qwen-calibrate --sample-db 96 [--seed 42]
  sova qwen-benchmark --run-id RUN_ID --models qwen3:14b,qwen3:8b,qwen3:4b,gemma3:4b,llama3.2:3b
  sova qwen-eval --labels labels.jsonl --models qwen3:14b,qwen3:8b
  sova google-login`)
}
