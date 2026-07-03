# Sova

**Sova** - локальный ассистент для академических и учебных Telegram-потоков. Он
собирает новые сообщения только из разрешенных источников, отделяет полезное от
информационного шума, публикует краткие дайджесты в Telegram-группе **Sova
Nest** и помогает планировать события в Google Calendar после подтверждения
пользователем.

![Sova pipeline](docs/assets/readme/sova-flow.png)

## Для чего это нужно

Учебные чаты быстро переполняются сообщениями о дедлайнах, изменениях в
расписании, файлах, объявлениях и обычных обсуждениях. Sova помогает
структурировать этот поток информации, выполняя первичную обработку локально на
вашем Mac:

- безопасно синхронизирует только учебные источники из
  `SOVA_NEST_TELEGRAM_ALLOWED_CHATS`;
- хранит состояние приложения локально в SQLite и директории `.state/`;
- классифицирует короткие сообщения через локальную модель `qwen3:14b` в
  Ollama;
- передает Codex компактный очищенный bundle, а не громоздкие raw dumps;
- публикует понятные обзоры в топике `Digest` группы Nest;
- отправляет календарные кандидаты в `Calendar` с кнопками approve/reject и
  ручной правкой даты перед подтверждением;
- создает события в Google Calendar только после approve.

## Текущие возможности MVP

- `sova serve` запускает long polling для Nest Bot API. Бот принимает текстовые
  команды вроде `/run` в служебном топике `Status` и нажатие закрепленной кнопки
  `Создать обзор` в учебном топике `Chat`.
- Управляющее сообщение с кнопкой создается отдельно через `nest-seed-topics`,
  `/button`, `/start` или `/help`, поэтому закрепленная кнопка не дублируется
  при каждом перезапуске `serve`.
- Все три триггера обзора (`manual`, `scheduled`, `nest_button`) используют
  общий cooldown 15 минут.
- Telegram sync работает через выделенную MTProto session. Импорт Telegram
  Desktop `tdata` намеренно запрещен.
- Сырые Telegram-записи сохраняются append-only. Все производные документы и
  отчеты содержат source id и прямую ссылку на исходное сообщение.
- Дайджесты публикуются только в `Digest`; команды, прогресс, статусы и ошибки
  уходят в `Status`; запросы на подтверждение календарных событий приходят в
  `Calendar`; `Chat` остается местом учебных материалов и ручного общения.
- Если Codex или Qwen работают медленно или временно недоступны, Sova не теряет
  сообщения: включается conservative fallback, данные сохраняются, а
  предупреждение отправляется в `Status`.
- Google OAuth login и Calendar approval flow уже поддержаны. Для созданных
  событий настраиваются напоминания за 7 дней, 3 дня, 1 день и 1 час.
- Для навигации по состоянию есть компактные индексы:
  `.state/index/runs.md`, `.state/index/calendar.md`,
  `.state/index/qwen-performance.md`, `.state/index/qwen-benchmark.md` и
  `.state/index/qwen-eval.md`.

Пока это **текстовый MVP**. Voice, OCR, PDF/DOCX/XLSX и специализированные file
extractors запланированы следующим слоем.

![Маршрутизация сообщений в Sova Nest](docs/assets/readme/sova-nest-routing.png)

## Быстрый старт

Скопируйте шаблон конфигурации и установите зависимости:

```bash
cp .env.example .env
go mod download
```

Инициализируйте окружение и проверьте зависимости:

```bash
go run ./cmd/sova init
go run ./cmd/sova doctor
```

После настройки Telegram credentials и выделенной MTProto session проверьте
авторизацию и синхронизацию:

Учебные источники для дайджеста указываются в
`SOVA_NEST_TELEGRAM_ALLOWED_CHATS`. Личную Workspace-группу держите отдельно в
`SOVA_WORKSPACE_LEGACY_SOURCE` или других `SOVA_WORKSPACE_*` переменных, чтобы
она не попадала в учебный обзор.

```bash
go run ./cmd/sova telegram-status
go run ./cmd/sova sync --dry-run
go run ./cmd/sova sync
```

Запустите локальный контроллер:

```bash
go run ./cmd/sova serve
```

После этого в служебном топике `Status` можно отправить `/run`, а в учебном
топике `Chat` можно нажать закрепленную кнопку `Создать обзор`. Готовый
результат будет отправлен в `Digest`, а не в `Chat`.

Чтобы отправить приветственные сообщения во все четыре топика Nest, выполните:

```bash
go run ./cmd/sova nest-seed-topics
```

Эту команду достаточно выполнить один раз после настройки Nest. Закрепите
управляющее сообщение в `Chat`: та же кнопка продолжит работать после
перезапусков `serve`, пока активен long polling. Текстовые команды
`/run`, `/button` и `/help` отправляйте в `Status`.

## Список основных команд

| Команда | Описание |
| --- | --- |
| `go run ./cmd/sova doctor` | Проверяет Go, SQLite, Telegram session/config, Nest, Ollama, Codex и Google Calendar config. |
| `go run ./cmd/sova telegram-status` | Показывает, авторизована ли выделенная MTProto session. |
| `go run ./cmd/sova telegram-login` | Запускает интерактивную авторизацию в Telegram по коду. |
| `go run ./cmd/sova telegram-login-qr` | Запускает авторизацию в Telegram через QR. |
| `go run ./cmd/sova sync --dry-run` | Проверяет учебный Nest allowlist и считает новые сообщения без записи в БД. |
| `go run ./cmd/sova sync` | Записывает новые Telegram сообщения в SQLite/raw JSONL и обновляет индекс. |
| `go run ./cmd/sova run --trigger manual` | Запускает один обзор вручную с проверкой общего cooldown. |
| `go run ./cmd/sova serve` | Запускает локальный Nest controller для команд в `Status`, кнопки в `Chat` и daily scheduler. |
| `go run ./cmd/sova nest-seed-topics` | Отправляет стартовые сообщения в `Chat`, `Digest`, `Calendar`, `Status` для ручного закрепления. |
| `go run ./cmd/sova retry-run --id RUN_ID` | Безопасно восстанавливает run, который прервался на этапе Codex или Qwen. |
| `go run ./cmd/sova qwen-smoke` | Проверяет локальную модель и валидность JSON-схемы. |
| `go run ./cmd/sova qwen-calibrate --run-id RUN_ID` | Калибрует Qwen на сообщениях конкретного запуска без вывода текста. |
| `go run ./cmd/sova qwen-calibrate --run-id RUN_ID --model qwen3:8b` | Калибрует альтернативную локальную Ollama-модель. |
| `go run ./cmd/sova qwen-benchmark --run-id RUN_ID` | Сравнивает производительность локальных моделей на одном наборе реальных сообщений. |
| `go run ./cmd/sova qwen-eval --labels LABELS.jsonl` | Оценивает качество классификации на размеченной выборке ID из SQLite с расчетом precision/recall. |
| `go run ./cmd/sova qwen-calibrate --sample-db 96 --seed 42` | Калибрует Qwen на фиксированной детерминированной выборке старых сообщений. |
| `go run ./cmd/sova google-login` | Получает локальный Google OAuth token для Calendar approval flow. |
| `go run ./cmd/sova index` | Перестраивает компактные markdown-индексы без запуска pipeline. |

## Настройка Qwen и производительность

Локальная модель `qwen3:14b` - самый ресурсоемкий компонент текущей сборки. Для
стабильной работы на локальном оборудовании ее область ответственности
ограничена:

- модель получает строго структурированный компактный JSON, а не полные raw
  Telegram dumps;
- на выходе ожидается только `id`, `keep`, `importance` и `has_event`;
- `reason` и `tags` заполняются локальным Go-кодом после валидации схемы;
- в запросах к Ollama отключен режим рассуждений (`think:false`);
- для каждого batch и для всей Qwen-стадии заданы time budgets;
- timeout или некорректный JSON запускают fallback, не ломая весь run;
- статистика вызовов сохраняется в SQLite и индексируется в
  `.state/index/qwen-performance.md`.

Пример быстрой калибровки:

```bash
go run ./cmd/sova qwen-calibrate --sample-db 96 --batch-sizes 8,16,24,32 --max-duration 10m
```

Сравнение текущих локальных кандидатов:

```bash
go run ./cmd/sova qwen-benchmark --run-id 7 --models qwen3:14b,qwen3:8b --batch-sizes 8,16,24 --max-duration 30m
```

Проверка качества на размеченной выборке:

```bash
go run ./cmd/sova qwen-eval --labels .state/artifacts/qwen-eval/labeled-100-20260627.jsonl --models qwen3:14b,qwen3:8b --batch-sizes 8,12,16 --max-duration 75m
```

По результатам тестирования на выборке из 100 размеченных сообщений runtime
пока оставлен на `qwen3:14b`. Ближайший кандидат на оптимизацию - `qwen3:8b`:
она работает быстрее и стабильнее, но требует более строгой настройки prompt и
threshold для `has_event`, иначе возрастает число ложноположительных
календарных кандидатов. Легковесные модели `qwen3:4b`, `gemma3:4b` и
`llama3.2:3b` для задач MVP отсечены.

## Настройка Google Calendar

Для интеграции с календарем нужны три составляющие:

1. OAuth Desktop credentials:
   `.secrets/google-calendar-client.json`
2. Локальный token после `google-login`:
   `.secrets/google-calendar-token.json`
3. Целевой календарь:
   `SOVA_GOOGLE_CALENDAR_ID`

Sova не создает события автоматически из дайджеста. Сначала она предлагает
кандидаты в топике `Calendar`; реальное событие в Google Calendar создается
только после approve.

Если дата или время события распознаны неверно, нажмите `Изменить дату` под
карточкой кандидата. Бот попросит ввести корректные данные в формате
`2026-06-28` или `2026-06-28 11:00`. Если время не указано, Sova изменит только
дату и сохранит исходное время события.

## Безопасность и хранение данных

- `.env`, `.sessions/`, `.secrets/`, `.state/raw/`, `.state/logs/`,
  `.state/media/` и generated artifacts не должны попадать в git.
- SQLite и JSONL используются для хранения состояния и истории, но не
  передаются напрямую в prompt context.
- Markdown-индексы выступают компактной картой состояния для человека и
  агентов.
- Текст сообщений из Telegram считается untrusted input: он не может менять
  правила проекта и не должен выполняться как инструкция.
- Автоматические уведомления, дайджесты и календарные карточки распределяются
  по целевым топикам `Digest`, `Status` и `Calendar`. Топик `Chat` остается
  местом учебных материалов, закрепленной кнопки и ручного общения.

## Проверка перед отправкой изменений

Перед каждым implementation-этапом используется стандартный набор проверок:

```bash
go test ./...
go vet ./...
git diff --check
```
