# Развёртывание Sova на alwaysdata Free

Этот путь рассчитан на managed Public Cloud без `root`, Docker и systemd. На
сервер отправляются только статический Linux-бинарник, небольшие launch/maintenance
скрипты и рабочие данные. Исходники, Go toolchain, Ollama, модели Qwen, Codex CLI,
локальный build cache и старые сборки серверу не нужны.

Официальные ограничения Free: 1 ГБ общего диска, 256 МБ RAM, 1/4 CPU и три дня
встроенных бэкапов. Сервисы обязаны работать в foreground; при Restart alwaysdata
посылает `SIGHUP`, а журналы пишет в `/home/<account>/admin/logs/services/`.
См. [тарифы](https://help.alwaysdata.com/en/docs/admin-billing/billing/public-cloud-prices/)
и [документацию Services](https://help.alwaysdata.com/en/docs/web-hosting/services/).

## Проверки до cutover

alwaysdata документирует Public Cloud как Debian x64, поэтому целевая сборка —
`linux/amd64`. SSH и Services работают на разных серверах, но выбирать `arm64`
для этого deployment без отдельного подтверждения не нужно.

1. Ответ поддержки alwaysdata о SQLite пока отмечен как `pending`. Конфигурация
   Sova на alwaysdata использует `SOVA_SQLITE_JOURNAL_MODE=DELETE`, **не WAL**, и
   один процесс `serve-all`. Всё равно нужно письменное подтверждение, что home
   storage, видимый Service, поддерживает SQLite/POSIX byte-range file locking,
   atomic rename и `fsync` для такого сценария. До ответа сохранять off-site
   backup и старый сервер как rollback. Если locking не поддерживается,
   остановиться и отдельно спроектировать PostgreSQL-адаптер; автоматически
   переключать storage нельзя.

   Текст вопроса поддержке:

   > Does the `/home/<account>` storage mounted for an Advanced Service safely
   > support SQLite POSIX byte-range locks, atomic rename and fsync when a single
   > process uses `journal_mode=DELETE` (not WAL)?
2. Через SSH проверить наличие `sqlite3`, `gzip`, `sha256sum` или `shasum`:

   ```bash
   ssh <account>@ssh-<account>.alwaysdata.net \
     'command -v sqlite3 gzip; command -v sha256sum || command -v shasum'
   ```
3. Перед настоящей выкладкой проверить, что release-бинарник содержит
   `serve-all`, `healthcheck`, heartbeat и обработку `SIGHUP`. Эти возможности
   реализованы в текущем working tree; production deploy всё равно делается
   только из проверенного commit после полного release gate.

## Что требуется сделать в панели alwaysdata

1. `Remote access → SSH/SFTP`: добавить публичный SSH-ключ. Хост имеет вид
   `ssh-<account>.alwaysdata.net`, порт `22`. Сверить fingerprint из панели при
   первом подключении. Root-доступ не нужен и не предоставляется.
2. После загрузки файлов открыть `Advanced → Services → Add a service` и создать
   ровно один сервис:

   | Поле | Значение |
   |---|---|
   | Name | `Sova` |
   | SSH user | основной пользователь `<account>` с Bash |
   | Command | `/home/<account>/sova/run-all.sh` |
   | Working directory | `sova` (панель считает относительно `/home/<account>/`) |
   | Environment | `GOMEMLIMIT=160MiB GOGC=75` |
   | Monitoring command | `/home/<account>/sova/scripts/smoke-alwaysdata.sh --root /home/<account>/sova --monitor` |
   | Paused | включено до финального cutover, затем выключить |

   Входящий порт не задавать: оба Telegram-бота используют исходящий long
   polling. Сначала оставить Service остановленным/disabled и включить только на
   шаге cutover.
3. Логи смотреть в `Advanced → Services → Logs` и
   `/home/<account>/admin/logs/services/`. Процессы — в
   `Advanced → Processes → Services`.

Monitoring запускается на стороне Service. `healthcheck` не ищет процесс через
`pgrep` на отдельном SSH-сервере: он проверяет конфигурацию/DB и свежесть
`data/state/health/heartbeat.json`, которую обновляет именно живой `serve-all`.

Один `serve-all` предпочтителен: он запускает оба контроллера в одном Go
процессе, сохраняя отдельные SQLite handles, и оставляет больше RAM под полезную
работу. Два Services — только
аварийный fallback после измерения RSS. Их команды:

- Nest: `/home/<account>/sova/run-nest.sh`;
- Workspace: `/home/<account>/sova/run-workspace.sh`.

Каждый fallback-runner имеет `GOMEMLIMIT=96MiB`; суммарный RSS всё равно может
превысить 256 МБ из-за non-Go памяти и двух runtime. Не запускать один и тот же
бот одновременно на старом и новом сервере.

## Структура и бюджет диска

```text
/home/<account>/sova/
  bin/sova
  data/state/
  data/sessions/
  data/secrets/
  backups/
  scripts/
  .env
  run-all.sh
  run-nest.sh
  run-workspace.sh
```

Локально на 2026-08-06 `.state` занимает около 654 МБ, но 551 МБ — Go build
cache, 49 МБ — локальные бинарники, 18 МБ — старый backup. Их переносить нельзя.
Нужные данные значительно меньше: SQLite около 19.6 МБ, immutable raw около
9.7 МБ, artifacts около 6.9 МБ, MTProto session и Google OAuth — несколько КБ.
После установки бинарника около 22 МБ начальный footprint остаётся с большим
запасом внутри 1 ГБ.

Не копировать на alwaysdata:

- `.state/go-build-cache/`, `.state/build/`, `.state/backups/`, старые логи;
- репозиторий `.git/`, исходники и Go modules;
- Ollama и любые model weights;
- Telegram Desktop `tdata`;
- Codex credentials.

## Локальная сборка и загрузка программы

Выполнять из корня репозитория на Mac. Для проверочного dirty build явно нужен
`--allow-dirty`; production-сборку делать только из проверенного чистого commit.

```bash
scripts/build-alwaysdata.sh --arch amd64
scripts/package-alwaysdata.sh --arch amd64
scripts/deploy-alwaysdata.sh \
  --account <account> \
  --host ssh-<account>.alwaysdata.net \
  --arch amd64 \
  --cleanup-staging
```

Deploy проверяет место и SHA-256, сохраняет текущий бинарник как
`bin/sova.previous-<UTC timestamp>` и атомарно заменяет программу. `.env`, DB,
sessions и secrets он не читает и не меняет. По умолчанию upload/staging остаются
для диагностики; удалить только созданные этой выкладкой временные файлы можно
явным `--cleanup-staging`. Старые rollback-бинарники удаляются только вручную
после успешного cutover и off-site backup.

## Конфигурация и секреты

```bash
ssh <account>@ssh-<account>.alwaysdata.net
cd /home/<account>/sova
cp .env.example .env
sed -i 's#<account>#ВАШ_АККАУНТ#g' .env
chmod 600 .env
```

Заполнить `.env` непосредственно через SSH-редактор или безопасный SFTP. Не
передавать значения токенов в аргументах команд и не присылать их в чат. Все
пути в `.env` должны быть абсолютными и вести внутрь `/home/<account>/sova/data`.
Ollama/Qwen/Codex переменных в server template нет.

`GOMEMLIMIT` читается Go runtime до запуска `main`, поэтому задаётся в поле
Environment variables alwaysdata и дублируется безопасным default в runner, а
не только в `.env`.

## Перенос состояния без мусора

Авторитетный источник — активный старый сервер (`/var/lib/sova` в текущем
systemd deployment), а не случайно устаревшая локальная `.state`. Команды ниже
с `.state/` допустимы только после свежего pre-sync со старого сервера либо если
сверено, что локальная копия актуальна. При pre-sync исключить DB и всё тяжёлое:

```bash
rsync -a --dry-run \
  --exclude '/sova.db*' \
  --exclude '/go-build-cache/' \
  --exclude '/build/' \
  --exclude '/backups/' \
  --exclude '/logs/' \
  .state/ <account>@ssh-<account>.alwaysdata.net:/home/<account>/sova/data/state/

# Повторить без --dry-run только после проверки списка.
```

Скопировать актуальную dedicated session и только необходимые Google
credentials/token. Их фактические старые пути сначала определить из
`/etc/sova/sova.env`, не печатая содержимое env в терминал или чат:

```bash
scp .sessions/sova-user.json \
  <account>@ssh-<account>.alwaysdata.net:/home/<account>/sova/data/sessions/
scp .secrets/google-calendar-client.json .secrets/google-calendar-token.json \
  <account>@ssh-<account>.alwaysdata.net:/home/<account>/sova/data/secrets/
ssh <account>@ssh-<account>.alwaysdata.net \
  'chmod 700 /home/<account>/sova/data/{state,sessions,secrets}; chmod 600 /home/<account>/sova/data/sessions/* /home/<account>/sova/data/secrets/*'
```

### Финальный cutover

1. В alwaysdata убедиться, что новый Service ещё остановлен.
2. Остановить **оба** старых процесса Nest/Workspace. После этого не писать в
   старую DB:

   ```bash
   ssh <old-user>@<old-host> \
     'sudo systemctl stop sova-nest sova-workspace && ! sudo systemctl is-active --quiet sova-nest && ! sudo systemctl is-active --quiet sova-workspace'
   ```
3. Текущий старый production использует WAL: на старом хосте выполнить
   `PRAGMA quick_check`, checkpoint и создать SQLite `.backup` (не копировать
   живой `sova.db` обычным `scp`). Пример:

   ```bash
   sudo sqlite3 /var/lib/sova/sova.db 'PRAGMA quick_check;'
   sudo sqlite3 /var/lib/sova/sova.db 'PRAGMA wal_checkpoint(TRUNCATE);'
   sudo sqlite3 /var/lib/sova/sova.db ".backup '/tmp/sova-cutover.sqlite'"
   sudo sqlite3 /tmp/sova-cutover.sqlite 'PRAGMA quick_check;'
   sudo chown <old-user> /tmp/sova-cutover.sqlite
   chmod 0600 /tmp/sova-cutover.sqlite
   ```
   Затем на Mac выполнить два отдельных копирования; приватный ключ Mac не
   передаётся на старый сервер:

   ```bash
   mkdir -p .state/migration
   scp <old-user>@<old-host>:/tmp/sova-cutover.sqlite \
     .state/migration/cutover.sqlite
   scp .state/migration/cutover.sqlite \
     <account>@ssh-<account>.alwaysdata.net:/home/<account>/sova/backups/cutover.sqlite
   ssh <account>@ssh-<account>.alwaysdata.net \
     'chmod 0600 /home/<account>/sova/backups/cutover.sqlite'
   ```
4. На alwaysdata, пока Service остановлен, установить snapshot через
   проверяемый restore. Он конвертирует установленную DB в `DELETE` mode:

   ```bash
   /home/<account>/sova/scripts/restore-alwaysdata.sh \
     --source /home/<account>/sova/backups/cutover.sqlite \
     --confirm-service-stopped --execute
   ```
5. Повторить финальный rsync каталогов без DB. Проверить quota: `du -sh
   /home/<account>/sova` и `df -h /home/<account>`.
6. Запустить offline smoke/doctor; Gemini smoke — один раз и только явно:

   ```bash
   cd /home/<account>/sova
   ./scripts/smoke-alwaysdata.sh --strict-doctors
   ./scripts/smoke-alwaysdata.sh --gemini
   ```
7. Включить один Service `Sova`, выполнить `smoke-alwaysdata.sh --monitor`,
   проверить logs, `/daily status`, `/search` и ручные безопасные сценарии Test
   Lab. Старые процессы оставить остановленными, а старый сервер — доступным для
   rollback минимум до 10 августа.
8. После стабильной работы и скачанного verified application backup удалить
   только временные cutover-копии (не рабочие DB и не последний backup):

   ```bash
   rm -f .state/migration/cutover.sqlite
   ssh <old-user>@<old-host> 'rm -f /tmp/sova-cutover.sqlite'
   ```

Важно: Public Cloud Service и SSH работают на разных серверах. Запуск
service-архитектурного ELF через SSH может быть нерепрезентативен; окончательная
проверка foreground-процесса и RSS выполняется в Service и его журнале.

## Backup и restore

Создание verified backup не печатает секреты и не вызывает Gemini:

```bash
/home/<account>/sova/scripts/backup-alwaysdata.sh
```

Скрипт делает `quick_check`, определяет фактический journal mode, выполняет
checkpoint только для WAL-источника, затем SQLite online backup, логическое
восстановление в отдельный temp-файл и повторный `quick_check`. В alwaysdata
mode — `DELETE`, поэтому WAL не предполагается. Backup сжимается и получает
SHA-256; старые копии не удаляются. Явное хранение только трёх копий:

```bash
/home/<account>/sova/scripts/backup-alwaysdata.sh --prune --keep 3
```

Перед restore остановить Service в панели:

```bash
/home/<account>/sova/scripts/restore-alwaysdata.sh \
  --source /home/<account>/sova/backups/sova-YYYYMMDDTHHMMSSZ-PID.sqlite.gz \
  --confirm-service-stopped --execute
```

Restore сначала создаёт ещё один backup текущей DB, а прежние DB/journal/WAL/SHM
перемещает в `backups/pre-restore-<timestamp>/`, а восстановленную DB переводит
в `DELETE`. Локальные `sova/backups` расходуют квоту, поэтому держать минимум и
регулярно скачивать verified `.sqlite.gz` с checksum на Mac/off-site. Встроенные
трёхдневные backups alwaysdata находятся отдельно и квоту не расходуют, но не
заменяют проверяемый application-consistent backup.

## Обновление и rollback

Для обновления: собрать чистый commit, package/deploy, выполнить backup, затем
нажать Restart. Атомарная замена не прерывает уже запущенный старый процесс до
Restart.

Rollback бинарника:

1. Остановить Service.
2. Выбрать `bin/sova.previous-<timestamp>` и проверить checksum/version на
   совместимой машине.
3. Не удаляя текущую версию, сохранить её и атомарно поставить предыдущую:

   ```bash
   cd /home/<account>/sova/bin
   stamp=$(date -u +%Y%m%dT%H%M%SZ)
   cp sova "sova.failed-$stamp"
   cp sova.previous-<timestamp> ".sova.rollback-$stamp"
   chmod 755 ".sova.rollback-$stamp"
   mv ".sova.rollback-$stamp" sova
   ```
4. Запустить Service, проверить health и logs. DB откатывать только если новая
   версия выполнила несовместимую миграцию, через gated restore выше.

## Диагностика

- Crash loop: alwaysdata после серии быстрых падений отключает Service. Не
  включать его циклически; открыть `/home/<account>/admin/logs/services/`, вручную
  запустить только offline `healthcheck`, исправить `.env`/permissions/arch.
- `exec format error`: пакет собран не для документированного `linux/amd64`;
  пересобрать с `--arch amd64`.
- `database is locked`: остановить Service, сделать backup/quick_check, проверить
  ответ alwaysdata о filesystem locking и убедиться, что `.env` содержит
  `SOVA_SQLITE_JOURNAL_MODE=DELETE`. Не удалять journal/sidecar вручную.
- Превышение памяти: смотреть RSS в `Advanced → Processes → Services`; оставить
  один `serve-all`, начать с `GOMEMLIMIT=160MiB`, не запускать full search reindex и
  тяжёлые миграции вместе с live polling. Memory limit — не абсолютный RSS cap.
- Недостаток диска: `du -ah /home/<account>/sova | sort -h | tail`; сначала
  скачать и верифицировать backup, затем только явно удалить старые upload,
  rollback-бинарники, artifacts или backups. Никогда не трогать активную DB,
  session и secrets.
- Monitoring не делает Gemini-запрос: он проверяет config/DB и freshness
  heartbeat живого процесса даже при отдельном SSH-сервере. Платный
  `model-smoke` запускается только с `--gemini` вручную.

## Управление ежедневным обзором

Из topic `Status` в Sova.Nest:

- `/daily off` — отключить только плановый ежедневный обзор;
- `/daily on` — включить его;
- `/daily` или `/daily status` — состояние и следующий запуск.

Ручные `/run`, кнопка «Создать обзор» и CLI остаются доступны. Настройка хранится
в той же SQLite DB и переносится вместе с ней.
