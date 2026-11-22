import '@fontsource-variable/golos-text';
import '@fontsource/ibm-plex-mono/400.css';
import '@fontsource/ibm-plex-mono/500.css';
import '@fontsource/ibm-plex-mono/600.css';
import './tokens.css';
import './privacy.css';
import {onOptionalChange, showPreferences, startConsent} from './consent.js';

if (matchMedia('(prefers-color-scheme: dark)').matches) document.documentElement.dataset.theme = 'dark';

const state = document.getElementById('choice-state');

// The event fires only once a choice exists, on every page load and after every change.
onOptionalChange(allowed => {
  state.textContent = allowed
    ? 'Вы приняли: история переводов и тема сохраняются в этом браузере.'
    : 'Вы отклонили: история и тема не сохраняются.';
});
startConsent();

document.getElementById('choice-open').addEventListener('click', showPreferences);
