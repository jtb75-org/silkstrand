import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
// Self-hosted Inter (variable 400–700) — bundled + served by Vite, no external
// font CDN (privacy-first; see CLAUDE.md). Imported before app styles so the
// @font-face is registered first.
import '@fontsource-variable/inter/index.css';
import App from './App';
import './index.css';

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
