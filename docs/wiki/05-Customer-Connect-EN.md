**🇬🇧 English** · [🇮🇷 فارسی](05-Customer-Connect)

# Connect from PasarGuard (customer)

You (the customer) have your own PasarGuard panel. You add the operator's node like a **normal node**.

## What the operator gave you
1. **Node address:** `NODE_IP` + gRPC port (default `62050`)
2. **Node certificate** (a `-----BEGIN CERTIFICATE-----` PEM block)
3. **Customer key** (API Key)

## Add the node in PasarGuard
| Field | Value |
|-------|-------|
| Address / Host | `NODE_IP` |
| Port | `62050` (or what the operator told you) |
| Protocol | **gRPC** |
| Certificate | the node certificate (PEM) |
| API Key | **the customer key** (not the master/core key) |

## After connecting
- The node should show **connected**. If your panel pushes core config, the node **ignores** it (no error) — the core config belongs to the operator.
- Users you create in your panel (vless/vmess/...) are added on the node under your tenant and appear in your own panel.
- Per-user traffic stats are returned to your panel.
- When your quota/expiry runs out, your users are disconnected and your key is rejected. After the operator renews, you reconnect.

## Critical for end-user links to work
The node has one shared real inbound (e.g. `vless-in` on port `443`). For your users' links to work:
- Your panel's inbound must match the node's real inbound in **port, protocol, network (tcp/ws/...), SNI/host and TLS settings**.
- **You don't have to guess:** the operator can click **"Customer connection"** in their panel and give you the exact inbound definition (output of `GET /subscriptions/{id}/connection` or `GET /nodes/{id}/inbounds`). Recreate that same JSON in your panel.
- The system automatically places your users on the node's real inbound, but the connection parameters in the generated link come from your panel — so they must match the node.

## Limitations
- You can't change the core config/protocol (it belongs to the operator).
- You can't stop the node.
- Quota and expiry are set by the operator; expiry is at the subscription level (not per-user).
