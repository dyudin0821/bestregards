---
status: proposed
---

# Legal frame: GDPR, and no service to countries that require local data storage

The operator is a natural person living in the EU and the server is in Finland, so the service follows the GDPR, the EU rules on browser storage and the Finnish law on electronic communications. The audience is Russian-speaking, but Russia, Kazakhstan and Uzbekistan require personal data of their citizens to be processed on technical means inside the country. We deliberately do not serve them: the privacy policy declares that the service is not addressed to them, and no data is localised there.

The status is `proposed` because the declaration is not yet backed by a technical restriction (a country block in front of the service) and the text has not been reviewed by a lawyer.

## Considered Options

- **Serve Russia as well**: needs a database in Russia for Russian users, a notification to Roskomnadzor, a separate policy and a cross-border notification for the AI provider and Cloudflare. Too heavy for a buzz project. Rejected.
- **Ignore local laws and take the risk**: a regulator can block the site. Rejected.

## Consequences

- A declaration alone is weak evidence for a Russian-language site with Russian-language promotion; a real country restriction is needed, either a Cloudflare rule in front of the service or a check inside it.
- Other Russian-speaking countries (Israel, Georgia, the Baltics, Germany, Cyprus) are served under the GDPR. Belarus, Ukraine, Armenia and Moldova have not been checked.
- The statements about Kazakhstan and Uzbekistan come from one secondary source and should be verified.
- Open gaps before a public launch: no retention period for Telegram accounts, deletion on request is manual, data transfers to the United States (AI provider, Cloudflare) are not yet checked against their contracts, no record of processing activities (GDPR Article 30), no terms of service.
- The privacy policy is `web/privacy.html`; the full list of gaps is in [docs/security.md](../security.md).
