const HISTORY_KEY = 'bestregards.history.v1';
const THEME_KEY = 'bestregards.theme';
export const HISTORY_LIMIT = 30;

const validItem = item =>
  item && typeof item.mode === 'string' && item.mode.length <= 32 && [1, 2, 3].includes(item.level) &&
  typeof item.input === 'string' && item.input.length <= 6000 &&
  typeof item.text === 'string' && item.text.length <= 20000 &&
  Number.isFinite(item.time) && ['pair', 'ai'].includes(item.source);

export const CONSENT_KEY = 'bestregards.consent.v1';
export const OPTIONAL = 'preferences';
// Raise it when /privacy changes in a way that needs a fresh answer: everyone is asked again.
export const CONSENT_REVISION = 1;

// The history and the theme are optional: they are read and written only while the visitor's choice,
// which consent.js keeps under CONSENT_KEY, includes the optional category. The guest session, which
// the site cannot work without, and the choice itself are not optional.
function mayRemember(now = Date.now()) {
  try {
    const choice = JSON.parse(localStorage.getItem(CONSENT_KEY));
    const expired = Number.isFinite(choice?.expirationTime) && choice.expirationTime <= now;
    return Boolean(choice?.categories?.includes(OPTIONAL)) && choice.revision === CONSENT_REVISION && !expired;
  } catch { return false; }
}

// Called when the optional category is declined or withdrawn.
export function forgetOptional() {
  try {
    localStorage.removeItem(HISTORY_KEY);
    localStorage.removeItem(THEME_KEY);
  } catch { /* storage may be blocked */ }
}
export function readHistory() {
  if (!mayRemember()) return [];
  try {
    const value = JSON.parse(localStorage.getItem(HISTORY_KEY) || '[]');
    return Array.isArray(value) ? value.filter(validItem).slice(0, HISTORY_LIMIT) : [];
  } catch { return []; }
}

export function saveHistory(items) {
  if (!mayRemember()) return false;
  try { localStorage.setItem(HISTORY_KEY, JSON.stringify(items.slice(0, HISTORY_LIMIT))); return true; }
  catch { return false; }
}

const GUEST_KEY = 'bestregards.guest.v1';
const GUEST_MARGIN_MS = 60_000;

// A returning visitor keeps the same guest account, and with it the same daily allowance.
export function readGuestSession(now = Date.now()) {
  try {
    const value = JSON.parse(localStorage.getItem(GUEST_KEY));
    if (value && typeof value.token === 'string' && value.token && Number.isFinite(value.expiresAt) && value.expiresAt > now + GUEST_MARGIN_MS) {
      return {token: value.token, expiresAt: value.expiresAt};
    }
  } catch { /* storage may be blocked or corrupted */ }
  return null;
}

export function saveGuestSession(session) {
  try { localStorage.setItem(GUEST_KEY, JSON.stringify({token: session.token, expiresAt: session.expiresAt})); }
  catch { /* storage may be blocked */ }
}

export function clearGuestSession() {
  try { localStorage.removeItem(GUEST_KEY); } catch { /* storage may be blocked */ }
}

// The stored choice wins; otherwise follow the system.
export function readTheme(prefersDark = false) {
  try {
    const stored = mayRemember() ? localStorage.getItem(THEME_KEY) : null;
    if (stored === 'dark' || stored === 'light') return stored;
  } catch { /* storage may be blocked */ }
  return prefersDark ? 'dark' : 'light';
}

export function saveTheme(theme) {
  if (!mayRemember()) return;
  try { localStorage.setItem(THEME_KEY, theme); } catch { /* storage may be blocked */ }
}
