# Releasing Sova

Sova uses Semantic Versioning and keeps human-readable changes in
`CHANGELOG.md`. A release is a cohesive, production-verified deployment, not a
commit counter.

## When to release

Prepare a release when at least one of these is true:

- a complete user-visible workflow is ready;
- stored data or production behavior changes materially;
- a coherent group of daily-use fixes has passed its acceptance checks.

Documentation-only edits, unfinished work, and isolated refactors remain under
`Unreleased` unless they are independently deployment-worthy.

## Required gate

1. Move reviewed `Unreleased` entries into the target version section.
2. Ensure `VERSION`, the changelog version, and the build version agree.
3. Run `go test ./...`, `go vet ./...`, and `git diff --check`.
4. Run feature-specific smoke tests without sending production messages.
5. Back up the production SQLite database.
6. Build the exact commit with embedded version and commit metadata.
7. Deploy the configured production runtime: the current GCP production uses
   one `sova.service` running `serve-all`; the two-unit systemd and alwaysdata
   paths are legacy-only.
8. Run strict doctors, inspect the active service logs/journals, and complete manual smoke
   flows in Test Lab.
9. Record a successful production deployment receipt for the exact commit.
10. Tag that deployed commit, then dry-run and explicitly execute the Inbox
    release announcement.

If any check fails, do not tag or announce the release. Fix the failure and run
the gate again from the start. Service startup must never send release notes.

The receipt and announcement commands are dry-run by default:

```bash
sova doctor --strict
sova workspace doctor --strict
sova workspace record-deployment --checks "tests, doctors, journals, smoke" --execute
sova workspace announce-release
git tag "v$(tr -d '[:space:]' < VERSION)"
sova workspace announce-release --execute
```

Record the receipt only after every configured production Service and its logs
were checked on the server. The tag and `--execute` announcement must point at that
same embedded commit; neither command belongs in service startup.

## Build

```bash
version=$(tr -d '[:space:]' < VERSION)
commit=$(git rev-parse HEAD)
go build -ldflags "-X github.com/SevastyanovYE/Sova/internal/buildinfo.Version=$version -X github.com/SevastyanovYE/Sova/internal/buildinfo.Commit=$commit" -o .state/build/sova ./cmd/sova
```

For the Linux server, prefix the final command with
`GOOS=linux GOARCH=amd64`.

## Release notes

Release text is rendered from the checked-in changelog. Git supplies the exact
commit, file, insertion, and deletion counts from the previous tag; a model
must not invent them. The announcement command is dry-run by default and must
deduplicate the version within an environment before any Telegram send, even
if a tag was moved to another commit.
