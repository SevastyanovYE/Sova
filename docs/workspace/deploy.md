# Sova.Workspace Deploy

This legacy deploy path runs Nest and Workspace as separate Linux `systemd`
services. Low-memory production hosts should use the single
`deploy/systemd/sova.service` unit documented in `deploy/gcp/README.md` so both
controllers share one `sova serve-all` process.

## Server Layout

- Binary: `/opt/sova/sova`
- Working directory: `/opt/sova`
- Environment file: `/etc/sova/sova.env`
- State directory: set `SOVA_STATE_DIR=/var/lib/sova` in the env file
- Services: `sova-workspace.service`, `sova-nest.service`

Do not commit `.env`, bot tokens, Telegram sessions, SQLite state, raw logs, or media.

## First Install

```bash
sudo useradd --system --home /opt/sova --shell /usr/sbin/nologin sova || true
sudo mkdir -p /opt/sova /etc/sova /var/lib/sova
sudo chown -R sova:sova /opt/sova /var/lib/sova
sudo chmod 750 /opt/sova /var/lib/sova
sudo chmod 750 /etc/sova
```

Build the exact release commit for the server architecture:

```bash
version=$(tr -d '[:space:]' < VERSION)
commit=$(git rev-parse HEAD)
GOOS=linux GOARCH=amd64 go build \
  -ldflags "-X github.com/SevastyanovYE/Sova/internal/buildinfo.Version=$version -X github.com/SevastyanovYE/Sova/internal/buildinfo.Commit=$commit" \
  -o .state/build/sova-linux-amd64 ./cmd/sova
```

Copy the binary and environment file:

```bash
scp .state/build/sova-linux-amd64 USER@HOST:/tmp/sova
scp /path/to/local/sova.env USER@HOST:/tmp/sova.env
ssh USER@HOST 'sudo install -o sova -g sova -m 0755 /tmp/sova /opt/sova/sova'
ssh USER@HOST 'sudo install -o root -g sova -m 0640 /tmp/sova.env /etc/sova/sova.env'
```

Install and start the service:

```bash
scp deploy/systemd/sova-workspace.service USER@HOST:/tmp/sova-workspace.service
scp deploy/systemd/sova-nest.service USER@HOST:/tmp/sova-nest.service
ssh USER@HOST 'sudo install -o root -g root -m 0644 /tmp/sova-workspace.service /etc/systemd/system/sova-workspace.service'
ssh USER@HOST 'sudo install -o root -g root -m 0644 /tmp/sova-nest.service /etc/systemd/system/sova-nest.service'
ssh USER@HOST 'sudo systemctl daemon-reload && sudo systemctl enable --now sova-workspace sova-nest'
ssh USER@HOST 'sudo systemctl status sova-workspace sova-nest --no-pager'
```

## Update

```bash
version=$(tr -d '[:space:]' < VERSION)
commit=$(git rev-parse HEAD)
GOOS=linux GOARCH=amd64 go build \
  -ldflags "-X github.com/SevastyanovYE/Sova/internal/buildinfo.Version=$version -X github.com/SevastyanovYE/Sova/internal/buildinfo.Commit=$commit" \
  -o .state/build/sova-linux-amd64 ./cmd/sova
scp .state/build/sova-linux-amd64 USER@HOST:/tmp/sova
ssh USER@HOST 'sudo systemctl stop sova-workspace sova-nest'
ssh USER@HOST 'sudo cp /var/lib/sova/sova.db /var/lib/sova/sova.db.backup-$(date +%Y%m%dT%H%M%S)'
ssh USER@HOST 'sudo install -o sova -g sova -m 0755 /tmp/sova /opt/sova/sova'
ssh USER@HOST 'sudo -u sova sh -lc "set -a; . /etc/sova/sova.env; set +a; cd /opt/sova; /opt/sova/sova init"'
ssh USER@HOST 'sudo systemctl start sova-workspace sova-nest'
ssh USER@HOST 'sudo journalctl -u sova-workspace -u sova-nest -n 150 --no-pager'
```

## Health Check

Before switching traffic to the server, run:

```bash
ssh USER@HOST 'sudo -u sova sh -lc "set -a; . /etc/sova/sova.env; set +a; cd /opt/sova; /opt/sova/sova doctor --strict"'
ssh USER@HOST 'sudo -u sova sh -lc "set -a; . /etc/sova/sova.env; set +a; cd /opt/sova; /opt/sova/sova workspace doctor --strict"'
```

Stop the local `workspace serve` before starting the server service, so the same Telegram bot is not polling from two places.
