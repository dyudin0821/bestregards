import '@fontsource-variable/golos-text';
import '@fontsource/ibm-plex-mono/400.css';
import '@fontsource/ibm-plex-mono/500.css';
import '@fontsource/ibm-plex-mono/600.css';
import './tokens.css';
import './app.css';
import {createRoot} from 'react-dom/client';
import {startConsent} from './consent.js';
import {loadTelegramSdk} from './sdk.js';

startConsent();

// telegram.js reads window.Telegram when it is imported, so the SDK has to be loaded first.
loadTelegramSdk()
  .then(() => import('./App.jsx'))
  .then(({default: App}) => createRoot(document.getElementById('root')).render(<App />));
