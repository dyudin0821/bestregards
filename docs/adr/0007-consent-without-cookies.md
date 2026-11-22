---
status: accepted
---

# Consent for optional browser storage, and no cookies

The site sets no cookies. Strictly necessary browser storage (the guest session, the visitor's own choice, the Turnstile and Telegram scripts' own values) is always allowed. Optional storage (the translation history and the theme) is read and written only after the visitor accepts it. The choice is shown by [vanilla-cookieconsent](https://cookieconsent.orestbida.com/), configured with `useLocalStorage: true` so that the record of consent is not a cookie either. Accept and Reject have equal weight on the first screen, because the Finnish regulator treats a harder refusal as invalid consent.

The Telegram script is loaded only when the page is opened from Telegram, so visitors of the plain website never contact telegram.org.

## Considered Options

- **`react-cookie-consent`**: stores the choice in a cookie, which contradicts the rule above. Rejected.
- **A banner of our own**: written first and then replaced. A maintained library gives a preferences dialog, keyboard and screen-reader handling and revision management (everyone is asked again when the policy changes) for the price of about 12 kB of gzipped JavaScript and 6 kB of CSS. Rejected.
- **No consent for history and theme**: a history of the visitor's texts is not strictly necessary for the service, so ePrivacy Article 5(3) and the Finnish Act 917/2014 section 205 require consent. Rejected.

## Consequences

- `web/src/storage.js` reads the library's record from `localStorage` directly (`CONSENT_KEY`, `CONSENT_REVISION`), so the storage code stays free of the DOM and testable.
- When what is stored in the browser changes, update the privacy page, the texts in `web/src/consent.js` and `CONSENT_REVISION`.
- The visual theme of the dialog is overridden through the library's CSS variables in `web/src/consent.css`.
