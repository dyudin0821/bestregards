---
status: accepted
---

# A cheap paid AI model, not a free tier

The service calls an OpenAI-compatible API, and the recommended setup is OpenRouter with a cheap paid DeepSeek-class model (`deepseek/deepseek-v4-flash` at the time of writing), not a model with the `:free` suffix. The provider, model and prices are configuration (`AI_BASE_URL`, `AI_MODEL`, `AI_PRICE_*`), so the choice is not a lock-in.

## Considered Options

- **OpenRouter free models (`:free`)**: limited to 20 requests per minute and 1000 per day per account (50 per day until $10 of credits is bought), shared by every user, and new keys do not raise the limit. One viral spike would take the service down, and free endpoints may log prompts. Rejected.
- **Pay per request without a spending ceiling**: an open site with anonymous guests would have no cost limit. Rejected; see [ADR 0004](./0004-open-site-with-guests.md).

## Consequences

- Token prices must be kept current: the daily ceiling is computed from `AI_PRICE_*`, and stale prices distort it.
- The text of a translation leaves the service for the provider and its model host; this is disclosed in the privacy policy ([ADR 0006](./0006-legal-frame-gdpr.md)).
