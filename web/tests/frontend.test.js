import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {loginAsGuest} from '../src/api.js';
import {findPair, normalize, pickPair} from '../src/logic.js';
import {
  CONSENT_KEY, CONSENT_REVISION, HISTORY_LIMIT, OPTIONAL, clearGuestSession, forgetOptional, readGuestSession, readHistory, readTheme,
  saveGuestSession, saveHistory, saveTheme,
} from '../src/storage.js';
import {isTelegramLaunch, loadTelegramSdk} from '../src/sdk.js';
import {CHALLENGE_MESSAGE, ChallengeError, getChallengeToken} from '../src/turnstile.js';

const catalog = JSON.parse(readFileSync(new URL('../../internal/pairs/pairs.json', import.meta.url)));

test('every catalog pair is found by its own original in its own mode only', () => {
  for (const pair of catalog.pairs) {
    assert.equal(findPair(catalog.pairs, pair.mode, pair.input)?.id, pair.id);
    assert.equal(findPair(catalog.pairs, 'no-such-mode', pair.input), undefined);
  }
});

test('normalize ignores case, quotes, trailing punctuation and spacing like the Go side', () => {
  assert.equal(normalize('  «Привет,   МИР!!» '), normalize('привет, мир'));
  assert.equal(normalize('"да" ?'), 'да');
});

test('pickPair stays inside the mode and avoids repeating the current pair', () => {
  for (const mode of catalog.modes.map(m => m.id)) {
    const first = catalog.pairs.find(p => p.mode === mode);
    for (const random of [() => 0, () => 0.5, () => 0.999]) {
      const picked = pickPair(catalog.pairs, mode, first.id, random);
      assert.equal(picked.mode, mode);
      assert.notEqual(picked.id, first.id);
    }
  }
});

const consent = (categories, extra = {}) => JSON.stringify({categories, revision: CONSENT_REVISION, ...extra});
const memoryStorage = data => ({
  getItem: key => data.get(key) ?? null,
  setItem: (key, value) => data.set(key, value),
  removeItem: key => data.delete(key),
});

test('history is bounded and malformed storage is ignored', () => {
  const data = new Map([[CONSENT_KEY, consent(['necessary', OPTIONAL])]]);
  globalThis.localStorage = memoryStorage(data);
  const entry = {input: 'Оригинал', text: 'Перевод', mode: 'boss', level: 2, source: 'pair', time: Date.now()};

  assert.equal(saveHistory(Array.from({length: HISTORY_LIMIT + 10}, () => entry)), true);
  assert.equal(readHistory().length, HISTORY_LIMIT);

  data.set('bestregards.history.v1', 'not json');
  assert.deepEqual(readHistory(), []);
  data.set('bestregards.history.v1', JSON.stringify([{}, null, {...entry, level: 99}, {...entry, source: 'x'}, entry]));
  assert.equal(readHistory().length, 1);

  globalThis.localStorage = {getItem() { throw Error('blocked'); }, setItem() { throw Error('quota'); }};
  assert.deepEqual(readHistory(), []);
  assert.equal(saveHistory([]), false);
  delete globalThis.localStorage;
});

test('theme: stored choice wins, otherwise the system preference', () => {
  const data = new Map([[CONSENT_KEY, consent(['necessary', OPTIONAL])]]);
  globalThis.localStorage = memoryStorage(data);
  assert.equal(readTheme(false), 'light');
  assert.equal(readTheme(true), 'dark');
  data.set('bestregards.theme', 'light');
  assert.equal(readTheme(true), 'light');
  globalThis.localStorage = {getItem() { throw Error('blocked'); }};
  assert.equal(readTheme(true), 'dark');
  delete globalThis.localStorage;
});

test('history and theme are saved only while the optional category is accepted', () => {
  const entry = {input: 'Оригинал', text: 'Перевод', mode: 'boss', level: 2, source: 'pair', time: Date.now()};
  const stored = {history: JSON.stringify([entry]), theme: 'dark'};
  const states = {
    'no choice yet': undefined,
    'declined': consent(['necessary']),
    'older revision': consent(['necessary', OPTIONAL], {revision: CONSENT_REVISION - 1}),
    'expired': consent(['necessary', OPTIONAL], {expirationTime: Date.now() - 1}),
    'corrupted': 'not json',
  };
  for (const [name, record] of Object.entries(states)) {
    const data = new Map([['bestregards.history.v1', stored.history], ['bestregards.theme', stored.theme]]);
    if (record !== undefined) data.set(CONSENT_KEY, record);
    globalThis.localStorage = memoryStorage(data);
    assert.deepEqual(readHistory(), [], `${name}: history is not read`);
    assert.equal(readTheme(false), 'light', `${name}: theme is not read`);
    assert.equal(saveHistory([entry]), false, `${name}: history is not written`);
    saveTheme('light');
    assert.equal(data.get('bestregards.theme'), 'dark', `${name}: theme is not written`);
  }

  const data = new Map([['bestregards.history.v1', stored.history], ['bestregards.theme', 'dark'], ['bestregards.guest.v1', '{}']]);
  globalThis.localStorage = memoryStorage(data);
  forgetOptional();
  assert.deepEqual([...data.keys()], ['bestregards.guest.v1'], 'declining wipes the optional data and keeps the guest session');
  delete globalThis.localStorage;
});

test('the Telegram SDK is requested only for a Telegram launch', async () => {
  assert.equal(isTelegramLaunch('#tgWebAppData=query_id%3D1&tgWebAppVersion=8.0', undefined), true);
  assert.equal(isTelegramLaunch('#x=1&tgWebAppData=abc', undefined), true);
  assert.equal(isTelegramLaunch('', {getItem: key => key === '__telegram__initParams' ? '{"tgWebAppData":"x"}' : null}), true);
  assert.equal(isTelegramLaunch('', {getItem: () => null}), false);
  assert.equal(isTelegramLaunch('#section', {getItem() { throw Error('blocked'); }}), false);

  const loaded = [];
  const loadScript = url => { loaded.push(url); return Promise.resolve(); };
  await loadTelegramSdk({location: {hash: ''}, sessionStorage: {getItem: () => null}}, loadScript);
  assert.deepEqual(loaded, [], 'a plain browser visit never contacts telegram.org');
  await loadTelegramSdk({location: {hash: '#tgWebAppData=abc'}, sessionStorage: {getItem: () => null}}, loadScript);
  assert.deepEqual(loaded, ['https://telegram.org/js/telegram-web-app.js']);
  await loadTelegramSdk({location: {hash: '#tgWebAppData=abc'}, sessionStorage: null}, () => Promise.reject(new Error('blocked')));
});

test('tracker batches events, caps the batch size and never throws on send failures', async () => {
  const {createTracker, newResultId} = await import('../src/events.js');
  const sent = [];
  let timers = [];
  const tracker = createTracker({
    send: batch => { sent.push(batch); return Promise.reject(new Error('offline')); },
    setTimer: fn => { timers.push(fn); return timers.length; },
    clearTimer: () => { timers = []; },
  });

  tracker.track({type: 'copied', resultId: 'a'.repeat(8)});
  tracker.track({type: 'copied', resultId: 'b'.repeat(8)});
  assert.equal(sent.length, 0, 'events wait for the timer');
  assert.equal(timers.length, 1, 'one timer for the whole batch');
  tracker.flush();
  assert.equal(sent.length, 1);
  assert.equal(sent[0].length, 2);
  assert.equal(tracker.pending(), 0);

  for (let i = 0; i < 45; i++) tracker.track({type: 'copied', resultId: String(i).padStart(8, '0')});
  tracker.flush();
  assert.deepEqual(sent.slice(1).map(b => b.length), [10, 10, 10, 10, 5], 'a request never exceeds the server limit of 10');

  const id = newResultId();
  assert.match(id, /^[a-z0-9]{8,32}$/);
  assert.notEqual(id, newResultId());
});

test('guest session: valid is restored, expired and corrupted are ignored, blocked storage never throws', () => {
  const data = new Map();
  globalThis.localStorage = {
    getItem: key => data.get(key) ?? null, setItem: (key, value) => data.set(key, value), removeItem: key => data.delete(key),
  };
  const now = Date.now();

  assert.equal(readGuestSession(now), null);
  saveGuestSession({token: 'abc', expiresAt: now + 3_600_000});
  assert.deepEqual(readGuestSession(now), {token: 'abc', expiresAt: now + 3_600_000});
  assert.equal(readGuestSession(now + 3_600_000), null, 'expired');
  assert.equal(readGuestSession(now + 3_600_000 - 30_000), null, 'about to expire counts as expired');

  for (const bad of ['not json', '{"token":1,"expiresAt":1}', '{"token":"","expiresAt":9999999999999}', '{"token":"x"}', 'null']) {
    data.set('bestregards.guest.v1', bad);
    assert.equal(readGuestSession(now), null, bad);
  }

  saveGuestSession({token: 'abc', expiresAt: now + 3_600_000});
  clearGuestSession();
  assert.equal(readGuestSession(now), null);

  globalThis.localStorage = {getItem() { throw Error('blocked'); }, setItem() { throw Error('quota'); }, removeItem() { throw Error('blocked'); }};
  assert.equal(readGuestSession(now), null);
  saveGuestSession({token: 'abc', expiresAt: now});
  clearGuestSession();
  delete globalThis.localStorage;
});

test('turnstile: a blocked script rejects with a handled challenge error', async () => {
  const blocked = () => Promise.reject(new Error('net::ERR_BLOCKED_BY_CSP'));
  await assert.rejects(
    getChallengeToken('site', {container: {}, loadScript: blocked, getApi: () => undefined}),
    e => e instanceof ChallengeError && e.code === 'challenge_failed' && e.message === CHALLENGE_MESSAGE,
  );
});

test('turnstile: resolves with the widget token and removes the widget; widget errors and timeouts reject', async () => {
  const widgets = [];
  const removed = [];
  const makeApi = behaviour => ({
    render(container, options) { widgets.push({container, options}); setTimeout(() => behaviour(options), 0); return `w${widgets.length}`; },
    remove(id) { removed.push(id); },
  });
  const run = (behaviour, extra = {}) => {
    const api = makeApi(behaviour);
    return getChallengeToken('site-key', {container: 'box', theme: 'dark', loadScript: async () => {}, getApi: () => api, ...extra});
  };

  assert.equal(await run(o => o.callback('tok')), 'tok');
  assert.equal(widgets[0].options.sitekey, 'site-key');
  assert.equal(widgets[0].options.theme, 'dark');
  assert.equal(widgets[0].container, 'box');
  assert.deepEqual(removed, ['w1']);

  await assert.rejects(run(o => o['error-callback']()), ChallengeError);
  await assert.rejects(run(() => {}, {timeoutMs: 5}), ChallengeError);
  await assert.rejects(getChallengeToken('k', {
    container: 'box', loadScript: async () => {}, getApi: () => ({render() { throw Error('bad sitekey'); }, remove() {}}),
  }), ChallengeError);
});

test('guest login maps server errors to friendly Russian messages', async () => {
  const reply = (status, body) => { globalThis.fetch = async () => ({ok: status < 400, status, json: async () => body}); };

  reply(403, {error: 'forbidden', code: 'challenge_failed'});
  await assert.rejects(loginAsGuest('t'), e => e.code === 'challenge_failed' && e.status === 403 && /не робот/.test(e.message));

  reply(429, {error: 'slow down', code: 'rate_limited'});
  await assert.rejects(loginAsGuest(''), e => e.code === 'rate_limited' && /Слишком много/.test(e.message));

  reply(404, {error: 'off', code: 'guests_disabled'});
  await assert.rejects(loginAsGuest(''), e => e.code === 'guests_disabled');

  reply(500, {error: 'Сервер упал'});
  await assert.rejects(loginAsGuest(''), e => e.message === 'Сервер упал');

  globalThis.fetch = async () => { throw new TypeError('offline'); };
  await assert.rejects(loginAsGuest(''), e => e.code === 'network');

  reply(200, {token: 'jwt', expiresIn: 60, user: {id: -1, firstName: ''}});
  assert.equal((await loginAsGuest('t')).token, 'jwt');
  delete globalThis.fetch;
});

test('landing QR: the marker becomes an SVG that paints exactly the dark modules of the code', async () => {
  const {injectQr} = await import('../qr.js');
  const {default: QRCode} = await import('qrcode');
  const url = 'https://t.me/WithBestregards_bot?startapp=landing_qr';

  const html = injectQr(`<p>a</p><div class="code" data-qr="${url}"></div><p>b</p>`);
  assert.match(html, /^<p>a<\/p><div class="code"><svg [^>]*viewBox="0 0 (\d+) \1"[^>]*><path [^>]*\/><\/svg><\/div><p>b<\/p>$/);
  assert.doesNotMatch(html, /data-qr/);

  const painted = new Set();
  for (const [, x, y, run] of html.matchAll(/M(\d+) (\d+)h(\d+)v1h-\3z/g)) {
    for (let i = 0; i < Number(run); i++) painted.add(`${Number(x) + i},${y}`);
  }
  const {size, data} = QRCode.create(url, {errorCorrectionLevel: 'L'}).modules;
  const dark = new Set();
  for (let i = 0; i < size * size; i++) if (data[i]) dark.add(`${i % size + 2},${Math.floor(i / size) + 2}`);
  assert.deepEqual(painted, dark);
});
