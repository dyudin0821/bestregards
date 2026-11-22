const SCRIPT_URL = 'https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit';

export const CHALLENGE_MESSAGE = 'Не удалось подтвердить, что вы не робот. Обновите страницу и попробуйте ещё раз.';

// Every failure of this module is a ChallengeError, so callers can show one friendly message.
export class ChallengeError extends Error {
  constructor(message = CHALLENGE_MESSAGE) {
    super(message);
    this.code = 'challenge_failed';
    this.status = 0;
    this.quota = null;
  }
}

export function injectScript(url, doc = document) {
  return new Promise((resolve, reject) => {
    const script = doc.createElement('script');
    script.src = url;
    script.async = true;
    script.onload = resolve;
    script.onerror = () => reject(new Error('script blocked'));
    doc.head.append(script);
  });
}

let scriptReady = null;

function loadApi(loadScript, getApi) {
  if (getApi()) return Promise.resolve(getApi());
  scriptReady ??= loadScript(SCRIPT_URL).catch(e => { scriptReady = null; throw e; });
  return scriptReady.then(() => {
    const api = getApi();
    if (!api) throw new Error('turnstile missing');
    return api;
  });
}

// Resolves with a one-time token. The widget stays invisible unless Cloudflare needs the visitor to
// act, so the container must be visible in the page. A blocked script, a widget error or a timeout
// reject with a ChallengeError instead of leaking raw errors.
export async function getChallengeToken(siteKey, {
  container, theme = 'auto', loadScript = injectScript, getApi = () => globalThis.turnstile, timeoutMs = 90_000,
} = {}) {
  let api;
  try {
    api = await loadApi(loadScript, getApi);
  } catch {
    throw new ChallengeError();
  }

  return new Promise((resolve, reject) => {
    let widgetId;
    let timer;
    const finish = (settle, value) => {
      clearTimeout(timer);
      try { if (widgetId !== undefined) api.remove(widgetId); } catch { /* already gone */ }
      settle(value);
    };
    const fail = () => finish(reject, new ChallengeError());
    timer = setTimeout(fail, timeoutMs);
    try {
      widgetId = api.render(container, {
        sitekey: siteKey,
        theme,
        appearance: 'interaction-only',
        callback: token => finish(resolve, token),
        'error-callback': fail,
        'expired-callback': fail,
      });
    } catch {
      fail();
    }
  });
}
