import {injectScript} from './turnstile.js';

const SDK_URL = 'https://telegram.org/js/telegram-web-app.js';

// Telegram opens a Mini App with the launch parameters in the URL fragment and the SDK keeps them
// in sessionStorage, so a reload inside Telegram is recognised too. A visitor of the plain website
// has neither, and telegram.org never learns about the visit.
export function isTelegramLaunch(hash, storage) {
  if (/[#&]tgWebAppData=/.test(hash)) return true;
  try { return Boolean(storage?.getItem('__telegram__initParams')); } catch { return false; }
}

export async function loadTelegramSdk(win = window, loadScript = injectScript) {
  if (!isTelegramLaunch(win.location.hash, win.sessionStorage)) return;
  try { await loadScript(SDK_URL); } catch { /* the app then behaves as in a browser */ }
}
