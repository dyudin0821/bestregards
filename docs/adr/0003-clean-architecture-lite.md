---
status: accepted
---

# Clean architecture, lite: entities, use cases, adapters

The backend is split into layers whose dependencies point only inward: entities, then quota and ceiling rules, then use cases that declare the ports they need, then adapters that implement those ports, then one composition root (`internal/app`) used only by `cmd/bestregards`. The `internal/arch` test enforces the allowed imports; the current package map is in [docs/architecture.md](../architecture.md), not here.

The structure is deliberately flat: no `adapter/` or `infrastructure/` directories, every layer is a top-level package in `internal/`. The full textbook set of layers would be overkill for a small service, and ports are needed only where there is business logic (sign-in, quota, translation, admin). Ports are declared by their consumer. Domain data embedded in the binary (the catalog of ready-made pairs) is used directly, without a port.

## Considered Options

- **Logic in HTTP handlers** (as before): faster, but the sqlc `pgtype` type leaked into the HTTP layer, and sign-in rules could not be tested without HTTP. Rejected.
- **Full structure with `adapter/` and `infrastructure/`**: premature; it can be reached by moving packages if the service grows. Rejected.
