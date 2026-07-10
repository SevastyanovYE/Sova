# Sova.Workspace Deploy

This deploy path runs the Workspace bot as a Linux `systemd` service.

## Server Layout

- Binary: `/opt/sova/sova`
- Working directory: `/opt/sova`
- Environment file: `/etc/sova/sova.env`
- State directory: set `SOVA_STATE_DIR=/var/lib/sova` in the env file
- Service: `sova-workspace.service`

Do not commit `.env`, bot tokens, Telegram sessions, SQLite state, raw logs, or media.

## First Install

```bash
sudo useradd --system --home /opt/sova --shell /usr/sbin/nologin sova || true
sudo mkdir -p /opt/sova /etc/sova /var/lib/sova
sudo chown -R sova:sova /opt/sova /var/lib/sova
sudo chmod 750 /opt/sova /var/lib/sova
sudo chmod 750 /etc/sova
```

Build locally for the server architecture:

```bash
GOOS=linux GOARCH=amd64 go build -o .state/build/sova-linux-amd64 ./cmd/sova
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
ssh USER@HOST 'sudo install -o root -g root -m 0644 /tmp/sova-workspace.service /etc/systemd/system/sova-workspace.service'
ssh USER@HOST 'sudo systemctl daemon-reload && sudo systemctl enable --now sova-workspace'
ssh USER@HOST 'sudo systemctl status sova-workspace --no-pager'
```

## Update

```bash
GOOS=linux GOARCH=amd64 go build -o .state/build/sova-linux-amd64 ./cmd/sova
scp .state/build/sova-linux-amd64 USER@HOST:/tmp/sova
ssh USER@HOST 'sudo systemctl stop sova-workspace && sudo install -o sova -g sova -m 0755 /tmp/sova /opt/sova/sova && sudo systemctl start sova-workspace'
ssh USER@HOST 'sudo journalctl -u sova-workspace -n 100 --no-pager'
```

## Health Check

Before switching traffic to the server, run:

```bash
ssh USER@HOST 'sudo -u sova sh -lc "set -a; . /etc/sova/sova.env; set +a; cd /opt/sova; /opt/sova/sova doctor"'
ssh USER@HOST 'sudo -u sova sh -lc "set -a; . /etc/sova/sova.env; set +a; cd /opt/sova; /opt/sova/sova workspace doctor"'
```

Stop the local `workspace serve` before starting the server service, so the same Telegram bot is not polling from two places.
