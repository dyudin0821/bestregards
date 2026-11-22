---
status: accepted
---

# Open site with guests, no payments

The product's goal is buzz and a "wow" (people should forward the link), not revenue. Sign-in through Telegram only is friction for an audience that arrives from LinkedIn and TikTok: a person follows a link, wants to try it in ten seconds, and is asked to open a bot. Telegram Stars payments cost a lot of code and risk (money, refunds, forged payments) for revenue that is not a goal. So the site is open to everyone through anonymous guest accounts behind a Cloudflare Turnstile check, the Mini App stays as a second entry with a larger quota, and payments are removed entirely.

## Considered Options

- **Keep Telegram-only sign-in and Stars payments**: does not remove the friction for the outside audience and keeps payment code nobody needs. Rejected.
- **Sign-in on the site through the Telegram Login Widget**: still requires a Telegram account, so the friction stays. Rejected.
- **Anonymous translations without accounts, per-IP limit only**: offices and mobile networks share IPs, and a per-person quota cannot be counted. A guest with a token is simpler and more accurate. Rejected.

## Consequences

- Guests can be farmed: anyone who passes Turnstile gets one, and clearing browser data gives a new quota (accepted). The real cost limit is the daily spending ceiling and the per-IP limits; guests share at most 40% of the hourly ceiling, and an administrator can switch guests off.
- There is no revenue: the owner pays for AI translations within the ceiling. This is the deliberate price of audience growth.
- A dependency on Cloudflare appears: the Turnstile script and check, and the guest's IP goes to Cloudflare. A wrong `TURNSTILE_SECRET` breaks guest sign-in.
- The old decision to sell translations for Telegram Stars is not restored: this one replaces it.
