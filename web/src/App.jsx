import {useCallback, useEffect, useRef, useState} from 'react';
import {Eraser, Plus} from 'lucide-react';
import {ApiError, fetchConfig, fetchMe, fetchPairs, loginAsGuest, loginWithTelegram, sendEvents, translateOnServer} from './api.js';
import {createTracker, newResultId} from './events.js';
import {findPair, pickPair} from './logic.js';
import {
  HISTORY_LIMIT, clearGuestSession, readGuestSession, readHistory, readTheme, saveGuestSession, saveHistory, saveTheme,
} from './storage.js';
import {onOptionalChange, showPreferences} from './consent.js';
import {
  applyChrome, haptic, inTelegram, initData, onThemeChanged, showBackButton, showMainButton, startTelegram, telegramScheme,
} from './telegram.js';
import {getChallengeToken} from './turnstile.js';
import {
  Brand, ErrorBox, History, LevelSwitch, LimitMemo, LoadingMemo, Memo, ModeSwitch, QuotaMeter, RandomButton, satireNotice, statuses,
} from './components.jsx';

const DEFAULT_MODE = 'colleagues';
const MAX_LENGTH = 6000;
const BOT_LINK = 'https://t.me/WithBestregards_bot?startapp=web';

const initialTheme = () => telegramScheme ?? readTheme(matchMedia('(prefers-color-scheme: dark)').matches);
const pairResult = (pair, preview) => ({source: 'pair', pairId: pair.id, resultId: newResultId(), text: pair.output, input: pair.input, mode: pair.mode, preview});

export default function App() {
  const [catalog, setCatalog] = useState(null);
  const [loadError, setLoadError] = useState('');
  const [mode, setMode] = useState(DEFAULT_MODE);
  const [text, setText] = useState('');
  const [level, setLevel] = useState(2);
  const [result, setResult] = useState(null);
  const [view, setView] = useState('compose');
  const [notice, setNotice] = useState('');
  const [needTelegram, setNeedTelegram] = useState(false);
  const [copied, setCopied] = useState(false);
  const [history, setHistory] = useState(readHistory);
  const [theme, setTheme] = useState(initialTheme);
  const [session, setSession] = useState(null);
  const [quota, setQuota] = useState(null);
  const [authError, setAuthError] = useState('');
  const [loading, setLoading] = useState(false);
  const [statusIndex, setStatusIndex] = useState(0);
  const [error, setError] = useState('');
  const [limited, setLimited] = useState(false);
  const challengeRef = useRef(null);
  const sourceRef = useRef(null);
  const configRef = useRef(null);

  // A translation that finishes after the user moved on must not replace what they see now.
  const requestId = useRef(0);
  const lastPrevious = useRef('');

  // Usage metric: a ready-made pair counts as translated when the user presses "translate" or
  // copies it; AI translations are counted by the server. No text ever leaves in an event.
  const tokenRef = useRef(null);
  const tracker = useRef(null);
  if (!tracker.current) tracker.current = createTracker({send: batch => sendEvents(tokenRef.current, batch)});
  const counted = useRef(new Set());
  const copiedIds = useRef(new Set());
  const trackResult = (type, r, lvl) => tracker.current.track({type, kind: r.source, mode: r.mode, level: lvl, resultId: r.resultId});
  const countPair = (r, lvl) => {
    if (r.source !== 'pair' || r.untracked || counted.current.has(r.resultId)) return;
    counted.current.add(r.resultId);
    trackResult('translate_done', r, lvl);
  };
  useEffect(() => {
    const onHide = () => { if (document.visibilityState === 'hidden') tracker.current.flush(); };
    document.addEventListener('visibilitychange', onHide);
    return () => document.removeEventListener('visibilitychange', onHide);
  }, []);

  const pairs = catalog?.pairs ?? [];
  const modes = catalog?.modes ?? [];
  const isReverse = mode === 'reverse';

  const loadCatalog = useCallback(() => {
    setLoadError('');
    fetchPairs().then(setCatalog, e => setLoadError(e.message));
  }, []);
  useEffect(loadCatalog, [loadCatalog]);

  const login = useCallback(async () => {
    if (!inTelegram) return null;
    setAuthError('');
    try {
      const fresh = await loginWithTelegram(initData);
      tokenRef.current = fresh.token;
      setSession(fresh);
      fetchMe(fresh.token).then(me => setQuota(me.quota), () => {});
      return fresh;
    } catch (e) {
      setAuthError(e.message);
      return null;
    }
  }, []);
  useEffect(() => { login(); }, [login]);

  const adoptGuest = useCallback(guest => {
    tokenRef.current = guest.token;
    const next = {token: guest.token, guest: true};
    setSession(next);
    return next;
  }, []);

  const dropGuest = useCallback(() => {
    clearGuestSession();
    tokenRef.current = null;
    setSession(null);
    setQuota(null);
  }, []);

  // In a browser nothing is requested for ready-made pairs; the config only says whether own texts
  // are allowed, and a returning guest gets the same account (and allowance) back.
  useEffect(() => {
    if (inTelegram) return;
    fetchConfig().then(config => {
      configRef.current = config;
      const stored = config.guests ? readGuestSession() : null;
      if (!stored) return;
      const guest = adoptGuest(stored);
      fetchMe(guest.token).then(me => setQuota(me.quota), e => { if (e.status === 401) dropGuest(); });
    }, () => {});
  }, [adoptGuest, dropGuest]);

  const guestLogin = async () => {
    configRef.current ??= await fetchConfig();
    const config = configRef.current;
    if (!config.guests) throw new ApiError('', {code: 'guests_disabled'});
    const challenge = config.turnstileSiteKey
      ? await getChallengeToken(config.turnstileSiteKey, {container: challengeRef.current, theme})
      : '';
    const fresh = await loginAsGuest(challenge);
    saveGuestSession({token: fresh.token, expiresAt: Date.now() + fresh.expiresIn * 1000});
    return adoptGuest(fresh);
  };

  useEffect(() => {
    document.documentElement.dataset.theme = theme;
    applyChrome(theme);
  }, [theme]);
  useEffect(() => { startTelegram(); return onThemeChanged(setTheme); }, []);
  // What is on screen at the moment the visitor accepts is what gets saved.
  useEffect(() => onOptionalChange(allowed => {
    if (!allowed) return;
    saveHistory(history);
    if (!inTelegram) saveTheme(theme);
  }), [history, theme]);

  useEffect(() => {
    if (!loading) return undefined;
    const timer = setInterval(() => setStatusIndex(i => (i + 1) % statuses.length), 1800);
    return () => clearInterval(timer);
  }, [loading]);

  const resetOutcome = useCallback(() => {
    requestId.current++;
    setLoading(false);
    setError('');
    setLimited(false);
    setNotice('');
    setNeedTelegram(false);
    setCopied(false);
  }, []);

  // Show a ready-made pair right away so the first screen already has something to copy.
  const showPair = useCallback(pair => {
    resetOutcome();
    setMode(pair.mode);
    setText(pair.input);
    setResult(pairResult(pair, true));
  }, [resetOutcome]);
  // Only on the first load: an emptied field later must stay empty and keep the chosen mode.
  const seeded = useRef(false);
  useEffect(() => {
    if (!catalog || seeded.current) return;
    seeded.current = true;
    if (!result && !text) showPair(pickPair(catalog.pairs, DEFAULT_MODE));
  }, [catalog, result, text, showPair]);

  const changeMode = next => { showPair(pickPair(pairs, next)); setView('compose'); };
  const randomPair = () => { showPair(pickPair(pairs, mode, result?.pairId)); setView('compose'); };

  const changeText = value => {
    resetOutcome();
    setText(value);
    const match = findPair(pairs, mode, value);
    setResult(match ? pairResult(match, true) : null);
  };
  const clearText = () => { changeText(''); sourceRef.current?.focus(); };
  // Telegram shows the result as its own screen; its native back arrow is easy to miss, so the screen
  // also offers a fresh start. Back keeps the text for editing, this empties it.
  const startOver = () => { changeText(''); setView('compose'); };

  const remember = entry => {
    setHistory(current => {
      const next = [entry, ...current].slice(0, HISTORY_LIMIT);
      saveHistory(next);
      return next;
    });
  };

  // One retry after a fresh login: session tokens expire (guests after 30 days).
  const callServer = async body => {
    const current = session ?? await guestLogin();
    try {
      return await translateOnServer(current.token, body);
    } catch (e) {
      if (e.status !== 401) throw e;
      if (!inTelegram) dropGuest();
      const fresh = await (inTelegram ? login() : guestLogin());
      if (!fresh) throw e;
      return translateOnServer(fresh.token, body);
    }
  };

  const translate = async (previous = '') => {
    const input = text.trim();
    if (!input) { setNotice(isReverse ? 'Сначала вставьте корпоративный текст.' : 'Сначала напишите, что вы на самом деле думаете.'); return; }

    const match = !previous && findPair(pairs, mode, input);
    if (match) {
      resetOutcome();
      const shown = pairResult(match, false);
      countPair(shown, level);
      setResult(shown);
      remember({mode, level, input: match.input, text: match.output, source: 'pair', time: Date.now()});
      setView('result');
      return;
    }
    if (inTelegram && !session) { setNotice(authError || 'Входим через Telegram… Повторите через секунду.'); return; }
    if (!inTelegram && configRef.current?.guests === false) { setNeedTelegram(true); return; }

    const id = ++requestId.current;
    lastPrevious.current = previous;
    setNotice('');
    setNeedTelegram(false);
    setError('');
    setLimited(false);
    setCopied(false);
    setStatusIndex(0);
    setLoading(true);
    setView('result');
    try {
      const res = await callServer({text: input, mode, level, previous});
      setQuota(res.quota);
      remember({mode, level, input, text: res.text, source: res.source, time: Date.now()});
      if (id !== requestId.current) return;
      counted.current.add(res.resultId); // the server already recorded this translation
      setResult({source: res.source, resultId: res.resultId, text: res.text, input, mode, preview: false});
      setLoading(false);
    } catch (e) {
      if (id !== requestId.current) return;
      setLoading(false);
      if (e.quota) setQuota(e.quota);
      if (e.code === 'quota_exhausted') setLimited(true);
      else if (e.code === 'guests_disabled') { setNeedTelegram(true); setView('compose'); }
      else setError(e.message);
    }
  };

  const copy = async () => {
    if (!result) return;
    if (await copyText(result.text)) {
      if (!result.untracked) {
        countPair(result, level);
        if (!copiedIds.current.has(result.resultId)) {
          copiedIds.current.add(result.resultId);
          trackResult('copied', result, level);
        }
      }
      setCopied(true);
      haptic('success');
      setTimeout(() => setCopied(false), 1500);
    } else {
      setNotice('Не удалось скопировать. Выделите текст и скопируйте вручную.');
    }
  };

  const openHistory = item => {
    resetOutcome();
    setMode(item.mode);
    setText(item.input);
    setLevel(item.level);
    setResult({source: item.source, untracked: true, text: item.text, input: item.input, mode: item.mode, preview: false});
    setView('result');
  };

  const clearHistory = () => { setHistory([]); saveHistory([]); };

  const toggleTheme = () => {
    const next = theme === 'dark' ? 'light' : 'dark';
    setTheme(next);
    saveTheme(next);
  };

  // Telegram's native buttons call whichever handlers are current.
  const actions = useRef({});
  actions.current = {translate, copy, example: randomPair, back: () => setView('compose'), retry: () => translate(lastPrevious.current)};
  useEffect(() => {
    if (view !== 'result') {
      return showMainButton('Перевести', () => actions.current.translate(), {disabled: !catalog || loading});
    }
    const stopBack = showBackButton(() => actions.current.back());
    let stopMain;
    if (loading) stopMain = showMainButton('Перевожу…', () => {}, {disabled: true});
    // Out of AI translations: the one thing left to do is a ready-made example, which is always free.
    else if (limited) stopMain = showMainButton('Готовый пример', () => actions.current.example());
    else if (error) stopMain = showMainButton('Повторить', () => actions.current.retry());
    else stopMain = showMainButton(copied ? 'Скопировано' : 'Скопировать', () => actions.current.copy());
    return () => { stopMain(); stopBack(); };
  }, [view, copied, catalog, loading, limited, error]);

  if (loadError) {
    return (
      <main className="wrap state-page">
        <Brand />
        <p role="alert">{loadError}</p>
        <button type="button" className="btn" onClick={loadCatalog}>Повторить</button>
      </main>
    );
  }

  const modeLabel = modes.find(m => m.id === (result?.mode ?? mode))?.label ?? '';
  const canAlternate = inTelegram && result?.source === 'ai' && !loading;

  return (
    <>
      <header className="topbar">
        <div className="wrap">
          <Brand />
          <div className="top-right">
            {session && <QuotaMeter quota={quota} />}
            {authError && (
              <span className="mono muted">Вход не выполнен · <button type="button" className="link" onClick={login}>Повторить</button></span>
            )}
            {!inTelegram && (
              <button type="button" className="btn" aria-pressed={theme === 'dark'} onClick={toggleTheme}>
                {theme === 'dark' ? 'Светлая' : 'Тёмная'}
              </button>
            )}
          </div>
        </div>
      </header>

      <main className="wrap" data-split={inTelegram} data-view={view}>
        <div className="layout">
          <section className="panel compose" aria-labelledby="in-title">
            <h2 id="in-title">Оригинал</h2>
            {catalog && <ModeSwitch modes={modes} value={mode} onChange={changeMode} />}
            <div className="field">
              <label className="mono" htmlFor="source">{isReverse ? 'Корпоративный текст' : 'Как есть'}</label>
              <textarea id="source" ref={sourceRef} value={text} maxLength={MAX_LENGTH} placeholder={isReverse ? 'Вставьте письмо или сообщение на корпоративном…' : 'Напишите, что думаете на самом деле…'}
                onChange={e => changeText(e.target.value)} />
              <div className="hint mono muted">
                <span>{result?.preview ? 'Готовый пример · без AI' : ''}</span>
                <span>{text.length} / {MAX_LENGTH}</span>
              </div>
            </div>
            <div>
              <div className="label"><span className="mono">Уровень</span></div>
              <LevelSwitch value={level} onChange={setLevel} />
              <p className="mono muted hint-line">Для готовых примеров уровень не меняется.</p>
            </div>
            {notice && <p className="notice" role="status">{notice}</p>}
            {needTelegram && (
              <p className="notice login-note" role="status">
                Перевод своих текстов доступен в Telegram: <a href={BOT_LINK}>открыть бота</a>. Готовые примеры работают и здесь.
              </p>
            )}
            <div className="challenge" ref={challengeRef} />
            <div className="actions">
              {!inTelegram && <button type="button" className="btn btn-primary" onClick={() => translate()} disabled={!catalog || loading}>Перевести</button>}
              <RandomButton onClick={randomPair} />
              {text && <button type="button" className="btn" onClick={clearText}><Eraser size={18} aria-hidden="true" />Очистить</button>}
            </div>
          </section>

          <section className="panel output" aria-labelledby="out-title" aria-live="polite">
            <h2 id="out-title">{isReverse ? 'По-человечески' : 'На корпоративном'}</h2>
            {loading && <LoadingMemo status={statuses[statusIndex]} />}
            {!loading && limited && <LimitMemo quota={quota} guest={Boolean(session?.guest)} botLink={BOT_LINK} />}
            {!loading && !limited && error && <ErrorBox message={error} onRetry={inTelegram ? undefined : () => translate(lastPrevious.current)} />}
            {!loading && !limited && !error && result && (
              <>
                <Memo result={result} modeLabel={modeLabel} copied={copied} onCopy={copy} showCopy={!inTelegram}
                  onAlternative={canAlternate ? () => translate(result.text) : undefined}
                  alternativeLabel="Другой вариант · спишет 1" />
                {result.mode === 'reverse' && <p className="muted">{satireNotice}</p>}
              </>
            )}
            {!loading && !limited && !error && !result && (
              <div className="stub"><p>Здесь появится готовый текст.<br />{isReverse ? 'Вставьте корпоративный текст' : 'Напишите, что думаете,'} или возьмите случайный пример.</p></div>
            )}
            {inTelegram && !loading && !limited && (
              <button type="button" className="btn start-over" onClick={startOver}>
                <Plus size={18} aria-hidden="true" />Новый перевод
              </button>
            )}
          </section>
        </div>
        {catalog && <History items={history} modes={modes} onOpen={openHistory} onClear={clearHistory} />}
      </main>
      <footer className="wrap app-foot mono muted">
        <a href="/privacy">Конфиденциальность</a>
        <button type="button" className="link" onClick={showPreferences}>Настройки хранения</button>
      </footer>
    </>
  );
}

async function copyText(value) {
  try {
    await navigator.clipboard.writeText(value);
    return true;
  } catch {
    const area = document.createElement('textarea');
    area.value = value;
    area.style.position = 'fixed';
    area.style.opacity = '0';
    document.body.append(area);
    area.select();
    const ok = document.execCommand('copy');
    area.remove();
    return ok;
  }
}
