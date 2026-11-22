---
status: accepted
---

# Rewrite the backend in Go

The Express backend (about 75 lines) is replaced by a single Go binary on `net/http` that serves the built React frontend through `go:embed`. The service now includes a Telegram bot, guest sign-in, quotas and a spending ceiling and has to run as several replicas, and one static binary keeps the image and the deployment simple. Node remains only for `vite build`.

## Considered Options

- **Keep Node and extend Express**: a faster start, but the project owner chose Go as the target stack for the service. Rejected.

## Consequences

- Two toolchains (Go and Node) are needed to build the image and in CI.
