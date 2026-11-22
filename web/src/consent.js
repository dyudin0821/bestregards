import 'vanilla-cookieconsent/dist/cookieconsent.css';
import './consent.css';
import * as CookieConsent from 'vanilla-cookieconsent';
import {CONSENT_KEY, CONSENT_REVISION, OPTIONAL, forgetOptional} from './storage.js';
import {isTelegramLaunch} from './sdk.js';

// Inside Telegram there is no guest login and the screen is a phone: a short notice, and no "Настроить"
// button, since the only optional category is covered by accept/decline (settings stay in the footer).
const telegram = typeof window !== 'undefined' && isTelegramLaunch(window.location.hash, window.sessionStorage);

const ru = {
  consentModal: telegram ? {
    title: 'Хранение на устройстве',
    description:
      'Историю переводов и тему можно сохранять на этом устройстве, на сервер они не уходят. ' +
      '<a href="/privacy">Подробнее</a>',
    acceptAllBtn: 'Сохранять',
    acceptNecessaryBtn: 'Не сохранять',
    revisionMessage: 'Мы обновили политику конфиденциальности.',
  } : {
    title: 'Куки и хранилище браузера',
    description:
      'Куки мы не используем. Чтобы сайт работал, браузер хранит ваш гостевой вход. Историю переводов и тему ' +
      'можно сохранять на этом устройстве: на сервер они не уходят. <a href="/privacy">Политика конфиденциальности</a>',
    acceptAllBtn: 'Принять',
    acceptNecessaryBtn: 'Отклонить',
    showPreferencesBtn: 'Настроить',
    revisionMessage: 'Мы обновили политику конфиденциальности.',
  },
  preferencesModal: {
    title: 'Хранилище браузера',
    acceptAllBtn: 'Принять',
    acceptNecessaryBtn: 'Отклонить',
    savePreferencesBtn: 'Сохранить выбор',
    closeIconLabel: 'Закрыть',
    sections: [
      {
        title: 'Куки',
        description: 'Мы их не используем. Единственное исключение: при сложной проверке Cloudflare Turnstile может поставить свои куки на время проверки.',
      },
      {
        title: 'Необходимое',
        description: telegram
          ? 'Ваш выбор в этом окне и служебные данные Telegram для запуска приложения. Без них приложение не работает.'
          : 'Гостевой вход, ваш выбор в этом окне и служебные данные проверки от ботов. Без них сайт не работает.',
        linkedCategory: 'necessary',
      },
      {
        title: 'История и тема',
        description: 'Последние до 30 переводов и выбранная тема. Хранятся только в вашем браузере, на сервер не отправляются.',
        linkedCategory: OPTIONAL,
      },
      {
        title: 'Подробности',
        description: 'Полный список и сроки: <a href="/privacy">страница о конфиденциальности</a>.',
      },
    ],
  },
};

let started = false;

export function startConsent() {
  if (started) return;
  started = true;
  CookieConsent.run({
    revision: CONSENT_REVISION,
    // The choice lives in localStorage, not in a cookie: the site sets no cookies.
    cookie: {name: CONSENT_KEY, useLocalStorage: true, expiresAfterDays: 365},
    guiOptions: {
      consentModal: {layout: 'box inline', position: 'bottom center', equalWeightButtons: true},
      preferencesModal: {layout: 'box', equalWeightButtons: true},
    },
    categories: {
      necessary: {enabled: true, readOnly: true},
      [OPTIONAL]: {},
    },
    language: {default: 'ru', translations: {ru}},
  });
}

export const showPreferences = () => CookieConsent.showPreferences();

// Fires for the first choice and every later change, with the current state of the optional category.
// Declining or withdrawing wipes what was saved; the caller saves its current state on acceptance.
export function onOptionalChange(handler) {
  const listener = () => {
    const allowed = CookieConsent.acceptedCategory(OPTIONAL);
    if (!allowed) forgetOptional();
    handler(allowed);
  };
  window.addEventListener('cc:onConsent', listener);
  window.addEventListener('cc:onChange', listener);
  return () => {
    window.removeEventListener('cc:onConsent', listener);
    window.removeEventListener('cc:onChange', listener);
  };
}
