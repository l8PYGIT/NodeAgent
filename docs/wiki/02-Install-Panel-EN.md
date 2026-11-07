**🇬🇧 English** · [🇮🇷 فارسی](02-Install-Panel)

# 1) Install the panel (operator)

Install the panel on a Linux server (with systemd). A prebuilt binary is downloaded; no Docker, no on-server build.

## One-line install
```bash
sudo bash -c "$(curl -sL https://raw.githubusercontent.com/l8PYGIT/NodePanel/main/scripts/pg-panel.sh)" @ install
```

At the end it prints — **save these**:
```
Panel UI:   http://SERVER_IP:8080/
API docs:   http://SERVER_IP:8080/docs
API token (Bearer) for the sales bot:
    <TOKEN>
```

## Options
| Option | Default | Description |
|--------|---------|-------------|
| `--port PORT` | `8080` | Panel HTTP port |
| `--token TOKEN` | random | Bearer token for the API/bot |
| `--version vX.Y.Z` | latest | Install a specific release |
| `--name NAME` | `pg-panel` | Instance name (paths/service/CLI) — to install alongside another service |

## Important security note
The panel comes up over plain HTTP with no TLS. **Put it behind a TLS reverse proxy** (e.g. Caddy or Nginx) before exposing it publicly, and keep the token secret. The token grants full admin access.

## Management commands
```bash
sudo pg-panel status      # service status
sudo pg-panel logs        # live logs
sudo pg-panel restart
sudo pg-panel info        # show URL/docs/token
sudo pg-panel set-token   # rotate token (random) — or: set-token <TOKEN>
sudo pg-panel update      # update to the latest release
```

## Next
Open `http://SERVER_IP:8080/` then [Install a node](03-Install-Node-EN).
