---
status: accepted
---

# Stateful backend: accounts, quotas and PostgreSQL

The original project was stateless: no accounts, no database. To bound the cost of AI translations we need a per-person daily quota and a global daily spending ceiling, and both need shared state, so the backend stores them in PostgreSQL. The database holds only an account id (a Telegram ID or a guest number, see [ADR 0004](./0004-open-site-with-guests.md)), the quota and spending counters, and pseudonymised usage events; translated texts and history stay with the user.

## Considered Options

- **SQLite**: simpler, but not designed for several replicas in Kubernetes. Rejected.
- **Stay stateless**: makes the quota and the spending ceiling impossible to count. Rejected.
- **Redis for the counters**: one more service to run; PostgreSQL does the atomic upserts the counters need. Rejected.

## Consequences

- A Telegram ID is personal data, so the service needs a privacy policy and a way to delete data ([ADR 0006](./0006-legal-frame-gdpr.md)).
