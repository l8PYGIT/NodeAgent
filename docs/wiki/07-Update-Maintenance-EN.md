**🇬🇧 English** · [🇮🇷 فارسی](07-Update-Maintenance)

# Update & maintenance (operator)

## Update
Since v0.2.2, `update` refreshes both the **binary** and the **CLI script** itself:
```bash
sudo pg-node-agent update     # on each node
sudo pg-panel update          # on the panel
```
Keys, certificate, config and database are all preserved.

Specific version:
```bash
sudo pg-node-agent update v0.4.2
```

## Update & manage the core from the web panel (no SSH)
Since node v0.5.0 and panel v0.6.0, from **Panel → Nodes → Details** you can, without SSH:
- **Start / restart / stop** the core,
- **Switch the Xray version** (the node downloads it and restarts),
- **Update the node binary** (like running the script).

> Note: "update node from the panel" only works for nodes already on **v0.5.0+**. Update a node once with the script the first time (`sudo pg-node-agent update`); afterwards the panel button works too.

> If your installed version is older than v0.2.1 (no self-update yet), refresh the script **once** by re-running `install` (keys/data are preserved):
> ```bash
> sudo bash -c "$(curl -sL https://raw.githubusercontent.com/l8PYGIT/NodeAgent/main/scripts/pg-node.sh)" @ install
> sudo bash -c "$(curl -sL https://raw.githubusercontent.com/l8PYGIT/NodePanel/main/scripts/pg-panel.sh)" @ install
> ```

## Update Xray-core
```bash
sudo pg-node-agent core-update          # latest
sudo pg-node-agent core-update v1.8.23   # specific version
```

## Renew the node certificate
```bash
sudo pg-node-agent renew-cert
```
Then re-pin the new certificate in the main panel (or, if using TOFU, delete the old record so it's re-fetched).

## Backup
- Node: the whole `/var/lib/pg-node-agent/` (includes `tenants.bolt`, `certs/`, `fixed-config.json`).
- Panel: the database `/var/lib/pg-panel/panel.db` and `/opt/pg-panel/.env` (includes the token).

## Install alongside the official PasarGuard node
`--name` separates the paths/service/CLI:
```bash
sudo bash -c "$(curl -sL .../pg-node.sh)" @ install --name pg-node-agent
```
Then manage with that name: `sudo pg-node-agent status`.

## Uninstall
```bash
sudo pg-node-agent uninstall   # data kept in /var/lib/... (remove manually)
sudo pg-panel uninstall
```
