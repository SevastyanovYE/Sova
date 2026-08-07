# Развёртывание Sova на Google Cloud Free Tier

Production использует одну Compute Engine VM и один процесс `sova serve-all`.
Исходники, Go toolchain, Docker и Ollama на сервер не копируются.

## Конфигурация VM

- регион и зона: `us-central1`, например `us-central1-a`;
- тип: `e2-micro`, Standard provisioning;
- ОС: Debian 12 x86_64;
- диск: 10 GB `pd-standard`, без snapshot schedule;
- сеть: custom dual-stack subnet, внутренний IPv4 и ephemeral external IPv6;
- внешний IPv4, Cloud NAT, Cloud DNS, HTTP/HTTPS ingress, VPC Flow Logs и Ops
  Agent отключены;
- SSH: `tcp:22` только с IPv6 `/128` администратора и только для target tag
  `sova-ssh`;
- service account отсутствует, project-wide SSH keys заблокированы.

Консоль показывает базовую цену VM до применения Free Tier. Бесплатная квота
применяется только к одной подходящей `e2-micro` в `us-central1`, `us-east1` или
`us-west1` и к `pd-standard` в пределах квоты. Free Tier — не жёсткий лимит:
нужно следить, чтобы в проекте не появился второй платный ресурс или внешний
IPv4.

## Размещение файлов

- бинарник: `/opt/sova/sova` (`root:root`, `0755`);
- конфигурация: `/etc/sova/sova.env` (`root:sova`, `0640`);
- Google OAuth client: `/etc/sova/google-credentials.json` (`root:sova`, `0640`);
- SQLite, MTProto session, OAuth token и индексы: `/var/lib/sova` (`sova:sova`);
- unit: `deploy/systemd/sova.service`;
- проверенные резервные копии: `/var/backups/sova` и отдельная копия вне VM.

Unit задаёт `GOMEMLIMIT=700MiB`, `GOGC=75`, `MemoryMax=850M`, запускает ровно
один `serve-all` и не требует Cloud Logging.

## Проверка до запуска

Сервис должен оставаться `disabled` и `inactive`, пока старая production-копия
polling Telegram.

```bash
sudo -u sova sh -lc '
  set -a
  . /etc/sova/sova.env
  set +a
  cd /opt/sova
  /opt/sova/sova doctor --strict
  /opt/sova/sova workspace doctor --strict
  /opt/sova/sova model-smoke
'

sudo -u sova sqlite3 /var/lib/sova/sova.db 'PRAGMA quick_check;'
sudo -u sova sqlite3 /var/lib/sova/sova.db 'PRAGMA journal_mode;'
```

Ожидаются `quick_check=ok`, `journal_mode=wal` и успешный Gemini smoke именно
с Compute Engine VM.

## Переключение

1. Остановить обе старые units и убедиться, что процессов polling больше нет.
2. Выполнить `PRAGMA wal_checkpoint(TRUNCATE)`, получить `0|0|0`.
3. Создать SQLite `.backup`, проверить `quick_check`, SHA-256 и прикладные
   счётчики.
4. Повторно перенести non-DB state, конфигурацию, MTProto session и OAuth files.
5. Пока новый service остановлен, установить snapshot, проверить SHA-256,
   `quick_check`, WAL, strict doctors и `model-smoke`.
6. Запустить только `sova.service` и проверить heartbeat, `NRestarts=0`, журнал
   и память.
7. Создать post-cutover `.backup`, скачать его с VM и выполнить пробное
   восстановление в отдельный файл.

```bash
sudo systemctl enable --now sova.service
sudo systemctl is-active sova.service
sudo systemctl show sova.service -p NRestarts -p MemoryCurrent -p MemoryMax

sudo -u sova sh -lc '
  set -a
  . /etc/sova/sova.env
  set +a
  cd /opt/sova
  /opt/sova/sova healthcheck
'
```

## Откат

До подтверждения стабильности старый сервер и финальный snapshot не удалять.
Если новая VM не прошла проверки до пользовательских записей, остановить и
отключить `sova.service`, затем вернуть старые units. Если GCP уже принял новые
записи, сначала остановить GCP, снять свежий `.backup`, перенести и проверить
его на старом сервере и только затем возобновлять старый polling.

## Ежедневный обзор

Команды отправляются в учебный топик Nest `Status`:

- `/daily off` — выключить только запуск по расписанию;
- `/daily on` — включить запуск по расписанию;
- `/daily` или `/daily status` — показать состояние и следующий запуск.

Ручной `/run`, кнопка «Создать обзор» и CLI остаются доступны. Значение хранится
в SQLite (`nest_settings.daily_overview_enabled`) и переживает рестарт/переезд.
