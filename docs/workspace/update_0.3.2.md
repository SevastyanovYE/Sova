# Обновление Sova до 0.3.2

Подготовлено 07.10.2026. Код и проверки выполнены локально; сервер в этой задаче
не обновлялся. Последняя подтверждённая production-версия в памяти проекта —
0.3.1 от 26.09.2026.

## Что изменится

- Весь текст «Пора вернуться к задаче» зачёркивается при «Готово»/«Отменить».
  Ссылка внутри напоминания сохраняется. При отмене добавляется «Отменено».
- Обновляются все известные боту напоминания задачи. Старые завершённые задачи
  обрабатываются после запуска новой версии; ошибки редактирования повторяются
  с задержкой из базы. За проход обрабатывается до 100 сообщений.
- В существующем закреплённом индексе отложенных задач появляется ссылка
  «Самая давняя задача». Она учитывает только открытые задачи, исключая
  отложенные, выполненные и отменённые, по порядку первоначальных карточек
  в топике. Если остались только отложенные, отображается
  «Открытых задач пока нет».
- Обновление индекса выполняется при изменениях и при запуске/раз в минуту.
  Новые напоминания и переносы дат не меняют возраст карточки.

Старые напоминания без сохранённого Telegram ID, в том числе затёртые старой
версией при повторном откладывании на ту же дату, автоматически найти нельзя.
Новый журнал доставленных сообщений сохраняет такие повторные отправки.

## 1. На Mac: зафиксировать исходники и собрать архив

Открой обычный Терминал на Mac. Это реальная папка репозитория:

```bash
cd /Users/syway/Documents/04_Code/Sova
git status --short
git add VERSION CHANGELOG.md memory/current_state.md \
  docs/workspace/overview.md docs/workspace/data_contracts.md \
  docs/workspace/update_0.3.2.md \
  scripts/build-gcp-release.sh scripts/deploy-gcp.sh \
  internal/storage/sqlite/store.go \
  internal/storage/sqlite/workspace_task_reminders.go \
  internal/storage/sqlite/workspace_task_navigation.go \
  internal/storage/sqlite/workspace_task_navigation_test.go \
  internal/workspace/live.go internal/workspace/task_reminders.go \
  internal/workspace/task_reminders_test.go \
  internal/workspace/task_navigation.go \
  internal/workspace/task_navigation_test.go
git commit -m "fix: close task reminders and link oldest active task"
bash scripts/build-gcp-release.sh
```

Сборщик требует чистое Git-дерево, чтобы бинарник соответствовал ровно одному
коммиту. Собирается статический Linux/amd64 бинарник с версией и полным hash
коммита. Go уже установлен на этом Mac.

Получится архив:

`/Users/syway/Documents/04_Code/Sova/.state/build/sova-0.3.2-gcp.tar.gz`

Сохрани напечатанную SHA-256 архива — она понадобится на VM. В архиве есть
бинарник, установщик, метаданные версии и контрольные суммы. Для обновления
серверу передаётся этот архив. Исходники можно отдельно сохранить в GitHub:
`git push` из этой же папки отправит коммит в настроенный remote.

## 2. В браузере: загрузить архив в Cloud Shell

Открой [Google Cloud Console](https://console.cloud.google.com/compute/instances?project=project-8c5dc42c-aa6d-4533-806).
Выбери проект `project-8c5dc42c-aa6d-4533-806` и открой Cloud Shell кнопкой
терминала справа сверху.

В меню Cloud Shell выбери загрузку файла (Upload), выбери архив из шага 1 и
загрузи в домашнюю папку Cloud Shell. Скрытую папку `.state` на Mac можно
открыть в диалоге выбора файла через `Cmd+Shift+G`, вставив полный путь архива.

В терминале **Cloud Shell** выполни:

```bash
gcloud compute scp \
  --project project-8c5dc42c-aa6d-4533-806 \
  --zone us-central1-a --tunnel-through-iap \
  ~/sova-0.3.2-gcp.tar.gz sova-prod:/tmp/sova-0.3.2-gcp.tar.gz

gcloud compute ssh sova-prod \
  --project project-8c5dc42c-aa6d-4533-806 \
  --zone us-central1-a --tunnel-through-iap
```

После второй команды ты окажешься в терминале VM `sova-prod`. Если Google
попросит разрешить использование учётной записи Cloud Shell — разреши.

## 3. На VM: проверить архив и установить

Сначала на **VM**:

```bash
sha256sum /tmp/sova-0.3.2-gcp.tar.gz
```

Сравни результат с SHA-256, напечатанной на Mac: должны совпасть все символы.
Если совпали, выполни:

```bash
mkdir -p /tmp/sova-update-0.3.2
tar -xzf /tmp/sova-0.3.2-gcp.tar.gz -C /tmp/sova-update-0.3.2
cd /tmp/sova-update-0.3.2
sha256sum -c SHA256SUMS && bash install.sh
```

Должно быть четыре строки `OK`, затем проверки установки. Скрипт:

1. Проверит SHA-256 бинарника, архитектуру VM, работающий сервис и целостность базы.
2. Сохранит прежний бинарник в `/var/backups/sova`.
3. Остановит `sova.service`, сделает SQLite backup и проверит его.
4. Установит бинарник, выполнит добавочную миграцию базы и обновит/закрепит
   существующий индекс задач без `--reset`.
5. Запустит единый сервис, проверит точную версию/коммит, healthcheck, strict
   doctors, модельный smoke, целостность базы и журнал ошибок.

При ошибке после начала установки скрипт пытается вернуть прежний бинарник
и запустить сервис. Базу автоматически назад не заменяет: миграция добавляет
журнал напоминаний, который прежняя версия может игнорировать; новые записи
не теряются. Отредактированные сообщения Telegram остаются отредактированными.

Успешный конец вывода:

```text
Deployment complete
Version: sova 0.3.2 (...полный hash коммита...)
Database backup: /var/backups/sova/sova-before-0.3.2-....sqlite
Previous binary: /var/backups/sova/sova-binary-before-0.3.2-...
```

Сохрани пути `Database backup` и `Previous binary`. Если вместо успешного конца
появилась ошибка, сначала проверь состояние сервиса командами следующего шага;
не считай выкладку завершённой по одному сообщению о запуске сервиса.

## 4. На VM и в Telegram: проверить результат

На **VM**:

```bash
sudo systemctl is-active sova.service
sudo systemctl show sova.service -p ActiveState -p SubState -p NRestarts
sudo -u sova sh -lc 'set -a; . /etc/sova/sova.env; set +a; cd /opt/sova; /opt/sova/sova version; /opt/sova/sova healthcheck'
sudo journalctl -u sova.service -n 80 --no-pager
```

Ожидаются `active`, `SubState=running`, версия `0.3.2` с коммитом из сборки,
успешный healthcheck и отсутствие новых ошибок/рестартов.

В **Telegram → InSync v1.0 → Задачи**:

1. Открой закреплённый индекс отложенных задач. Наверху должна быть ссылка
   «Самая давняя задача». Она открывает первую открытую карточку, пропуская отложенные; дальше
   листай топик вниз. В истории останутся и зачёркнутые карточки.
2. На реально выполненной задаче с напоминанием нажми «Готово»: основная
   карточка и все известные напоминания должны зачеркнуться целиком.
   Ссылка из напоминания должна открывать карточку.
3. На ненужной задаче с напоминанием нажми «Отменить»: проверь зачёркивание
   и пометку «Отменено».
4. Если закрыта или отложена самая давняя задача, ссылка должна перейти
   к следующей открытой. Если открытых больше нет — исчезнуть, даже если
   отложенные ещё есть.
5. Старые напоминания уже закрытых задач должны обновиться после запуска.
   При большом количестве дождись нескольких минут. Если правка не проходит,
   журнал покажет `workspace task maintenance unavailable` или
   `workspace task reminder closure unavailable` с причиной Telegram.

## 5. Если нужен ручной откат

На **VM**, вместо `ПУТЬ_ПРЕЖНЕГО_БИНАРНИКА` вставь точный путь из вывода
успешной установки. Выполни команды по порядку, проверяя отсутствие ошибок:

```bash
sudo systemctl stop sova.service
sudo install -o root -g root -m 0755 ПУТЬ_ПРЕЖНЕГО_БИНАРНИКА /opt/sova/sova
sudo systemctl start sova.service
sudo systemctl is-active sova.service
sudo -u sova sh -lc 'set -a; . /etc/sova/sova.env; set +a; cd /opt/sova; /opt/sova/sova version; /opt/sova/sova healthcheck'
```

Не подменяй текущую базу старым backup автоматически: после запуска в неё
могли попасть новые задачи и сообщения. Backup — дополнительная точка
восстановления для отдельного разбора.

## 6. После успешной ручной проверки: завершить релиз

Это завершающий порядок из `docs/releasing.md`. Команда объявления ниже
отправляет релизное сообщение в Inbox только при явном `--execute`.

На **VM**, после проверки сервиса, журнала и ручных сценариев:

```bash
sudo -u sova sh -lc 'set -a; . /etc/sova/sova.env; set +a; cd /opt/sova; /opt/sova/sova workspace record-deployment --checks "tests, doctors, journals, smoke" --execute'
sudo -u sova sh -lc 'set -a; . /etc/sova/sova.env; set +a; cd /opt/sova; /opt/sova/sova workspace announce-release'
```

На **Mac** поставь тег на тот самый коммит, чей hash показан в серверном
`version`. Подставь его вместо `КОММИТ_ИЗ_VERSION`:

```bash
cd /Users/syway/Documents/04_Code/Sova
git tag v0.3.2 КОММИТ_ИЗ_VERSION
git push
git push origin v0.3.2
```

Затем на **VM**, если preview объявления устраивает:

```bash
sudo -u sova sh -lc 'set -a; . /etc/sova/sova.env; set +a; cd /opt/sova; /opt/sova/sova workspace announce-release --execute'
```

Не записывай успешный receipt, не ставь тег и не отправляй объявление,
если проверка установки или ручные сценарии не прошли.
