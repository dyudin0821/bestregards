const tg = typeof window === 'undefined' ? undefined : window.Telegram?.WebApp;

// initData is empty when the page is opened in a regular browser.
export const inTelegram = Boolean(tg?.initData);
export const initData = tg?.initData ?? '';
export const telegramScheme = inTelegram ? tg.colorScheme : undefined;

const ACCENT = '#d7ff3a';
const ON_ACCENT = '#111111';

// Paper colours of the two themes; Telegram paints its own chrome with them.
export const paper = {light: '#f4f4f0', dark: '#0e0e0e'};

const safely = fn => { try { fn(); } catch { /* older clients lack some methods */ } };

export function startTelegram() {
  if (!inTelegram) return;
  safely(() => { tg.ready(); tg.expand(); });
  // A swipe down on the long form would otherwise collapse the app instead of scrolling it.
  if (tg.isVersionAtLeast?.('7.7')) safely(() => tg.disableVerticalSwipes());
}

export function applyChrome(theme) {
  if (!inTelegram) return;
  safely(() => tg.setHeaderColor(paper[theme]));
  // Overscroll and the loading frame show this colour; Telegram's own would not match the page.
  if (tg.isVersionAtLeast?.('6.1')) safely(() => tg.setBackgroundColor(paper[theme]));
  if (tg.isVersionAtLeast?.('7.10')) safely(() => tg.setBottomBarColor(paper[theme]));
}

export function onThemeChanged(handler) {
  if (!inTelegram) return () => {};
  const listener = () => handler(tg.colorScheme);
  tg.onEvent('themeChanged', listener);
  return () => tg.offEvent('themeChanged', listener);
}

// Native bottom button. It stays visible between screens, so cleanup only detaches the handler.
export function showMainButton(text, onClick, {disabled = false} = {}) {
  if (!inTelegram) return () => {};
  const button = tg.MainButton;
  safely(() => {
    button.setParams({text, color: ACCENT, text_color: ON_ACCENT, is_active: !disabled, is_visible: true});
    button.onClick(onClick);
  });
  return () => safely(() => button.offClick(onClick));
}

export function showBackButton(onClick) {
  if (!inTelegram) return () => {};
  const button = tg.BackButton;
  safely(() => { button.onClick(onClick); button.show(); });
  return () => safely(() => { button.offClick(onClick); button.hide(); });
}

export const haptic = type => { if (inTelegram) safely(() => tg.HapticFeedback.notificationOccurred(type)); };
