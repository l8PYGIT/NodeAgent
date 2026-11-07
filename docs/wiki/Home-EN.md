**🇬🇧 English** · [🇮🇷 فارسی](Home)

# Node-selling system guide (NodeAgent + NodePanel)

This wiki explains how the system works, what each role does, and the step-by-step install/connect/use flow.

## What is this?
A **node-reselling** system based on a fork of [`PasarGuard/node`](https://github.com/PasarGuard/node):

- **NodeAgent** ([repo](https://github.com/l8PYGIT/NodeAgent)): a **multi-tenant** node. One shared Xray serves many customers (tenants) with their own users; quota/expiry are enforced locally.
- **NodePanel** ([repo](https://github.com/l8PYGIT/NodePanel)): the central control panel that manages many nodes, customers, plans and subscriptions and exposes usage to a sales bot.

## Architecture at a glance
```
┌──────────┐   Bearer API     ┌───────────┐  master key (HTTPS :8090)  ┌──────────────┐
│ sales bot │ ───────────────▶ │ NodePanel  │ ─────────────────────────▶ │   NodeAgent   │
│ (wallet)  │ ◀─ webhook(HMAC) │ (fleet mgmt)│                            │ (shared Xray) │
└──────────┘                   └───────────┘                            └──────┬───────┘
                                customer's PasarGuard ── gRPC (:62050) ─────────┘
                                (creates end users)
```

- **The panel has no money logic.** Wallet/pricing/payment live in the external sales bot; the panel only manages bytes/time/status and reports usage/overage.
- Tenants are separated by their users; credential uniqueness is enforced node-wide.
- Quota/expiry → that customer's users are removed from the core and its key is rejected (suspend ≠ delete; renewal restores data).

## Roles
| Role | Who | What they do |
|------|-----|--------------|
| **Operator** | You (service owner) | Installs and manages the panel and nodes, creates plans, hands out subscriptions. |
| **Customer** | Node buyer | Connects to the node with their own PasarGuard panel and the customer key, creates end users. |
| **End user** | The customer's customer | Gets a subscription link (vless/...) from the customer. |
| **Sales bot** | External service | Uses the panel API to create subscriptions and webhooks for usage/overage. |

## Where to start
- Operator: [Install the panel](02-Install-Panel-EN) → [Install a node](03-Install-Node-EN) → [Add a node & sell](04-Add-Node-And-Sell-EN)
- Configure the node's Xray core (seller): [Configure the core](10-Configure-Core-EN)
- Customer: [Connect from PasarGuard](05-Customer-Connect-EN)
- Personal use alongside selling: [Personal use](06-Personal-Use-EN)
- Maintenance/updates: [Update & maintenance](07-Update-Maintenance-EN)
- Problems: [Troubleshooting](08-Troubleshooting-EN)
- Bot developer: [API & Webhooks](09-Sales-Bot-API-EN)
