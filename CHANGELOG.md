# Changelog

All notable changes to Sova are documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and the project uses [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.0] - 2026-07-24

### Added

- добавили мастер цитат в Inbox и отдельный индекс цитат в «Опыт»
- добавили надёжные напоминания для отложенных задач
- добавили семантический поиск по новому и старому InSync и Sova.Nest
- добавили версионирование и защищённые от дублей релизные объявления

### Changed

- Nest классифицирует сообщения и извлекает события через упорядоченную цепочку Google-моделей
- Publish поддерживает аккуратное первичное форматирование и явно заданные свободные ревизии

### Fixed

- Publish восстанавливается после перезапуска и частичной отправки в Telegram
- ошибки доступности отдельной модели переключают обработку на следующую модель
- конфигурация поиска принимает как Bot API chat ID, так и стабильный `telegram:channel:…` source ref
- командные закрепы обновляются без дублей; отдельная справка по цитатам добавлена в «Опыт»

[Unreleased]: https://github.com/SevastyanovYE/Sova/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/SevastyanovYE/Sova/compare/0b7c4b4cd10bbab9d764a327f0e39744413ace9d...v0.1.0
