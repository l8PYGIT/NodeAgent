# Contributing to NodeAgent

NodeAgent is a **multi-tenant** node for selling node access, based on a fork of
[`PasarGuard/node`](https://github.com/PasarGuard/node). Thanks for helping out!

> Repo: https://github.com/l8PYGIT/NodeAgent · Control panel: https://github.com/l8PYGIT/NodePanel
> Full guide: the [`docs/wiki/`](docs/wiki/Home.md) folder (also published as the repo Wiki).

## Reporting issues
Open a GitHub issue and include:
- What you expected vs what actually happened.
- Relevant logs: `sudo pg-node-agent logs` (or `journalctl -u pg-node-agent`).
- Your fixed Xray config and the env you set (censor secrets: keys, certs).
- Versions: NodeAgent (release tag), Xray-core, and OS.

Do not paste master keys, core keys, customer keys or private certificates.

## Development setup
- Go (see `go.mod` for the version) and an Xray binary.
- Run the test suite before sending a PR:
  ```bash
  go build ./...
  go test ./tenant/... ./shared/... ./controller/...
  go vet ./...
  gofmt -l .          # must print nothing
  ```
  (The upstream `controller/rest` and `controller/rpc` tests need local cert
  fixtures and may fail in a bare checkout; the multi-tenant packages above are
  the ones to keep green.)
- Run locally:
  ```bash
  PG_AGENT_MASTER_KEY=master \
  PG_AGENT_FIXED_CONFIG=configs/fixed-config.example.json \
  XRAY_EXECUTABLE_PATH=/path/to/xray \
  go run ./cmd/agent
  ```

## Pull requests
- Branch off `main`; keep PRs focused and small.
- Match the existing style; keep changes idiomatic Go and run `gofmt`.
- Add/extend tests for new behavior (e.g. tenant logic, shared manager).
- Don't change the Go module path (`github.com/pasarguard/node`).
- Update `docs/wiki/` (both the Persian page and its `-EN` English counterpart)
  and the OpenAPI/README when you change behavior or endpoints.
- Releases are built by the `release.yml` workflow on a `v*` tag.

## Project structure
```
cmd/agent/             multi-tenant entrypoint
tenant/                Registry + two-level auth + enforcement (bbolt)
shared/                shared-core manager (one Xray, per-tenant add/remove)
controller/agent/      HTTP API (master/tenant) + enforcement loop
controller/grpccompat/ PasarGuard-compatible gRPC (core key + customer keys)
pkg/tlsutil/           self-signed TLS helper
backend/, common/, ... upstream base code
scripts/pg-node.sh     installer/manager (prebuilt binary + systemd)
docs/wiki/             bilingual (FA/EN) documentation
```

This is a fork; the original license is preserved in `LICENSE`.
