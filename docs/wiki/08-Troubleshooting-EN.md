**🇬🇧 English** · [🇮🇷 فارسی](08-Troubleshooting)

# Troubleshooting

## Service crash-loops / `bind: address already in use`
If `status` shows `activating (auto-restart)` with a rising restart counter, and `logs` shows:
```
server error: listen tcp :8080: bind: address already in use
```
the port is already taken — usually by a **Docker container** (e.g. an official PasarGuard panel/node) or a stray instance.
```bash
sudo ss -ltnp 'sport = :8080'      # panel
sudo ss -ltnp 'sport = :62050'     # node: gRPC
sudo ss -ltnp 'sport = :8090'      # node: HTTP control
```
- If it's `docker-proxy`: a container publishes that port. Either stop that container (`docker stop <name>`) or **change our service's port**:
  - Panel: `sudo pg-panel edit-env` → `PANEL_HTTP_ADDR=:2095`
  - Node: `sudo pg-node-agent edit-env` → `PG_AGENT_HTTP_ADDR=:8092` and `PG_AGENT_GRPC_ADDR=:62052` (and set the node's address and "gRPC port" in the panel accordingly).
- If it's a stray `pg-panel`/`pg-node-agent`: `sudo pkill -f /opt/pg-panel/pg-panel` (then `systemctl reset-failed` and `restart`).
- Note: the official PasarGuard node also uses **62050** for gRPC; to install alongside it, change our node's ports.

## Log `Failed to load API Key` or `Failed to load env file`
Harmless. It comes from the upstream base code; the script writes a dummy `API_KEY` to silence it. Multi-tenancy works regardless.

## The node shows offline in the main panel
- Check `sudo pg-node-agent status` and `logs`.
- Did you enter the address correctly? It must be `https://NODE_IP:8090` (not the gRPC one).
- Are firewall ports `8090` (control) and `62050` (gRPC) open?
- Does the pinned cert match the node's current cert? If you ran `renew-cert`, re-pin.

## The panel won't accept the certificate / TLS error when adding a node
- Paste the full `cert_pem` (with BEGIN/END lines).
- Or leave the cert field empty for automatic TOFU.
- If the node cert changed, delete and re-add the node.

## Stale UI after an update (missing buttons)
The browser cached the old `app.js`. Hard-refresh with `Ctrl+Shift+R`. (Newer panel versions send no-cache headers so this won't recur.)

## Customer connected in PasarGuard but the user link doesn't work
- The customer's inbound must **match** the node's real inbound: port, protocol, network, SNI/host, TLS. (See [Connect from PasarGuard](05-Customer-Connect-EN)).
- The node auto-derives the real inbound tags from the running config and pins users to them; you don't need to set `--force-inbounds` manually unless you want to restrict users to a subset of inbounds.
- View the node config with `sudo pg-node-agent edit` and give the exact parameters to the customer.

## Every customer user gets a config but can't connect ("EOF")
This usually means the users were added to no real inbound (the client connects but its UUID isn't registered on any inbound, so the handshake drops with EOF).
- **Common cause:** on older nodes, `PG_AGENT_FORCE_INBOUNDS=vless-in` is still pinned in the env file, but you pushed a real core config with a different tag (e.g. `vless-reality`). That stale override sends users to the non-existent `vless-in` tag.
- **Fix:** run `sudo pg-node-agent edit-env` and set `PG_AGENT_FORCE_INBOUNDS` to **empty** (or to your config's real tag), then restart. When empty, the node auto-derives the tags from the live config.
- To confirm, look for a `matched no inbound` warning in `sudo pg-node-agent logs` — it means a user landed on no inbound.

## Customer users got disconnected
- Quota/expiry likely ran out. Usage: `GET /api/v1/customers/{id}/usage`.
- Restore via `topup-quota` or `renew`. Data returns on renewal (suspend ≠ delete).

## Overage
Because Xray is shared, a little traffic may be recorded after the quota runs out, until enforcement runs. This overage is reported in `GET /customers/{id}/usage` (`overage_bytes`) so the sales bot can settle the wallet. Default enforcement interval is 10s (`PG_AGENT_ENFORCE_INTERVAL`).
