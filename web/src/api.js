export class ApiError extends Error {
  constructor(message, {status = 0, code = '', quota = null} = {}) {
    super(message);
    this.status = status;
    this.code = code;
    this.quota = quota;
  }
}

async function request(path, options) {
  let response;
  try {
    response = await fetch(path, options);
  } catch {
    throw new ApiError('Нет связи с сервером. Проверьте интернет и повторите.', {code: 'network'});
  }
  const data = await response.json().catch(() => ({}));
  if (!response.ok) {
    throw new ApiError(data.error || 'Что-то пошло не так. Попробуйте ещё раз.', {
      status: response.status, code: data.code ?? '', quota: data.quota ?? null,
    });
  }
  return data;
}

const bearer = token => ({Authorization: `Bearer ${token}`});

export const fetchPairs = () => request('/api/pairs');

export const loginWithTelegram = initData =>
  request('/api/auth/telegram', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({initData}),
  });

export const fetchConfig = () => request('/api/config');

const GUEST_ERRORS = {
  challenge_failed: 'Не удалось подтвердить, что вы не робот. Обновите страницу и попробуйте ещё раз.',
  rate_limited: 'Слишком много попыток войти. Подождите немного и попробуйте ещё раз.',
  guests_disabled: 'Перевод своих текстов на сайте сейчас недоступен. Откройте бота в Telegram.',
};

// The challenge is a Cloudflare Turnstile token; it is empty when the server has no site key.
export async function loginAsGuest(challenge) {
  try {
    return await request('/api/auth/guest', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({challenge}),
    });
  } catch (e) {
    if (!(e instanceof ApiError) || !GUEST_ERRORS[e.code]) throw e;
    throw new ApiError(GUEST_ERRORS[e.code], {status: e.status, code: e.code});
  }
}

export const fetchMe = token => request('/api/me', {headers: bearer(token)});

// keepalive lets the last batch survive the page being closed. Failures are the tracker's business.
export const sendEvents = (token, events) =>
  fetch('/api/events', {
    method: 'POST',
    keepalive: true,
    headers: {'Content-Type': 'application/json', ...(token ? bearer(token) : {})},
    body: JSON.stringify({events}),
  });

export const translateOnServer = (token, body) =>
  request('/api/translate', {
    method: 'POST',
    headers: {'Content-Type': 'application/json', ...bearer(token)},
    body: JSON.stringify(body),
  });
