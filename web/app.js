'use strict';
/* Zero Launcher 2 — интерфейс. Все данные и действия идут через Go (HTTP API на 127.0.0.1).
   Интерфейс ничего не выдумывает: статусы запуска, процессов, путей и картинки приходят из backend. */

/* ================================================================ связь с Go */

const T = new URLSearchParams(location.search).get('t') || sessionStorage.getItem('zero.t') || '';
try { sessionStorage.setItem('zero.t', T); } catch (_) { /* приватный режим */ }
if (location.search) history.replaceState(null, '', '/');

let epoch = 0; // растёт при каждом изменении — фоновое обновление не затрёт свежие данные старыми
async function api(method, path, body) {
  if (method !== 'GET' && path !== 'ping') epoch++;
  const res = await fetch('/api/' + path, {
    method,
    headers: { 'X-Zero-Token': T, 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  let data = {};
  try { data = await res.json(); } catch (_) { /* пустой ответ */ }
  if (!res.ok) {
    const err = new Error(data.error || `Ошибка ${res.status}`);
    err.status = res.status;
    throw err;
  }
  return data;
}
const img = (u) => (u ? `${u}${u.includes('?') ? '&' : '?'}t=${encodeURIComponent(T)}` : '');
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

/* ================================================================ состояние интерфейса */

const S = {
  state: null,          // последний ответ /api/state
  view: 'home',
  selected: '',
  filter: 'all',
  search: '',
  jobs: {},             // id игры → последняя задача запуска (из Go)
  polling: new Set(),   // id задач, которые сейчас опрашиваются
  dismissed: new Set(), // задачи, чьё сообщение скрыто
  templates: null,
  connected: true,
  D: null,              // черновик формы игры
  lastFocus: null,
};
const LIB_VIEWS = new Set(['home', 'favorites', 'recent', 'all']);

/* ================================================================ помощники */

const $ = (id) => document.getElementById(id);
const SVGNS = 'http://www.w3.org/2000/svg';

function h(tag, props, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(props || {})) {
    if (v === null || v === undefined || v === false) continue;
    if (k === 'class') el.className = v;
    else if (k.startsWith('on')) el.addEventListener(k.slice(2), v);
    else if (k === 'checked' || k === 'disabled' || k === 'value' || k === 'open' || k === 'selected') el[k] = v;
    else el.setAttribute(k, v === true ? '' : v);
  }
  for (const c of kids.flat(Infinity)) {
    if (c === null || c === undefined || c === false) continue;
    el.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return el;
}

// replaceChildren без «null» в тексте: пропускает пустые значения, разворачивает массивы.
function fill(el, ...kids) {
  el.replaceChildren(...kids.flat(Infinity).filter((k) => k !== null && k !== undefined && k !== false));
  return el;
}

function icon(name, cls) {
  const s = document.createElementNS(SVGNS, 'svg');
  s.setAttribute('class', 'ico' + (cls ? ' ' + cls : ''));
  s.setAttribute('aria-hidden', 'true');
  const u = document.createElementNS(SVGNS, 'use');
  u.setAttribute('href', '#i-' + name);
  s.append(u);
  return s;
}

function logoZ(cls) {
  const s = document.createElementNS(SVGNS, 'svg');
  s.setAttribute('class', cls || '');
  s.setAttribute('aria-hidden', 'true');
  const u = document.createElementNS(SVGNS, 'use');
  u.setAttribute('href', '#logo-z');
  s.append(u);
  return s;
}

function hue(name) {
  let x = 0;
  for (const ch of name || '') x = (x * 31 + ch.charCodeAt(0)) % 3600;
  return Math.round(x / 10);
}
const coverStyle = (name) => {
  const k = hue(name);
  return `--cover:linear-gradient(160deg, hsl(${k} 55% 30%), hsl(${(k + 40) % 360} 60% 14%));--sym:hsl(${k} 95% 86%)`;
};

// «GTA 5 RP» → GTA, «Hollow Knight» → HK, «VALORANT» → V
function initials(name) {
  const words = (name || '').split(/[^\p{L}\p{N}]+/u).filter(Boolean);
  if (!words.length) return '?';
  const first = words[0];
  if (first.length <= 4 && first === first.toUpperCase() && /\p{L}/u.test(first)) return first;
  if (words.length > 1 && /\p{L}/u.test(words[1][0])) return (first[0] + words[1][0]).toUpperCase();
  return first[0].toUpperCase();
}

function plural(n, one, few, many) {
  const a = Math.abs(n) % 100, b = a % 10;
  if (a > 10 && a < 20) return many;
  if (b === 1) return one;
  if (b >= 2 && b <= 4) return few;
  return many;
}

function agoShort(ts) {
  if (!ts) return '';
  const d = Math.floor(Date.now() / 1000) - ts;
  if (d < 90) return 'только что';
  if (d < 3600) return `${Math.round(d / 60)} мин назад`;
  if (d < 86400) return `${Math.round(d / 3600)} ч назад`;
  const days = Math.floor(d / 86400);
  if (days === 1) return '1 дн назад';
  if (days < 30) return `${days} дн назад`;
  return new Date(ts * 1000).toLocaleDateString('ru-RU');
}

// «Сегодня, 15:24», «Вчера, 09:10», «28.10, 18:02»
function whenLabel(ts) {
  if (!ts) return '—';
  const d = new Date(ts * 1000);
  const time = d.toLocaleTimeString('ru-RU', { hour: '2-digit', minute: '2-digit' });
  const today = new Date();
  const y = new Date(); y.setDate(today.getDate() - 1);
  if (d.toDateString() === today.toDateString()) return `Сегодня, ${time}`;
  if (d.toDateString() === y.toDateString()) return `Вчера, ${time}`;
  return `${d.toLocaleDateString('ru-RU', { day: '2-digit', month: '2-digit' })}, ${time}`;
}

const games = () => (S.state ? S.state.games : []);
const gameById = (id) => games().find((g) => g.id === id);
const launcherById = (id) => (S.state ? S.state.launchers.find((l) => l.id === id) : null);
const platformName = (p) => (S.state && S.state.platforms[p]) || 'Другое';
const isRunning = (gid) => { const j = S.jobs[gid]; return !!(j && j.state === 'running'); };
const launchWord = (n) => plural(n, 'запуск', 'запуска', 'запусков');

const ART_FROM = {
  'steam-custom': 'ваша картинка из Steam', steam: 'Steam', 'steam-cdn': 'Steam', epic: 'Epic Games',
  riot: 'Riot Games', exe: 'иконка программы', store: 'Microsoft Store', custom: 'своя картинка',
};

const GTA_RE = /\bgta\b|grand theft auto|гта|gta\s*(5|v)\b|gta5|gtav/i;
function looksGta(d) {
  const t = (d.target || '').toLowerCase();
  return d.platform === 'rage' || GTA_RE.test(d.name || '') || t === '{rage}' || t.includes('ragemp') ||
    /(^|[\\/])(gta5|gta5_enhanced|playgtav|gtavlauncher)\.exe$/.test(t) || /rungameid\/(271590|3240220)\b/.test(t);
}
function looksRage(d) {
  const t = (d.target || '').toLowerCase();
  return t === '{rage}' || t.includes('ragemp') || /gta5rp/.test(t) || (d.platform === 'rage' && !t);
}
function guessPlatform(target) {
  const s = (target || '').toLowerCase();
  if (!s) return null;
  if (s.startsWith('steam://') || s.includes('steamapps')) return 'steam';
  if (s.startsWith('shell:appsfolder')) return /minecraft|4297127d64ec6/.test(s) ? 'minecraft' : 'xbox';
  if (s === '{battlenet}' || s.includes('battle.net')) return 'battlenet';
  if (s.startsWith('com.epicgames.launcher://') || s.includes('epic games')) return 'epic';
  if (s.startsWith('uplay://') || s.includes('ubisoft')) return 'ubisoft';
  if (s === '{riot}' || s.includes('riot games') || s.includes('valorant')) return 'riot';
  if (s === '{rage}' || s.includes('ragemp') || s.includes('gta5rp')) return 'rage';
  if (/minecraft|tlauncher/.test(s)) return 'minecraft';
  if (/playgtav|gta5|rockstar games/.test(s)) return 'rockstar';
  if (s.includes('battle.net')) return 'battlenet';
  if (s.includes('gog galaxy') || s.includes('gog games')) return 'gog';
  if (s.includes('electronic arts') || s.includes('ea games')) return 'ea';
  return null;
}

/* ================================================================ уведомления и строка состояния */

let toastTimer, statusTimer, statusUntil = 0;
function libraryLine() {
  if (!S.state) return 'Загрузка библиотеки…';
  if (!games().length) return 'Библиотека пуста — добавьте первую игру';
  return `Библиотека обновлена • ${whenLabel(S.state.library_updated)}`;
}
function setStatus(text, kind, sticky) {
  const box = $('statusLeft');
  $('statusText').textContent = text;
  box.classList.toggle('bad', kind === 'bad');
  box.classList.toggle('busy', kind === 'busy');
  clearTimeout(statusTimer);
  statusUntil = sticky ? 0 : Date.now() + 9000;
  if (!sticky) statusTimer = setTimeout(idleStatus, 9100);
}
// Когда ничего не происходит, внизу видно, когда обновлялась библиотека.
function idleStatus() {
  if (!S.state || !S.connected || runningJobs().length || Date.now() < statusUntil) return;
  setStatus(libraryLine(), '', true);
}
function notify(text, kind) {
  setStatus(text, kind);
  const quiet = S.state && S.state.settings.hide_toasts;
  if (quiet && kind !== 'bad') return; // ошибки показываются всегда
  const el = $('toast');
  el.textContent = text;
  el.className = 'toast show' + (kind === 'bad' ? ' bad' : kind === 'warn' ? ' warn' : '');
  el.setAttribute('role', kind === 'bad' ? 'alert' : 'status');
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => el.classList.remove('show'), kind === 'bad' ? 6000 : 3000);
}

async function browse(kind, title, initial) {
  try {
    const { path } = await api('POST', 'browse', { kind, title, initial: initial || '' });
    return path || '';
  } catch (e) { notify(e.message, 'bad'); return ''; }
}

/* ================================================================ загрузка */

function applyPrefs() {
  const s = S.state.settings;
  const b = document.body.classList;
  b.toggle('accent-violet', s.accent === 'violet');
  b.toggle('accent-cyan', s.accent === 'cyan');
  b.toggle('compact-mode', !!s.compact);
  b.toggle('no-motion', !!s.reduce_motion);
}

function setConnected(ok) {
  if (S.connected === ok && S.state) return;
  S.connected = ok;
  renderStatus();
  if (!ok) setStatus('Нет связи с Zero Launcher. Если окно осталось после закрытия программы, запустите её снова.', 'bad', true);
}

function fatal(message) {
  fill(document.body, h('section', { class: 'fatal' },
    h('h1', null, 'Zero Launcher не отвечает'),
    h('p', null, 'Закройте это окно и запустите ZeroLauncher.exe заново.'),
    h('p', null, message)));
}

async function load() {
  let st;
  try {
    st = await api('GET', 'state');
  } catch (e) {
    if (!S.state) { fatal(e.message); return; }
    setConnected(false);
    return;
  }
  S.state = st;
  setConnected(true);
  applyPrefs();
  ensureSelected();
  resumeJobs();
  renderAll();
}

// Фоновое обновление: статусы лаунчеров, процессы и картинки меняются и без участия интерфейса.
async function refresh() {
  if (document.hidden || !S.state) return;
  const ep = epoch;
  let st;
  try { st = await api('GET', 'state'); } catch (_) { setConnected(false); return; }
  setConnected(true);
  if (ep !== epoch) return; // пока шёл запрос, что-то поменяли — данные уже устарели
  const changed = JSON.stringify(st.games) !== JSON.stringify(S.state.games) || st.art_rev !== S.state.art_rev;
  const launchersChanged = JSON.stringify(st.launchers) !== JSON.stringify(S.state.launchers);
  const settings = S.state.settings;
  S.state = st;
  S.state.settings = settings; // настройки меняются только отсюда
  ensureSelected();
  resumeJobs();
  renderStatus();
  if (changed && LIB_VIEWS.has(S.view)) renderView();
  if (S.view === 'launchers') {
    if (launchersChanged && !document.activeElement.closest('#launcherList')) renderLaunchers();
    else updateLauncherStatus();
  }
}

function ensureSelected() {
  const list = games();
  if (!list.length) { S.selected = ''; return; }
  if (!gameById(S.selected)) S.selected = sortedGames('recent')[0].id;
}

// Запуски, начатые до перезагрузки окна или с ярлыка, тоже показываем.
function resumeJobs() {
  for (const j of S.state.active_jobs || []) {
    if (!S.polling.has(j.id)) poll(j.game_id, j.id);
  }
}

function renderAll() {
  renderStatus();
  renderView();
}

/* ================================================================ навигация */

function setView(view) {
  if (view === 'library') view = 'home';
  S.view = view;
  const lib = LIB_VIEWS.has(view);
  for (const b of document.querySelectorAll('.topnav .nav-tab')) {
    const on = b.dataset.group === 'library' ? lib : b.dataset.view === view;
    b.classList.toggle('active', on);
    if (on) b.setAttribute('aria-current', 'page'); else b.removeAttribute('aria-current');
  }
  for (const b of document.querySelectorAll('.side-link')) {
    const on = b.dataset.view === view;
    b.classList.toggle('active', on);
    if (on) b.setAttribute('aria-current', 'page'); else b.removeAttribute('aria-current');
  }
  $('topnav').classList.remove('open');
  $('mobileMenu').setAttribute('aria-expanded', 'false');
  closeMenu();
  $('hero').classList.toggle('hidden', view !== 'home');
  $('pageHead').classList.toggle('hidden', view === 'home');
  $('library').classList.toggle('hidden', !lib);
  $('launchersView').classList.toggle('hidden', view !== 'launchers');
  $('settingsView').classList.toggle('hidden', view !== 'settings');
  $('aboutView').classList.toggle('hidden', view !== 'about');
  $('main').scrollTop = 0;
  window.scrollTo(0, 0);
  renderView();
}

function renderView() {
  if (!S.state) return;
  const v = S.view;
  if (v !== 'home') renderPageHead();
  if (v === 'home') renderHero();
  if (LIB_VIEWS.has(v)) renderLibrary();
  if (v === 'launchers') renderLaunchers();
  if (v === 'settings') renderSettings();
  if (v === 'about') renderAbout();
}

function renderPageHead() {
  const n = games().length;
  const fav = games().filter((g) => g.favorite).length;
  const heads = {
    favorites: ['heart', 'Избранное', fav ? `${fav} ${plural(fav, 'игра', 'игры', 'игр')} со звёздочкой.` : 'Отмечайте игры звёздочкой на карточке — они соберутся здесь.'],
    recent: ['clock', 'Последние', 'Во что вы играли недавно и чем закончились запуски.'],
    all: ['grid', 'Все игры', n ? `${n} ${plural(n, 'игра', 'игры', 'игр')} в библиотеке.` : 'Библиотека пока пуста.'],
    launchers: ['rocket', 'Лаунчеры', 'Zero Launcher открывает их по очереди и ждёт, пока они загрузятся. Укажите путь к .exe, если лаунчер не нашёлся сам.'],
    settings: ['gear', 'Настройки', 'Внешний вид и поведение Zero Launcher. Всё сохраняется сразу.'],
    about: ['info', 'О программе', 'Что умеет Zero Launcher и где он хранит данные.'],
  };
  const [ic, title, text] = heads[S.view] || heads.all;
  let actions = null;
  if (S.view === 'all') {
    actions = [h('button', { class: 'secondary-button', onclick: () => openImport() }, icon('scan'), 'Найти игры'),
      h('button', { class: 'primary-button', onclick: () => openEditor(null) }, icon('plus'), 'Добавить игру')];
  } else if (S.view === 'launchers') {
    actions = [h('button', { class: 'secondary-button', onclick: detectLaunchers }, icon('scan'), 'Найти автоматически'),
      h('button', { class: 'secondary-button', onclick: openAddLauncher }, icon('plus'), 'Свой лаунчер')];
  }
  fill($('pageHead'), h('div', null, h('h1', null, icon(ic), title), h('p', null, text)),
    actions ? h('div', { class: 'page-actions' }, actions) : null);
}

/* ================================================================ левое меню, статус и правая колонка */

function runningJobs() {
  const ids = new Set();
  for (const [gid, j] of Object.entries(S.jobs)) if (j.state === 'running') ids.add(gid);
  for (const j of (S.state && S.state.active_jobs) || []) ids.add(j.game_id);
  return [...ids];
}

function systemState() {
  const list = games();
  const running = runningJobs();
  if (!S.connected) return { cls: 'bad', title: 'Нет связи', text: 'Перезапустите Zero Launcher', rail: 'Нет связи с программой' };
  if (running.length) {
    const g = gameById(running[0]);
    return { cls: 'warn busy', title: 'Идёт запуск', text: g ? g.name : 'Игра запускается', rail: `Запуск: ${g ? g.name : 'игра'}` };
  }
  if (!list.length) return { cls: 'idle', title: 'Библиотека пуста', text: 'Добавьте первую игру', rail: 'Нет игр' };
  const issues = list.filter((g) => !g.ready || g.chain.some((c) => c.missing)).length;
  if (issues) {
    const t = `${issues} ${plural(issues, 'игре', 'играм', 'играм')} нужен путь`;
    return { cls: 'warn', title: 'Проверьте пути', text: t, rail: t };
  }
  return { cls: '', title: 'Готов к запуску', text: 'Все системы в порядке', rail: 'Всё работает' };
}

function renderStatus() {
  if (!S.state) return;
  const sys = systemState();
  const side = $('sideStatus');
  side.className = 'side-status' + (sys.cls ? ' ' + sys.cls.replace('idle', '') : '');
  $('sideStatusTitle').textContent = sys.title;
  $('sideStatusText').textContent = sys.text;
  const rail = $('sysState');
  rail.className = 'sys-state' + (sys.cls ? ' ' + sys.cls : '');
  fill(rail, h('i'), h('span', null, sys.rail));

  const list = games();
  $('badgeAll').textContent = list.length;
  const fav = list.filter((g) => g.favorite).length;
  $('badgeFav').textContent = fav;
  $('badgeFav').classList.toggle('hidden', !fav);
  $('appVersion').textContent = S.state.version;
  const ms = S.state.avg_launch_ms;
  $('avgLaunch').textContent = ms ? `~ ${Math.max(1, Math.round(ms / 1000))} сек` : '—';
  renderSysLaunchers();

  const q = quickGame();
  $('qaLaunch').disabled = !q;
  $('qaLaunch').title = q ? `Запустить ${q.name}` : 'В библиотеке пока нет игр';
  idleStatus();
}

function launcherIcon(L, cls) {
  const box = h('span', { class: 'l-icon' + (cls ? ' ' + cls : '') });
  if (L && L.icon_url) box.append(h('img', { src: img(L.icon_url), alt: '' }));
  else {
    const k = hue(L ? L.name : '?');
    box.setAttribute('style', `background:linear-gradient(150deg, hsl(${k} 50% 34%), hsl(${(k + 40) % 360} 55% 18%))`);
    box.append(initials(L ? L.name : '?').slice(0, 2));
  }
  return box;
}

// Лаунчеры, которые нужны играм из библиотеки: они и показываются в «Статусе системы».
function relevantLaunchers() {
  const count = {};
  for (const g of games()) {
    for (const c of g.chain) count[c.id] = (count[c.id] || 0) + 1;
    if (g.placeholder) count[g.placeholder] = (count[g.placeholder] || 0) + 1;
  }
  let ids = Object.keys(count).sort((a, b) => count[b] - count[a]);
  if (!ids.length) ids = S.state.launchers.filter((l) => l.exists).map((l) => l.id);
  const running = S.state.launchers.filter((l) => l.running).map((l) => l.id);
  ids = [...new Set([...running.filter((id) => ids.includes(id) || !ids.length), ...ids])];
  return ids.map(launcherById).filter(Boolean).slice(0, 4);
}

function renderSysLaunchers() {
  const list = relevantLaunchers();
  if (!list.length) {
    fill($('sysLaunchers'), h('li', { class: 'sys-empty' }, 'Лаунчеры не найдены. Укажите пути в разделе «Лаунчеры».'));
    return;
  }
  fill($('sysLaunchers'), list.map((L) => {
    let text = 'Найден', cls = '';
    if (L.running) { text = 'Запущен'; cls = 'live'; }
    else if (!L.exists) { text = L.path ? 'Файл не найден' : 'Не найден'; cls = 'bad'; }
    return h('li', { title: L.path || 'Путь не указан' }, launcherIcon(L),
      h('div', null, h('b', null, L.name), h('small', { class: cls }, text)));
  }));
}

function quickGame() {
  const sel = gameById(S.selected);
  if (sel && sel.ready) return sel;
  return sortedGames('recent').find((g) => g.ready) || sel || null;
}

/* ================================================================ библиотека */

function sortedGames(mode, list) {
  const out = [...(list || games())];
  const by = mode || S.state.settings.sort;
  if (by === 'name') out.sort((a, b) => a.name.localeCompare(b.name, 'ru'));
  else if (by === 'added') out.sort((a, b) => b.added - a.added);
  else if (by === 'popular') out.sort((a, b) => (b.play_count - a.play_count) || (b.last_played - a.last_played) || a.name.localeCompare(b.name, 'ru'));
  else out.sort((a, b) => (b.last_played - a.last_played) || (b.added - a.added));
  return out;
}

function baseGames() {
  if (S.view === 'favorites') return games().filter((g) => g.favorite);
  if (S.view === 'recent') return sortedGames('recent', games().filter((g) => g.last_played));
  return games();
}

function visibleGames() {
  const q = S.search.trim().toLowerCase();
  const base = baseGames();
  const list = S.view === 'recent' ? base : sortedGames(null, base);
  return list.filter((g) => (S.filter === 'all' || g.platform === S.filter) &&
    (!q || `${g.name} ${platformName(g.platform)} ${g.chain.map((c) => c.name).join(' ')}`.toLowerCase().includes(q)));
}

const selectedGame = () => gameById(S.selected) || sortedGames()[0] || null;

function select(id) {
  if (!gameById(id)) return;
  S.selected = id;
  if (S.view === 'home') renderHero();
  for (const c of document.querySelectorAll('[data-game]')) c.classList.toggle('selected', c.dataset.game === id);
  renderStatus();
}

function renderLibrary() {
  if (!S.state) return;
  $('sortSelect').value = S.state.settings.sort;
  $('sortSelect').parentElement.classList.toggle('hidden', S.view === 'recent');
  const mode = S.state.settings.view;
  $('viewGrid').classList.toggle('active', mode !== 'list');
  $('viewList').classList.toggle('active', mode === 'list');
  $('viewGrid').setAttribute('aria-pressed', String(mode !== 'list'));
  $('viewList').setAttribute('aria-pressed', String(mode === 'list'));
  renderFilters();
  renderGrid();
  renderJournal();
}

function renderFilters() {
  const base = baseGames();
  const counts = {};
  for (const g of base) counts[g.platform] = (counts[g.platform] || 0) + 1;
  const keys = Object.keys(S.state.platforms).filter((p) => counts[p]);
  if (S.filter !== 'all' && !counts[S.filter]) S.filter = 'all';
  const pill = (key, label, n) => h('button', {
    class: 'pill' + (S.filter === key ? ' active' : ''), 'aria-pressed': String(S.filter === key),
    onclick: () => { S.filter = key; renderFilters(); renderGrid(); },
  }, label, key === 'all' ? h('b', null, n) : null);
  $('libControls').classList.toggle('hidden', !base.length);
  const allLabel = { favorites: 'Все избранные', recent: 'Все недавние' }[S.view] || 'Все игры';
  fill($('filterPills'), pill('all', allLabel, base.length),
    keys.length > 1 ? keys.map((k) => pill(k, platformName(k), counts[k])) : null);
  markOverflow();
}
// Если фильтры не помещаются в строку, правый край плавно гаснет — видно, что их можно прокрутить.
function markOverflow() {
  const el = $('filterPills');
  el.classList.toggle('overflow', el.scrollWidth - el.scrollLeft > el.clientWidth + 2);
}

function cardStatus(g, busy) {
  if (busy) return { cls: 'busy', text: 'Запуск…' };
  if (!g.ready) return { cls: 'bad', text: g.problem };
  const miss = g.chain.find((c) => c.missing);
  if (miss) return { cls: 'bad', text: `Нет пути к ${miss.name}` };
  if (g.last_played) return { cls: '', text: `Запуск: ${agoShort(g.last_played)}` };
  return { cls: 'idle', text: 'Ещё не запускали' };
}

function coverArt(g) {
  const art = h('div', { class: 'card-art', style: coverStyle(g.name) });
  // Обложка показывается целиком, как её нарисовал разработчик: без обрезки, поля заполняет размытая копия.
  if (g.cover_url) {
    art.append(h('img', { class: 'cover-bg', src: img(g.cover_url), alt: '', loading: 'lazy', draggable: 'false' }),
      h('img', { class: 'cover', src: img(g.cover_url), alt: '', loading: 'lazy', draggable: 'false' }));
  } else if (g.icon_url) {
    art.append(h('img', { class: 'icon-bg', src: img(g.icon_url), alt: '', draggable: 'false' }),
      h('img', { class: 'icon-fg', src: img(g.icon_url), alt: '', draggable: 'false', onload: fitIcon }));
  } else {
    const sym = initials(g.name);
    art.append(h('div', { class: 'initials' + (sym.length > 2 ? ' long' : '') }, sym));
  }
  return art;
}

// Маленькие иконки (32–48 px) не растягиваем во всю карточку — так они остаются чёткими.
function fitIcon(e) {
  const im = e.currentTarget;
  if (im.naturalWidth && im.naturalWidth < 128) im.style.width = `${Math.max(48, im.naturalWidth * 1.5)}px`;
}

function starButton(g) {
  return h('button', {
    class: 'star' + (g.favorite ? ' on' : ''), 'aria-pressed': String(!!g.favorite),
    'aria-label': g.favorite ? `Убрать ${g.name} из избранного` : `Добавить ${g.name} в избранное`,
    title: g.favorite ? 'Убрать из избранного' : 'В избранное',
    onclick: (e) => { e.stopPropagation(); toggleFavorite(g); },
  }, icon('star'));
}

function kebab(g) {
  return h('button', {
    class: 'kebab', 'aria-label': `Действия: ${g.name}`, 'aria-haspopup': 'menu', title: 'Действия',
    onclick: (e) => { e.stopPropagation(); openGameMenu(g, e.currentTarget); },
  }, icon('more'));
}

function cardClick(g) {
  if (S.view === 'home') select(g.id);
  else { select(g.id); openDetails(g); }
}

function gameCard(g, selected) {
  const busy = isRunning(g.id);
  const st = cardStatus(g, busy);
  const art = coverArt(g);
  art.append(
    h('span', { class: 'card-badge' }, platformName(g.platform)),
    starButton(g),
    h('button', {
      class: 'card-play' + (busy ? ' busy' : ''), disabled: busy, 'aria-label': `Запустить ${g.name}`, title: 'Запустить',
      onclick: (e) => { e.stopPropagation(); launch(g.id); },
    }, busy ? h('span', { class: 'spinner' }) : icon('play')));
  return h('article', {
    class: 'game-card' + (selected ? ' selected' : ''), 'data-game': g.id, tabindex: '0',
    'aria-label': `${g.name}. ${st.text}`,
    onclick: () => cardClick(g),
    ondblclick: () => launch(g.id),
    onkeydown: (e) => { if (e.key === 'Enter' && e.target === e.currentTarget) cardClick(g); },
  },
  art,
  h('div', { class: 'card-info' },
    h('div', { class: 'card-text' },
      h('h3', { title: g.name }, g.name),
      h('div', { class: 'card-status ' + st.cls, title: st.text }, h('i'), h('span', null, st.text))),
    kebab(g)));
}

function rowThumb(g) {
  const t = h('div', { class: 'row-thumb', style: coverStyle(g.name) });
  if (g.cover_url) t.append(h('img', { src: img(g.cover_url), alt: '', loading: 'lazy' }));
  else if (g.icon_url) t.append(h('img', { class: 'icon', src: img(g.icon_url), alt: '' }));
  else t.append(initials(g.name).slice(0, 3));
  return t;
}

function chainText(g) {
  if (g.rage && g.server === 'official') return 'Через лаунчер GTA5RP';
  if (g.chain.length) return g.chain.map((c) => c.name).join(' → ') + ' → игра';
  if (g.placeholder === 'riot') return 'Через Riot Client';
  if (g.placeholder === 'rage') return 'Через RAGE MP';
  return 'Запускается сразу';
}

function gameRow(g, selected) {
  const busy = isRunning(g.id);
  const st = cardStatus(g, busy);
  return h('article', {
    class: 'game-row' + (selected ? ' selected' : ''), 'data-game': g.id, tabindex: '0', 'aria-label': `${g.name}. ${st.text}`,
    onclick: () => cardClick(g), ondblclick: () => launch(g.id),
    onkeydown: (e) => { if (e.key === 'Enter' && e.target === e.currentTarget) cardClick(g); },
  },
  rowThumb(g),
  h('div', { class: 'row-name' }, h('b', { title: g.name }, g.name), h('small', null, platformName(g.platform))),
  h('div', { class: 'row-chain' + (st.cls === 'bad' ? ' bad' : '') }, st.cls === 'bad' ? st.text : chainText(g)),
  h('div', { class: 'row-played' }, g.last_played ? `Запуск: ${agoShort(g.last_played)}` : 'Ещё не запускали'),
  starButton(g),
  h('button', { class: 'row-launch', disabled: busy, onclick: (e) => { e.stopPropagation(); launch(g.id); } },
    busy ? [h('span', { class: 'spinner' }), 'Запуск…'] : [icon('play'), 'Играть']),
  kebab(g));
}

function renderGrid() {
  const all = games();
  const grid = $('gameGrid');
  const empty = $('emptyState');
  const list = visibleGames();
  const listMode = S.state.settings.view === 'list';
  grid.classList.toggle('list', listMode);
  if (!all.length) {
    fill(grid);
    grid.classList.add('hidden');
    empty.classList.remove('hidden');
    renderWelcome(empty);
    return;
  }
  const base = baseGames();
  if (!list.length) {
    fill(grid);
    grid.classList.add('hidden');
    empty.classList.remove('hidden');
    if (!base.length && S.view === 'favorites') {
      fill(empty, icon('star'), h('h3', null, 'В избранном пока пусто'),
        h('p', null, 'Нажмите на звёздочку в углу карточки игры — она появится здесь.'),
        h('div', { class: 'page-actions' }, h('button', { class: 'secondary-button', onclick: () => setView('all') }, 'Все игры')));
    } else if (!base.length && S.view === 'recent') {
      fill(empty, icon('clock'), h('h3', null, 'Вы ещё ничего не запускали'),
        h('p', null, 'Запустите игру кнопкой «Запустить» — она появится в этом списке.'));
    } else {
      fill(empty, icon('search'), h('h3', null, 'Ничего не найдено'), h('p', null, 'Измените запрос или выберите другую платформу.'),
        h('div', { class: 'page-actions' }, h('button', {
          class: 'secondary-button',
          onclick: () => { S.search = ''; S.filter = 'all'; $('globalSearch').value = ''; renderFilters(); renderGrid(); },
        }, 'Сбросить фильтры')));
    }
    return;
  }
  empty.classList.add('hidden');
  grid.classList.remove('hidden');
  const sel = selectedGame();
  fill(grid, list.map((g) => (listMode ? gameRow : gameCard)(g, sel && sel.id === g.id)));
}

function renderWelcome(box) {
  const tpl = S.templates || [];
  fill(box, logoZ('welcome-logo'),
    h('h3', null, 'Библиотека пока пуста'),
    h('p', null, 'Найдите установленные игры из Steam, Epic Games, Riot, Rockstar, RAGE MP, Minecraft, Xbox, EA app, Battle.net, Ubisoft и GOG или добавьте игру по пути к .exe. Обложки и иконки подтянутся сами.'),
    S.view === 'home' ? null : h('div', { class: 'page-actions' }, // на главной эти кнопки уже есть выше
      h('button', { class: 'primary-button', onclick: () => openImport() }, icon('scan'), 'Найти игры'),
      h('button', { class: 'secondary-button', onclick: () => openEditor(null) }, icon('plus'), 'Добавить вручную')),
    tpl.length ? h('p', { class: 'muted-note', style: 'margin-top:18px' }, 'Или начните с шаблона:') : null,
    tpl.length ? h('div', { class: 'template-chips' }, tpl.map((t) => h('button', { class: 'pill', onclick: () => openEditor(null, t) }, t.name))) : null);
  const logo = box.querySelector('.welcome-logo');
  if (logo) logo.setAttribute('style', 'width:64px;height:64px;filter:drop-shadow(0 0 16px #ff8a1266)');
}

const HISTORY_TEXT = { done: 'Запущено', warn: 'Запущено с предупреждением', error: 'Ошибка', cancelled: 'Отменено' };
const HISTORY_CLASS = { done: 'ok', warn: 'warn-text', error: 'bad-text', cancelled: '' };
const sortedHistory = () => [...(S.state.history || [])].sort((a, b) => b.time - a.time);

function renderJournal() {
  const box = $('journal');
  if (S.view !== 'recent') { box.classList.add('hidden'); return; }
  box.classList.remove('hidden');
  const hist = sortedHistory();
  fill(box, h('h2', null, icon('clock'), 'Журнал запусков'),
    hist.length ? h('ul', { class: 'history-list' }, hist.map((e) => {
      const d = new Date(e.time * 1000);
      const dur = e.duration_ms ? ` · ${Math.max(1, Math.round(e.duration_ms / 1000))} сек` : '';
      return h('li', null,
        h('span', { class: 'when' }, d.toLocaleDateString('ru-RU', { day: '2-digit', month: '2-digit' }) + ' ' +
          d.toLocaleTimeString('ru-RU', { hour: '2-digit', minute: '2-digit' })),
        h('div', null, h('b', null, e.game_name),
          h('small', { class: HISTORY_CLASS[e.state] || '' }, (HISTORY_TEXT[e.state] || e.state) + dur),
          e.message ? h('small', null, e.message) : null));
    })) : h('p', { class: 'muted-note' }, 'Запусков ещё не было.'));
}

async function toggleFavorite(game) {
  const g = gameById(game.id) || game;
  const want = !g.favorite;
  g.favorite = want;
  renderStatus();
  renderView();
  try {
    const res = await api('PUT', `games/${g.id}`, { favorite: want });
    const cur = gameById(g.id);
    if (cur) cur.favorite = !!res.game.favorite;
    renderStatus();
    if (LIB_VIEWS.has(S.view)) renderView();
    notify(want ? `«${g.name}» в избранном` : `«${g.name}» убрана из избранного`);
  } catch (e) {
    g.favorite = !want;
    renderView();
    notify(e.message, 'bad');
  }
}

/* ---------------------------------------------------------------- меню карточки */

let menuAnchor = null;
function openMenu(anchor, items) {
  const menu = $('menu');
  if (menuAnchor === anchor && !menu.classList.contains('hidden')) { closeMenu(); return; }
  menuAnchor = anchor;
  fill(menu, items.filter(Boolean).map((it) => (it === '-' ? h('hr') : h('button', {
    role: 'menuitem', class: it.danger ? 'danger' : '',
    onclick: () => { closeMenu(); it.run(); },
  }, icon(it.icon), it.label))));
  menu.classList.remove('hidden');
  positionMenu();
  const first = menu.querySelector('button');
  if (first) first.focus({ preventScroll: true });
}
// Меню следует за кнопкой при прокрутке и закрывается, если кнопка ушла с экрана.
function positionMenu() {
  const menu = $('menu');
  if (menu.classList.contains('hidden') || !menuAnchor) return;
  if (!document.contains(menuAnchor)) { closeMenu(); return; }
  const r = menuAnchor.getBoundingClientRect();
  if (r.bottom < 0 || r.top > window.innerHeight) { closeMenu(); return; }
  const mw = menu.offsetWidth, mh = menu.offsetHeight;
  const left = Math.max(8, Math.min(r.right - mw, window.innerWidth - mw - 8));
  let top = r.bottom + 6;
  if (top + mh > window.innerHeight - 8) top = Math.max(8, r.top - mh - 6);
  menu.style.left = `${left}px`;
  menu.style.top = `${top}px`;
}
function closeMenu() {
  const menu = $('menu');
  if (menu.classList.contains('hidden')) return;
  menu.classList.add('hidden');
  const a = menuAnchor;
  menuAnchor = null;
  if (a && document.contains(a) && menu.contains(document.activeElement)) a.focus();
}

function openGameMenu(g, anchor) {
  openMenu(anchor, [
    { icon: 'play', label: isRunning(g.id) ? 'Запускается…' : 'Запустить', run: () => launch(g.id) },
    { icon: 'info', label: 'Подробнее', run: () => openDetails(g) },
    { icon: 'gear', label: 'Настройки игры', run: () => openEditor(g) },
    { icon: 'star', label: g.favorite ? 'Убрать из избранного' : 'В избранное', run: () => toggleFavorite(g) },
    { icon: 'refresh', label: 'Обновить обложку', run: () => refreshArt(g) },
    { icon: 'folder', label: 'Открыть папку', run: () => gameAction(g, 'folder') },
    S.state.is_windows ? { icon: 'monitor', label: 'Ярлык на рабочем столе', run: () => gameAction(g, 'shortcut') } : null,
    '-',
    { icon: 'trash', label: 'Удалить из библиотеки', danger: true, run: () => confirmDelete(g) },
  ]);
}

async function refreshArt(g) {
  setStatus(`Проверяю обложку: ${g.name}…`, 'busy');
  try {
    const { changed } = await api('POST', `games/${g.id}/art`);
    await load();
    notify(changed ? `Обложка «${g.name}» обновлена` : `У «${g.name}» уже актуальные картинки`);
  } catch (e) { notify(e.message, 'bad'); }
}

/* ---------------------------------------------------------------- главный экран выбранной игры */

function heroBackground(g) {
  const bg = h('div', { class: 'hero-bg', 'aria-hidden': 'true' });
  if (g && g.hero_url) bg.append(h('img', { class: 'wide', src: img(g.hero_url), alt: '' }));
  else if (g && g.cover_url) bg.append(h('img', { class: 'blur', src: img(g.cover_url), alt: '' }), h('img', { class: 'poster', src: img(g.cover_url), alt: '' }));
  else if (g && g.icon_url) bg.append(h('div', { class: 'scene' }), h('img', { class: 'blur', src: img(g.icon_url), alt: '', style: 'opacity:.75' }), h('img', { class: 'big-icon', src: img(g.icon_url), alt: '' }));
  else bg.append(h('div', { class: 'scene' }));
  return bg;
}

function titleNode(name) {
  const words = name.trim().split(/\s+/);
  if (words.length < 2) return h('h1', null, name);
  return h('h1', null, words[0] + ' ', h('em', null, words.slice(1).join(' ')));
}

function howText(g) {
  if (g.rage && g.server === 'official') return 'Откроется официальный лаунчер GTA5RP.';
  if (g.chain.length) return `Перед игрой откроются: ${g.chain.map((c) => c.name).join(', ')}.`;
  if (g.placeholder === 'riot') return 'Запускается через Riot Client.';
  return 'Запускается сразу, без лаунчеров.';
}

function heroTags(g) {
  const tags = [h('span', { class: 'tag' }, platformName(g.platform))];
  for (const c of g.chain) {
    if (c.missing) tags.push(h('span', { class: 'tag bad' }, `Нет пути к ${c.name}`));
    else if (c.name !== platformName(g.platform) || c.forced) {
      tags.push(h('span', { class: 'tag' + (c.forced ? ' lock' : ''), title: c.forced ? 'Для GTA запускается всегда' : '' }, c.forced ? icon('lock') : null, c.name));
    }
  }
  if (g.rage && g.server && g.server !== 'official') tags.push(h('span', { class: 'tag' }, 'RP'));
  if (!g.ready) tags.push(h('span', { class: 'tag bad' }, 'Нужен путь'));
  return tags;
}

// Листание на главной идёт в том же порядке, что и карточки ниже: с учётом фильтра, сортировки и поиска.
function carouselList() {
  const list = visibleGames();
  return list.length ? list : sortedGames();
}

// Шаг считается от выбранной сейчас игры, а не от момента отрисовки, — без пропусков при быстрых нажатиях.
function heroStep(d) {
  const list = carouselList();
  if (list.length < 2) return;
  let i = list.findIndex((x) => x.id === S.selected);
  i = i < 0 ? (d > 0 ? 0 : list.length - 1) : (i + d + list.length) % list.length;
  S.heroDir = d;
  select(list[i].id);
}

// Картинки соседних игр загружаются заранее, чтобы листание было без мигания.
function preloadNeighbours(list, i) {
  for (const d of [-1, 1]) {
    const n = list[(i + d + list.length) % list.length];
    for (const u of [n && n.hero_url, n && n.cover_url, n && n.logo_url]) {
      if (u) { const im = new Image(); im.src = img(u); }
    }
  }
}

function heroPager(g) {
  const list = carouselList();
  if (list.length < 2) return [];
  const i = list.findIndex((x) => x.id === g.id);
  preloadNeighbours(list, Math.max(0, i));
  const arrows = h('div', { class: 'hero-arrows' },
    h('button', { class: 'round', 'aria-label': 'Предыдущая игра', title: 'Предыдущая игра (←)', onclick: () => heroStep(-1) }, icon('left')),
    h('button', { class: 'round', 'aria-label': 'Следующая игра', title: 'Следующая игра (→)', onclick: () => heroStep(1) }, icon('right')));
  if (list.length > 9) {
    return [arrows, h('div', { class: 'hero-counter', 'aria-live': 'polite' }, `${i < 0 ? '—' : i + 1} / ${list.length}`)];
  }
  return [arrows, h('div', { class: 'hero-dots', role: 'group', 'aria-label': 'Игры' }, list.map((d) => h('button', {
    class: d.id === g.id ? 'current' : '', title: d.name, 'aria-label': d.name, 'aria-current': d.id === g.id ? 'true' : null,
    onclick: () => { S.heroDir = list.indexOf(d) > i ? 1 : -1; select(d.id); },
  })))];
}

function renderHero() {
  const box = $('hero');
  if (!S.state) return;
  const g = selectedGame();
  if (!g) { renderWelcomeHero(box); return; }
  const job = S.jobs[g.id];
  const busy = isRunning(g.id);
  const stats = g.play_count ? `${g.play_count} ${launchWord(g.play_count)}` : '';
  const meta = [g.last_played ? `Запускали ${agoShort(g.last_played)}` : 'Ещё не запускали', stats, g.desc ? howText(g) : ''].filter(Boolean).join(' · ');

  let primary;
  if (busy) primary = h('button', { class: 'primary-button busy', disabled: true }, h('span', { class: 'spinner' }), 'Запуск…');
  else if (!g.ready) primary = h('button', { class: 'primary-button fix', onclick: () => fixGame(g) }, icon('folder'), 'Указать путь');
  else primary = h('button', { class: 'primary-button', onclick: () => launch(g.id) }, icon('play'), 'Запустить игру');

  const swap = S.heroDir ? (S.heroDir > 0 ? ' swap-next' : ' swap-prev') : '';
  S.heroDir = 0;
  box.className = 'hero' + swap + (S.view !== 'home' ? ' hidden' : '');
  fill(box,
    heroBackground(g),
    h('div', { class: 'hero-shade' }),
    g.logo_url ? h('img', { class: 'hero-logo', src: img(g.logo_url), alt: '' }) : null,
    h('div', { class: 'hero-copy' },
      h('div', { class: 'tags' }, heroTags(g)),
      titleNode(g.name),
      h('p', { class: 'hero-sub' }, g.desc || howText(g)),
      h('p', { class: 'hero-meta' }, meta),
      g.rage ? serverLine(g) : null,
      h('div', { class: 'hero-buttons' },
        primary,
        h('button', { class: 'secondary-button', onclick: () => openDetails(g) }, icon('info'), 'Подробнее'),
        busy ? h('button', { class: 'ghost-button', onclick: () => cancelJob(g.id) }, 'Отменить') : null),
      job && !S.dismissed.has(job.id) ? renderTrack(trackNodes(g, job)) : null,
      renderAlert(g, job)),
    heroPager(g));
}

function renderWelcomeHero(box) {
  box.className = 'hero' + (S.view !== 'home' ? ' hidden' : '');
  fill(box, heroBackground(null), h('div', { class: 'hero-shade' }),
    h('div', { class: 'hero-copy' },
      h('div', { class: 'tags' }, ['Steam', 'Epic Games', 'Riot', 'RAGE MP'].map((t) => h('span', { class: 'tag' }, t)),
        h('span', { class: 'tag lock' }, icon('lock'), 'Social Club')),
      h('h1', null, 'Все игры — ', h('em', null, 'одной кнопкой')),
      h('p', { class: 'hero-sub' }, 'Перед онлайн-игрой Zero Launcher сам откроет нужные лаунчеры, а для GTA — ещё и Social Club.'),
      h('p', { class: 'hero-meta' }, 'Обложки и иконки игр подбираются автоматически и обновляются сами.'),
      h('div', { class: 'hero-buttons' },
        h('button', { class: 'primary-button', onclick: () => openImport() }, icon('scan'), 'Найти игры'),
        h('button', { class: 'secondary-button', onclick: () => openEditor(null) }, icon('plus'), 'Добавить вручную'))));
}

function serverLine(g) {
  let text;
  if (g.server === 'official') text = [h('span', null, 'Вход через '), h('b', null, 'официальный лаунчер GTA5RP')];
  else if (g.server) text = [h('span', null, 'Сервер: '), h('b', null, g.server_name || g.server)];
  else text = [h('span', null, 'Сервер ещё не выбран')];
  return h('div', { class: 'hero-server' }, icon('server'), h('span', null, text),
    h('button', { class: 'link-button', onclick: () => openServerPicker(g, false) }, g.server ? 'Сменить сервер' : 'Выбрать сервер'));
}

function stepClass(s) {
  if (s.state === 'done' && s.message === 'Уже запущен') return 'is-already';
  return 'is-' + s.state;
}

function trackNodes(g, job) {
  if (job && job.steps && job.steps.length) {
    return job.steps.map((s) => ({
      label: s.kind === 'game' ? 'Игра' : s.label,
      note: s.message || (s.forced ? 'Обязателен для GTA' : ''),
      cls: stepClass(s) + (s.forced ? ' forced' : ''),
    }));
  }
  const nodes = g.chain.map((c) => {
    const live = !c.missing && (launcherById(c.id) || {}).running;
    let note = c.forced ? 'Обязателен для GTA' : 'Откроется, если не запущен';
    if (c.missing) note = 'Путь не указан';
    else if (live) note = 'Уже открыт';
    return { label: c.name, note, cls: (c.forced && !live ? 'forced' : '') + (c.missing ? ' missing' : '') + (live ? ' is-already' : '') };
  });
  let note = g.chain.length ? 'После лаунчеров' : 'Запустится сразу';
  if (!g.ready) note = g.problem;
  nodes.push({ label: 'Игра', note, cls: g.ready ? '' : 'missing' });
  return nodes;
}

function dotIcon(cls) {
  if (/is-(done|already)/.test(cls)) return icon('check');
  if (/is-error/.test(cls)) return icon('x');
  if (/is-(skipped|cancelled)/.test(cls)) return icon('minus');
  if (/is-warn|missing/.test(cls)) return icon('alert');
  return null;
}

function renderTrack(nodes) {
  const ol = h('ol', { class: 'launch-track', 'aria-label': 'Порядок запуска', 'aria-live': 'polite' });
  nodes.forEach((n, i) => {
    if (i) ol.append(h('li', { class: 'track-sep', 'aria-hidden': 'true' }, icon('right')));
    ol.append(h('li', { class: 'track-step ' + n.cls },
      h('span', { class: 'track-dot' }, dotIcon(n.cls)),
      h('span', { class: 'track-copy' }, h('b', null, n.label), n.note ? h('small', null, n.note) : null)));
  });
  return ol;
}

function renderAlert(g, job) {
  if (!job || S.dismissed.has(job.id) || job.state === 'running') return null;
  const hide = h('button', { class: 'ghost-button small', onclick: () => { S.dismissed.add(job.id); renderHero(); } }, 'Скрыть');
  const last = job.steps[job.steps.length - 1];
  if (job.state === 'done') {
    if (last && last.state === 'warn') return h('div', { class: 'hero-alert warn', role: 'status' }, h('p', null, last.message), hide);
    return h('div', { class: 'hero-buttons', style: 'margin-top:8px' }, hide);
  }
  if (job.state !== 'error') return h('div', { class: 'hero-buttons', style: 'margin-top:8px' }, hide);
  const err = job.error || {};
  const actions = [];
  if (err.code === 'launcher_missing' && err.launcher_id) {
    const L = launcherById(err.launcher_id);
    actions.push(h('button', {
      class: 'primary-button small',
      onclick: async () => {
        const p = await browse('exe', `Где находится ${L ? L.name : 'лаунчер'}?`);
        if (!p) return;
        try {
          await api('PUT', `launchers/${err.launcher_id}`, { path: p });
          await load();
          launch(g.id);
        } catch (e) { notify(e.message, 'bad'); }
      },
    }, 'Указать путь'));
    actions.push(h('button', { class: 'secondary-button small', onclick: () => launch(g.id, { skip: true }) }, 'Запустить без него'));
  }
  if (err.code === 'game_missing') actions.push(h('button', { class: 'primary-button small', onclick: () => openEditor(g) }, 'Изменить игру'));
  actions.push(hide);
  return h('div', { class: 'hero-alert', role: 'alert' }, h('p', null, err.message || 'Не удалось запустить'), actions);
}

function fixGame(g) {
  if (g.placeholder && launcherById(g.placeholder) && /лаунчер/i.test(g.problem)) {
    setView('launchers');
    setTimeout(() => {
      const input = document.querySelector(`[data-lid="${g.placeholder}"] .launcher-path input`);
      if (input) { input.scrollIntoView({ block: 'center' }); input.focus(); }
    }, 60);
    notify(`Укажите путь к «${launcherById(g.placeholder).name}»`, 'warn');
    return;
  }
  openEditor(g);
}

async function gameAction(g, action) {
  try {
    await api('POST', `games/${g.id}/${action}`);
    notify(action === 'shortcut' ? `Ярлык «${g.name}» создан на рабочем столе` : `Открыта папка: ${g.name}`);
  } catch (e) { notify(e.message, 'bad'); }
}

/* ---------------------------------------------------------------- подробности об игре */

function openDetails(g) {
  const banner = h('div', { class: 'details-banner', style: coverStyle(g.name) });
  if (g.hero_url) banner.append(h('img', { class: 'wide', src: img(g.hero_url), alt: '' }));
  else if (g.cover_url) banner.append(h('img', { class: 'blur', src: img(g.cover_url), alt: '' }), h('img', { class: 'poster', src: img(g.cover_url), alt: '' }));
  else if (g.icon_url) banner.append(h('img', { class: 'blur', src: img(g.icon_url), alt: '' }), h('img', { class: 'icon', src: img(g.icon_url), alt: '' }));
  if (g.logo_url) banner.append(h('img', { class: 'logo', src: img(g.logo_url), alt: '' }));

  const from = g.art_from ? (ART_FROM[g.art_from] || g.art_from) : '';
  const artLine = g.cover_custom ? 'Своя обложка. Автоматическая подборка для обложки отключена.'
    : from ? `Обложка: ${from}. Обновляется автоматически, когда меняется в ${g.art_from === 'exe' ? 'файле игры' : 'лаунчере'}.`
      : g.icon_url ? 'Обложки в лаунчерах не нашлось — показана иконка игры. Можно выбрать свою картинку.'
        : 'Картинок пока нет — показаны буквы названия. Можно выбрать свою картинку в настройках игры.';

  const body = [
    banner,
    g.desc ? h('p', { class: 'details-desc' }, g.desc) : null,
    h('div', { class: 'stats' },
      h('div', null, h('small', null, 'Запусков'), h('b', null, String(g.play_count || 0))),
      h('div', null, h('small', null, 'Последний запуск'), h('b', null, g.last_played ? agoShort(g.last_played) : '—')),
      h('div', null, h('small', null, 'В библиотеке с'), h('b', null, g.added ? new Date(g.added * 1000).toLocaleDateString('ru-RU') : '—')),
      h('div', null, h('small', null, 'Платформа'), h('b', null, platformName(g.platform)))),
    h('p', { class: 'section-title' }, 'Как запускается'),
    renderTrack(trackNodes(g, null)),
    h('p', { class: 'hint', style: 'color:var(--muted);font-size:13px;margin:10px 0 0' }, howText(g)),
    g.rage ? h('div', { style: 'margin-top:14px' }, serverLine(g)) : null,
    !g.ready ? h('div', { class: 'modal-note' }, icon('alert'), h('p', null, g.problem)) : null,
    h('p', { class: 'section-title' }, 'Обложка и иконка'),
    h('p', { class: 'hint', style: 'color:var(--soft);font-size:13px;margin:0 0 10px' }, artLine),
    h('div', { class: 'modal-tools' },
      h('button', { class: 'secondary-button small', onclick: () => refreshArt(g) }, icon('refresh'), 'Обновить обложку'),
      h('button', { class: 'secondary-button small', onclick: () => openEditor(g) }, icon('image'), 'Своя обложка…')),
  ];
  const busy = isRunning(g.id);
  openDrawer({
    eyebrow: platformName(g.platform), title: g.name, wide: true, body,
    actions: [
      h('button', { class: 'secondary-button', onclick: () => openEditor(g) }, icon('gear'), 'Настройки'),
      h('button', { class: 'ghost-button square', title: 'Открыть папку игры', 'aria-label': 'Открыть папку игры', onclick: () => gameAction(g, 'folder') }, icon('folder')),
      S.state.is_windows ? h('button', { class: 'ghost-button square', title: 'Ярлык на рабочем столе', 'aria-label': 'Ярлык на рабочем столе', onclick: () => gameAction(g, 'shortcut') }, icon('monitor')) : null,
      h('span', { class: 'spacer' }),
      g.ready ? h('button', { class: 'primary-button', disabled: busy, onclick: () => { closeDrawer(); launch(g.id); } }, icon('play'), busy ? 'Запускается…' : 'Запустить игру')
        : h('button', { class: 'primary-button fix', onclick: () => { closeDrawer(); fixGame(g); } }, 'Указать путь'),
    ],
  });
}

/* ================================================================ запуск */

async function launch(gid, opts = {}) {
  const g = gameById(gid);
  if (!g || isRunning(gid)) return;
  closeMenu();
  if (S.view !== 'home') setView('home');
  select(gid);
  if (!g.ready) { fixGame(g); return; }
  if (g.rage && !g.server) { openServerPicker(g, true); return; } // сервер GTA 5 RP выбирается один раз
  if (S.state.settings.confirm_launch && !opts.confirmed) { confirmLaunch(g, opts); return; }
  try {
    const { job } = await api('POST', `games/${gid}/launch`, { skip_missing: !!opts.skip });
    S.jobs[gid] = job;
    setStatus(`Запуск: ${g.name}`, 'busy', true);
    renderHero(); renderGrid(); renderStatus();
    poll(gid, job.id);
  } catch (e) { notify(e.message, 'bad'); }
}

function confirmLaunch(g, opts) {
  const steps = g.chain.map((c) => c.name);
  openDialog({
    title: `Запустить ${g.name}?`,
    body: [h('p', null, steps.length ? `Сначала откроются: ${steps.join(', ')}. Потом — игра.` : 'Игра запустится сразу.')],
    confirm: 'Запустить',
    onConfirm: () => launch(g.id, { ...opts, confirmed: true }),
  });
}

async function poll(gid, jid) {
  if (S.polling.has(jid)) return;
  S.polling.add(jid);
  let failures = 0;
  try {
    for (;;) {
      let job;
      try {
        ({ job } = await api('GET', `jobs/${jid}`));
        failures = 0;
      } catch (e) {
        if (e.status === 404 || ++failures > 10) break;
        await sleep(800);
        continue;
      }
      const changed = JSON.stringify(S.jobs[gid]) !== JSON.stringify(job);
      S.jobs[gid] = job;
      if (changed) {
        liveStatus(gid, job);
        if (LIB_VIEWS.has(S.view)) {
          if (S.view === 'home' && S.selected === gid) renderHero();
          const card = document.querySelector(`[data-game="${gid}"]`);
          const g = gameById(gid);
          if (card && g) card.replaceWith((S.state.settings.view === 'list' ? gameRow : gameCard)(g, S.selected === gid));
        }
        renderStatus();
      }
      if (job.state !== 'running') { await finishJob(gid, job); break; }
      await sleep(400);
    }
  } finally {
    S.polling.delete(jid);
  }
}

function liveStatus(gid, job) {
  const g = gameById(gid);
  const name = g ? g.name : 'Игра';
  const step = job.steps.find((s) => s.state === 'running');
  if (job.state === 'running' && step) setStatus(`${name}: ${step.kind === 'game' ? 'игра' : step.label} — ${step.message || 'выполняется'}`, 'busy', true);
}

async function finishJob(gid, job) {
  const g = gameById(gid);
  const name = g ? g.name : 'Игра';
  const last = job.steps[job.steps.length - 1];
  if (job.state === 'done') {
    if (last && last.state === 'warn') notify(`${name}: ${last.message}`, 'warn');
    else notify(`${name}: ${(last && last.message) || 'игра запущена'}`);
  } else if (job.state === 'cancelled') notify(`${name}: запуск отменён`);
  else notify(`${name}: ${(job.error && job.error.message) || 'не удалось запустить'}`, 'bad');
  await load();
  if (job.state === 'done' && job.close_ui) setTimeout(() => window.close(), 1500);
}

async function cancelJob(gid) {
  const job = S.jobs[gid];
  if (!job) return;
  try { await api('POST', `jobs/${job.id}/cancel`); setStatus('Отменяем запуск…', 'busy'); } catch (e) { notify(e.message, 'bad'); }
}

/* ================================================================ боковая панель и диалог */

function openDrawer({ eyebrow, title, body, actions, wide, focus }) {
  closeMenu();
  if ($('drawer').classList.contains('hidden')) S.lastFocus = document.activeElement;
  const panel = $('drawerPanel');
  panel.className = 'modal-panel' + (wide ? ' wide' : '');
  fill(panel,
    h('div', { class: 'modal-heading' },
      h('div', null, eyebrow ? h('p', { class: 'eyebrow' }, eyebrow) : null, h('h2', { id: 'drawerTitle' }, title)),
      h('button', { class: 'icon-button', 'aria-label': 'Закрыть', onclick: closeDrawer }, icon('x'))),
    h('div', { class: 'modal-body' }, body),
    actions ? h('div', { class: 'modal-actions' }, actions) : null);
  $('drawer').classList.remove('hidden');
  document.body.style.overflow = 'hidden';
  setTimeout(() => { const el = focus ? panel.querySelector(focus) : panel.querySelector('.modal-body input, .modal-body select, .modal-actions .primary-button, button'); if (el) el.focus(); }, 30);
}

function closeDrawer() {
  $('drawer').classList.add('hidden');
  fill($('drawerPanel'));
  document.body.style.overflow = '';
  S.D = null;
  if (S.lastFocus && document.contains(S.lastFocus)) S.lastFocus.focus();
}

function openDialog({ title, body, confirm, danger, onConfirm, cancel }) {
  const prev = document.activeElement;
  const close = () => { $('dialog').classList.add('hidden'); if (prev && document.contains(prev)) prev.focus(); };
  fill($('dialogPanel'),
    h('h2', { id: 'dialogTitle' }, title), body,
    h('div', { class: 'modal-actions' }, h('span', { class: 'spacer' }),
      cancel === false ? null : h('button', { class: 'secondary-button', onclick: close }, cancel || 'Отмена'),
      confirm ? h('button', { class: danger ? 'danger-button' : 'primary-button', onclick: () => { close(); if (onConfirm) onConfirm(); } }, confirm) : null));
  $('dialog').classList.remove('hidden');
  setTimeout(() => { const b = $('dialogPanel').querySelector(danger ? '.secondary-button' : '.primary-button, .secondary-button'); if (b) b.focus(); }, 30);
}

/* ================================================================ настройки игры */

function openEditor(game, template) {
  S.D = {
    isNew: !game,
    id: game ? game.id : null,
    name: game ? game.name : '',
    target: game ? game.target : '',
    args: game ? game.args : '',
    workdir: game ? game.workdir : '',
    platform: game ? game.platform : 'other',
    online: game ? game.online : false,
    requires: game ? [...game.requires] : [],
    admin: game ? game.admin : false,
    cover: game && game.cover_custom ? game.cover : '',
    source_id: game ? game.source_id : '',
    touchedReq: !!game,
    touchedPlat: !!game,
    template: null,
  };
  if (template) applyTemplate(template);
  renderEditor();
}

function applyTemplate(t) {
  Object.assign(S.D, {
    name: t.name, target: t.target, args: t.args, platform: t.platform, online: t.online,
    requires: [...t.requires], source_id: t.source_id || '',
    touchedReq: true, touchedPlat: true, template: t,
  });
}

function setPlatform(p, fromUser) {
  const D = S.D;
  D.platform = p;
  if (fromUser) D.touchedPlat = true;
  if (!D.touchedReq) {
    D.requires = (S.state.platform_requires[p] || []).slice();
    D.online = D.requires.length > 0;
  }
}

function targetHint() {
  const t = (S.D.target || '').trim();
  const m = t.match(/^\{([a-z0-9_]+)\}$/i);
  if (m) {
    const L = launcherById(m[1]);
    if (!L) return h('p', { class: 'hint bad' }, 'Неизвестный лаунчер в фигурных скобках');
    return L.exists ? h('p', { class: 'hint ok' }, `Будет запущен ${L.name}: ${L.path}`)
      : h('p', { class: 'hint bad' }, `Путь к «${L.name}» пока не указан — задайте его в разделе «Лаунчеры».`);
  }
  if (/valorant|league of legends|riot/i.test(t + ' ' + S.D.name) && !/^steam:/.test(t)) {
    return h('p', { class: 'hint' }, 'Игры Riot запускаются через Riot Client с командой запуска игры — Zero Launcher подставит её сам.');
  }
  return h('p', { class: 'hint' }, 'Файл игры .exe, ярлык .lnk или .url, либо ссылка вида steam://rungameid/578080');
}

function renderRequires() {
  const box = $('editorReq');
  if (!box) return;
  const D = S.D;
  const gta = looksGta(D);
  const list = h('ul', { class: 'check-list' + (D.online ? '' : ' off') });
  for (const L of S.state.launchers) {
    const forced = gta && L.id === 'socialclub';
    const checked = forced || D.requires.includes(L.id);
    let note = !L.path ? 'Путь не указан' : (L.exists ? (L.running ? 'Запущен сейчас' : 'Найден') : 'Файл не найден');
    let noteCls = !L.exists ? ' bad' : (L.running ? ' live' : '');
    if (forced) { note = 'Обязателен для GTA'; noteCls = ''; }
    list.append(h('li', null, h('label', { class: 'check-row' + (forced ? ' locked' : '') },
      h('input', {
        type: 'checkbox', checked, disabled: forced,
        onchange: (e) => {
          D.touchedReq = true;
          D.requires = e.target.checked ? [...D.requires, L.id] : D.requires.filter((x) => x !== L.id);
        },
      }),
      launcherIcon(L),
      h('span', { class: 'name' }, L.name),
      h('span', { class: 'note' + noteCls }, forced ? icon('lock') : null, note))));
  }
  fill(box,
    gta ? h('div', { class: 'modal-note' }, icon('info'), h('p', null, 'Это GTA: перед игрой Zero Launcher всегда откроет Social Club, даже если игра не отмечена как онлайн.')) : null,
    list);
}

function field(label, control, hint) {
  return h('div', { class: 'field' }, h('label', { for: control.id || null }, label), control, hint || null);
}

function coverField(g) {
  const D = S.D;
  const thumb = h('div', { class: 'cover-thumb', style: coverStyle(D.name || (g && g.name) || '') });
  const showCurrent = g && (D.cover ? g.cover_custom && D.cover === g.cover : !g.cover_custom);
  if (showCurrent && g.cover_url) thumb.append(h('img', { src: img(g.cover_url), alt: '' }));
  else if (!D.cover && g && g.icon_url) thumb.append(h('img', { class: 'icon', src: img(g.icon_url), alt: '' }));
  else thumb.append(D.cover ? icon('image') : initials(D.name || '?').slice(0, 3));

  const pick = async () => {
    const p = await browse('image', 'Картинка для обложки', D.cover);
    if (p) { D.cover = p; fill($('coverField'), coverField(g)); }
  };
  let title, note, buttons;
  if (D.cover) {
    title = 'Своя картинка';
    note = D.cover;
    buttons = [
      S.state.is_windows ? h('button', { class: 'secondary-button small', type: 'button', onclick: pick }, 'Выбрать другую…') : null,
      h('button', { class: 'ghost-button small', type: 'button', onclick: () => { D.cover = ''; fill($('coverField'), coverField(g)); } }, 'Вернуть автоматическую'),
    ];
  } else {
    title = 'Подбирается автоматически';
    const from = g && g.art_from && g.art_from !== 'custom' ? ART_FROM[g.art_from] || g.art_from : '';
    note = from ? `Источник: ${from} · обновляется сама` : 'Steam, Epic Games, Riot или иконка из .exe — что найдётся';
    buttons = [S.state.is_windows ? h('button', { class: 'secondary-button small', type: 'button', onclick: pick }, icon('image'), 'Выбрать свою…') : null];
  }
  const manual = !S.state.is_windows ? h('div', { class: 'inline-form', style: 'margin-top:8px' },
    h('input', { id: 'fCover', value: D.cover, placeholder: 'Путь к картинке .jpg или .png', spellcheck: 'false', oninput: (e) => { D.cover = e.target.value; } })) : null;
  return h('div', { class: 'cover-field' }, thumb,
    h('div', { class: 'cover-info' }, h('b', null, title), h('small', { title: note }, note), h('div', { class: 'row' }, buttons), manual));
}

function renderEditor() {
  const D = S.D;
  const g = D.isNew ? null : gameById(D.id);

  const tplSelect = D.isNew ? h('select', {
    id: 'fTemplate',
    onchange: (e) => {
      const t = (S.templates || []).find((x) => x.id === e.target.value);
      if (t) applyTemplate(t); else D.template = null;
      renderEditor();
    },
  }, h('option', { value: '' }, 'Без шаблона'), (S.templates || []).map((t) => h('option', { value: t.id, selected: D.template && D.template.id === t.id }, t.name))) : null;

  const platSel = h('select', { id: 'fPlatform', onchange: (e) => { setPlatform(e.target.value, true); renderEditor(); } },
    Object.entries(S.state.platforms).map(([k, v]) => h('option', { value: k, selected: k === D.platform }, v)));

  const target = h('input', {
    id: 'fTarget', value: D.target, spellcheck: 'false', placeholder: 'C:\\Games\\Minecraft\\TLauncher.exe',
    oninput: (e) => {
      D.target = e.target.value;
      const p = guessPlatform(D.target);
      if (p && !D.touchedPlat) { setPlatform(p, false); $('fPlatform').value = p; $('fOnline').checked = D.online; }
      fill($('fTargetHint'), targetHint());
      renderRequires();
    },
  });

  let serverBlock = null;
  if (g && g.rage) {
    const label = g.server === 'official' ? 'Через официальный лаунчер GTA5RP' : (g.server_name || g.server || 'Не выбран — спрошу при первом запуске');
    serverBlock = h('div', { class: 'field' }, h('span', { class: 'label' }, 'Сервер GTA 5 RP'),
      h('div', { class: 'with-button' }, h('div', { class: 'static-value' }, label),
        h('button', { class: 'secondary-button', type: 'button', onclick: () => openServerPicker(g, false) }, g.server ? 'Сменить' : 'Выбрать')));
  } else if (looksRage(D)) {
    serverBlock = h('div', { class: 'modal-note' }, icon('server'), h('p', null, 'Сервер выберете при первом запуске — Zero Launcher запомнит его и будет подключать сразу.'));
  }

  const body = [
    g ? h('div', { class: 'modal-tools' },
      h('button', { class: 'secondary-button small', type: 'button', onclick: () => gameAction(g, 'folder') }, icon('folder'), 'Открыть папку'),
      S.state.is_windows ? h('button', { class: 'secondary-button small', type: 'button', onclick: () => gameAction(g, 'shortcut') }, icon('monitor'), 'Ярлык на рабочем столе') : null) : null,
    tplSelect ? field('Шаблон', tplSelect) : null,
    D.template && D.template.hint ? h('div', { class: 'modal-note' }, icon('info'), h('p', null, D.template.hint)) : null,
    field('Название', h('input', {
      id: 'fName', value: D.name, maxlength: '120', placeholder: 'Например, GTA 5 RP', required: true,
      oninput: (e) => { D.name = e.target.value; }, onchange: () => renderRequires(),
    })),
    h('div', { class: 'field' }, h('label', { for: 'fTarget' }, 'Путь к игре'),
      h('div', { class: 'with-button' }, target, h('button', {
        class: 'secondary-button', type: 'button',
        onclick: async () => {
          const p = await browse('game', 'Выберите файл игры', D.target);
          if (!p) return;
          D.target = p;
          if (!D.name) D.name = p.split(/[\\/]/).pop().replace(/\.(exe|lnk|url|bat)$/i, '');
          const guess = guessPlatform(p);
          if (guess && !D.touchedPlat) setPlatform(guess, false);
          renderEditor();
        },
      }, 'Обзор…')),
      h('div', { id: 'fTargetHint' }, targetHint())),
    h('div', { class: 'two-cols' },
      field('Аргументы запуска', h('input', { id: 'fArgs', value: D.args, placeholder: 'Необязательно', spellcheck: 'false', oninput: (e) => { D.args = e.target.value; } })),
      field('Платформа', platSel)),
    h('label', { class: 'toggle-row' },
      h('span', { class: 'switch' }, h('input', {
        id: 'fOnline', type: 'checkbox', checked: D.online,
        onchange: (e) => {
          D.online = e.target.checked;
          if (D.online && !D.requires.length) D.requires = (S.state.platform_requires[D.platform] || []).slice();
          renderRequires();
        },
      }), h('span')),
      h('span', null, h('b', null, 'Онлайн-игра'), h('small', null, 'Перед игрой открыть отмеченные лаунчеры и дождаться, пока они загрузятся'))),
    h('div', { id: 'editorReq' }),
    serverBlock,
    h('div', { class: 'field' }, h('span', { class: 'label' }, 'Обложка'), h('div', { id: 'coverField' }, coverField(g))),
    h('details', { class: 'more', open: !!(D.workdir || D.admin) }, h('summary', null, 'Дополнительно'),
      h('div', { class: 'field' }, h('label', { for: 'fWorkdir' }, 'Рабочая папка'),
        h('div', { class: 'with-button' },
          h('input', { id: 'fWorkdir', value: D.workdir, placeholder: 'По умолчанию — папка с .exe', spellcheck: 'false', oninput: (e) => { D.workdir = e.target.value; } }),
          h('button', {
            class: 'secondary-button', type: 'button',
            onclick: async () => { const p = await browse('folder', 'Рабочая папка игры', D.workdir || D.target); if (p) { D.workdir = p; $('fWorkdir').value = p; } },
          }, 'Обзор…'))),
      h('label', { class: 'toggle-row' },
        h('span', { class: 'switch' }, h('input', { type: 'checkbox', checked: D.admin, onchange: (e) => { D.admin = e.target.checked; } }), h('span')),
        h('span', null, h('b', null, 'Запускать от имени администратора'), h('small', null, 'Нужно редким играм и модам. Windows спросит разрешение.')))),
  ];

  const actions = [
    D.isNew ? null : h('button', { class: 'danger-button', type: 'button', onclick: () => confirmDelete(g) }, icon('trash'), 'Удалить'),
    h('span', { class: 'spacer' }),
    h('button', { class: 'secondary-button', type: 'button', onclick: closeDrawer }, 'Отмена'),
    h('button', { class: 'primary-button', type: 'button', onclick: saveGame }, D.isNew ? 'Добавить игру' : 'Сохранить'),
  ];

  openDrawer({ eyebrow: D.isNew ? 'Новая игра' : 'Настройки игры', title: D.isNew ? 'Добавить игру' : g.name, body, actions, focus: D.isNew ? '#fName' : null });
  renderRequires();
}

async function saveGame() {
  const D = S.D;
  if (!D) return;
  if (!D.name.trim()) { notify('Введите название игры', 'bad'); $('fName').focus(); return; }
  if (!D.target.trim()) { notify('Укажите путь к игре — без него её нечем запустить', 'bad'); $('fTarget').focus(); return; }
  const payload = {
    name: D.name.trim(), target: D.target.trim(), args: D.args.trim(), workdir: D.workdir.trim(),
    platform: D.platform, online: D.online, requires: D.online ? D.requires : [], admin: D.admin,
    cover: D.cover.trim(), source_id: D.source_id,
  };
  try {
    const res = D.isNew ? await api('POST', 'games', payload) : await api('PUT', `games/${D.id}`, payload);
    const isNew = D.isNew;
    closeDrawer();
    S.selected = res.game.id;
    S.filter = 'all';
    await load();
    notify(isNew ? `Игра «${res.game.name}» добавлена — обложка подбирается` : 'Настройки игры сохранены');
    setTimeout(load, 2500); // подхватить обложку и иконку
  } catch (e) { notify(e.message, 'bad'); }
}

function confirmDelete(g) {
  openDialog({
    title: `Удалить «${g.name}»?`,
    body: [h('p', null, 'Игра пропадёт из библиотеки Zero Launcher. Файлы игры на диске не удаляются.')],
    confirm: 'Удалить', danger: true,
    onConfirm: async () => {
      try {
        await api('DELETE', `games/${g.id}`);
        closeDrawer();
        delete S.jobs[g.id];
        await load();
        notify(`«${g.name}» удалена из библиотеки`);
      } catch (e) { notify(e.message, 'bad'); }
    },
  });
}

/* ================================================================ сервер GTA 5 RP */

function openServerPicker(g, thenLaunch) {
  const servers = S.state.rp_servers || [];
  const known = servers.find((s) => s.address === g.server);
  let choice = g.server === 'official' ? 'official' : (known ? known.address : (g.server ? 'custom' : (servers[0] || {}).address));
  const custom = h('input', {
    placeholder: 'strawberry.gta5rp.com:22005', spellcheck: 'false', 'aria-label': 'Адрес другого сервера',
    value: g.server && !known && g.server !== 'official' ? g.server : '',
    oninput: () => { choice = 'custom'; for (const r of document.querySelectorAll('input[name=srv]')) r.checked = r.value === 'custom'; },
  });
  const row = (value, title, note, extra) => h('li', null, h('label', { class: 'check-row' },
    h('input', { type: 'radio', name: 'srv', value, checked: choice === value, onchange: () => { choice = value; } }),
    h('span', { class: 'stack' }, h('b', null, title), extra || null),
    note ? h('span', { class: 'note' }, note) : null));
  const L = launcherById('gta5rp');
  const officialNote = L && L.exists ? 'Сервер выбираете в нём' : 'Лаунчер GTA5RP не найден';

  const save = async () => {
    let server = choice;
    let name = '';
    if (choice === 'custom') {
      server = custom.value.trim().replace(/^rage:\/\/.*ip=/i, '');
      if (!server || /[\s/\\]/.test(server)) { notify('Впишите адрес сервера, например strawberry.gta5rp.com:22005', 'bad'); custom.focus(); return; }
      if (!/:\d+$/.test(server)) server += ':22005';
      name = server.split('.')[0].replace(/^\p{L}/u, (c) => c.toUpperCase());
      if (/^\d/.test(name)) name = server;
    } else if (choice !== 'official') {
      name = (servers.find((s) => s.address === choice) || {}).name || choice;
    }
    try {
      await api('PUT', `games/${g.id}`, { server, server_name: name });
      closeDrawer();
      await load();
      notify(choice === 'official' ? 'Сохранено: будет открываться лаунчер GTA5RP' : `Сервер ${name} запомнен`);
      if (thenLaunch) launch(g.id);
    } catch (e) { notify(e.message, 'bad'); }
  };

  openDrawer({
    eyebrow: 'GTA 5 RP', title: 'Выбор сервера',
    body: [
      h('p', { class: 'hint', style: 'color:var(--muted);margin:0 0 6px' }, 'Выберите сервер один раз — дальше кнопка «Запустить игру» будет сразу подключать к нему, как официальный лаунчер GTA5RP. Сменить сервер можно в любой момент.'),
      h('ul', { class: 'check-list' },
        servers.map((s) => row(s.address, s.name, s.address)),
        row('custom', 'Другой сервер', null, h('div', { class: 'custom-addr' }, custom, h('small', null, 'Адрес вида имя.gta5rp.com:22005 или IP:порт'))),
        row('official', 'Через официальный лаунчер GTA5RP', officialNote)),
    ],
    actions: [h('span', { class: 'spacer' }),
      h('button', { class: 'secondary-button', onclick: closeDrawer }, 'Отмена'),
      h('button', { class: 'primary-button', onclick: save }, thenLaunch ? 'Сохранить и запустить' : 'Сохранить')],
    focus: 'input[name=srv]:checked',
  });
}

/* ================================================================ поиск установленных игр */

async function openImport(folder) {
  openDrawer({
    eyebrow: 'Импорт', title: 'Найти игры', wide: true,
    body: [h('div', { class: 'loading' }, h('span', { class: 'spinner' }),
      folder ? `Смотрю папку ${folder}` : 'Смотрю Steam, Epic Games, Riot, Rockstar, RAGE MP, Minecraft, Xbox и Microsoft Store, EA app, Battle.net, Ubisoft, GOG и ваши папки с играми')],
  });
  setStatus('Поиск установленных игр…', 'busy');
  let items;
  try {
    ({ items } = await api('POST', 'scan', folder ? { sources: [], folder } : {}));
  } catch (e) {
    if (!$('drawer').classList.contains('hidden')) {
      fill($('drawerPanel').querySelector('.modal-body'), h('p', { class: 'hint bad' }, e.message));
    }
    notify(e.message, 'bad');
    return;
  }
  if ($('drawer').classList.contains('hidden')) return;

  const picked = new Set(items.filter((i) => !i.in_library).map((i) => i.key));
  const groups = {};
  for (const it of items) (groups[it.source] = groups[it.source] || []).push(it);
  const addBtn = h('button', { class: 'primary-button' });
  const updateCount = () => { addBtn.textContent = picked.size ? `Добавить выбранные (${picked.size})` : 'Добавить выбранные'; addBtn.disabled = !picked.size; };
  addBtn.onclick = async () => {
    addBtn.disabled = true;
    try {
      const { added } = await api('POST', 'import', { items: items.filter((i) => picked.has(i.key)) });
      closeDrawer();
      if (added.length) S.selected = added[0];
      await load();
      notify(added.length ? `Добавлено: ${added.length} ${plural(added.length, 'игра', 'игры', 'игр')}. Обложки подтягиваются` : 'Эти игры уже в библиотеке');
      setTimeout(load, 3000);
    } catch (e) { notify(e.message, 'bad'); updateCount(); }
  };

  const body = [];
  if (!items.length) body.push(h('p', { class: 'hint', style: 'color:var(--muted)' }, 'Ничего не нашлось. Выберите папку, где лежат игры, или добавьте игру вручную — достаточно указать путь к .exe.'));
  for (const [src, list] of Object.entries(groups)) {
    body.push(h('p', { class: 'group-title' }, src), h('ul', { class: 'check-list' }, list.map((it) => h('li', null,
      h('label', { class: 'check-row' + (it.in_library ? ' done' : '') },
        h('input', {
          type: 'checkbox', checked: picked.has(it.key), disabled: it.in_library,
          onchange: (e) => { if (e.target.checked) picked.add(it.key); else picked.delete(it.key); updateCount(); },
        }),
        h('span', { class: 'stack' }, h('b', null, it.name), h('small', { title: it.install_dir || it.target }, it.install_dir || it.target)),
        h('span', { class: 'note' }, it.in_library ? 'Уже в библиотеке'
          : (it.requires.length ? it.requires.map((r) => (launcherById(r) || { name: r }).name).join(' + ') : 'Без лаунчера')))))));
  }
  updateCount();
  setStatus(`Найдено игр: ${items.length}`);
  const panel = $('drawerPanel');
  fill(panel.querySelector('.modal-body'), ...body);
  const actions = h('div', { class: 'modal-actions' },
    h('button', { class: 'secondary-button', onclick: async () => { const p = await browse('folder', 'Папка с играми или ярлыками'); if (p) openImport(p); } }, icon('folder'), 'Выбрать папку…'),
    h('span', { class: 'spacer' }),
    h('button', { class: 'secondary-button', onclick: closeDrawer }, 'Закрыть'),
    addBtn);
  panel.append(actions);
}

function openAddMenu(anchor) {
  openMenu(anchor, [
    { icon: 'scan', label: 'Найти установленные игры', run: () => openImport() },
    { icon: 'plus', label: 'Добавить вручную…', run: () => openEditor(null) },
  ]);
}

/* ================================================================ проверка файлов */

async function runCheck() {
  const btn = $('qaCheck');
  btn.disabled = true;
  const label = btn.querySelector('span').textContent;
  btn.querySelector('span').textContent = 'Проверяю…';
  setStatus('Проверяю пути игр, лаунчеры и обложки…', 'busy');
  try {
    const r = await api('POST', 'check');
    await load();
    const probs = r.problems || [];
    const lines = [];
    lines.push(h('p', null, `Проверено игр: ${r.games}.` + (probs.length ? '' : ' Пути в порядке.')));
    if (r.launchers_found && r.launchers_found.length) lines.push(h('p', null, `Найдены лаунчеры: ${r.launchers_found.join(', ')}.`));
    lines.push(h('p', null, r.art_busy ? 'Обложки уже обновляются в фоне.'
      : r.art_changed ? `Обновлено картинок: ${r.art_changed}.` : 'Обложки и иконки актуальны.'));
    if (probs.length) {
      lines.push(h('ul', { class: 'check-report' }, probs.map((p) => {
        const g = gameById(p.id);
        return h('li', null, icon('alert'), h('span', null, h('b', null, p.name), h('small', null, p.problem)),
          g ? h('button', { class: 'secondary-button small', onclick: () => { $('dialog').classList.add('hidden'); fixGame(g); } }, 'Исправить') : null);
      })));
    }
    openDialog({ title: probs.length ? `Нужно внимание: ${probs.length}` : 'Всё в порядке', body: lines, cancel: 'Закрыть' });
    setStatus(probs.length ? `Проверка: ${probs.length} ${plural(probs.length, 'проблема', 'проблемы', 'проблем')}` : 'Проверка завершена: всё в порядке', probs.length ? 'bad' : '');
  } catch (e) { notify(e.message, 'bad'); }
  finally { btn.disabled = false; btn.querySelector('span').textContent = label; }
}

/* ================================================================ лаунчеры */

function launcherState(L) {
  if (L.running) return ['running', 'live', 'Запущен сейчас'];
  if (L.exists) return ['', 'okk', L.auto ? 'Найден автоматически' : 'Путь указан'];
  if (L.path) return ['missing', 'error', 'Файл не найден — проверьте путь'];
  return ['missing', 'error', 'Не найден — укажите путь к .exe'];
}

async function saveLauncher(id, fields, label) {
  try {
    await api('PUT', `launchers/${id}`, fields);
    S.state = await api('GET', 'state');
    applyPrefs();
    updateLauncherStatus();
    renderStatus();
    if (label) notify(label);
  } catch (e) { notify(e.message, 'bad'); }
}

async function detectLaunchers() {
  try {
    const { changed } = await api('POST', 'launchers/detect');
    await load();
    notify(changed.length ? `Найдено лаунчеров: ${changed.length}` : 'Новых лаунчеров не нашлось');
  } catch (err) { notify(err.message, 'bad'); }
}

function renderLaunchers() {
  if (!S.state) return;
  const q = S.search.trim().toLowerCase();
  const list = S.state.launchers.filter((L) => !q || `${L.name} ${L.desc}`.toLowerCase().includes(q));
  if (!list.length) {
    fill($('launcherList'), h('div', { class: 'empty-state' }, icon('search'), h('h3', null, 'Ничего не найдено'), h('p', null, 'Измените поисковый запрос.')));
    return;
  }
  fill($('launcherList'), list.map((L) => {
    const [dot, cls, text] = launcherState(L);
    const pathInput = h('input', {
      value: L.path, spellcheck: 'false', placeholder: 'Путь к .exe лаунчера', 'aria-label': `Путь к ${L.name}`,
      onchange: (e) => saveLauncher(L.id, { path: e.target.value.trim().replace(/^"|"$/g, '') }, `Путь к ${L.name} сохранён`),
    });
    return h('div', { class: 'launcher-row', 'data-lid': L.id },
      h('div', { class: 'launcher-name' }, launcherIcon(L, dot),
        h('div', null, h('b', null, L.name), h('small', null, L.desc))),
      h('div', { class: 'launcher-path' }, pathInput, h('small', { class: cls }, text)),
      h('button', {
        class: 'secondary-button small browse-button',
        onclick: async () => {
          const p = await browse('exe', `Где находится ${L.name}?`, L.path);
          if (p) { pathInput.value = p; saveLauncher(L.id, { path: p }, `Путь к ${L.name} сохранён`); }
        },
      }, 'Обзор…'),
      h('label', { class: 'launcher-delay', title: 'Сколько секунд подождать после появления лаунчера, чтобы он успел войти в аккаунт' },
        'Пауза', h('input', {
          type: 'number', min: '0', max: '120', value: String(L.delay), 'aria-label': `Пауза для ${L.name}, секунд`,
          onchange: (e) => saveLauncher(L.id, { delay: e.target.value }, `${L.name}: пауза ${e.target.value} с`),
        }), 'с'),
      h('div', { class: 'row-tools' },
        h('button', {
          class: 'secondary-button small open-button', disabled: !L.exists || L.running,
          onclick: async () => {
            try { await api('POST', `launchers/${L.id}/start`); notify(`${L.name} открывается`); setTimeout(refresh, 2500); } catch (e) { notify(e.message, 'bad'); }
          },
        }, 'Открыть'),
        L.builtin ? null : h('button', {
          class: 'icon-small', 'aria-label': `Удалить ${L.name}`, title: 'Удалить свой лаунчер',
          onclick: () => openDialog({
            title: `Удалить «${L.name}»?`, body: [h('p', null, 'Лаунчер пропадёт из списка и из настроек игр. Сама программа не удаляется.')],
            confirm: 'Удалить', danger: true,
            onConfirm: async () => { try { await api('DELETE', `launchers/${L.id}`); await load(); notify('Лаунчер удалён'); } catch (e) { notify(e.message, 'bad'); } },
          }),
        }, icon('trash'))),
      h('details', { class: 'launcher-more' }, h('summary', null, 'Аргументы и процессы'),
        h('div', { class: 'launcher-adv' },
          h('label', null, 'Аргументы запуска', h('input', {
            value: L.args, placeholder: L.id === 'steam' ? 'Например, -silent' : 'Необязательно', spellcheck: 'false',
            onchange: (e) => saveLauncher(L.id, { args: e.target.value }, 'Аргументы сохранены'),
          })),
          h('label', null, 'Процессы через запятую', h('input', {
            value: L.processes.join(', '), spellcheck: 'false',
            onchange: (e) => saveLauncher(L.id, { processes: e.target.value }, 'Список процессов сохранён'),
          })))));
  }));
}

// Статусы обновляются на месте, чтобы не сбивать ввод в полях.
function updateLauncherStatus() {
  for (const L of S.state.launchers) {
    const row = document.querySelector(`[data-lid="${L.id}"]`);
    if (!row) continue;
    const [dot, cls, text] = launcherState(L);
    row.querySelector('.l-icon').className = 'l-icon' + (dot ? ' ' + dot : '');
    const st = row.querySelector('.launcher-path small');
    st.textContent = text;
    st.className = cls;
    const open = row.querySelector('.open-button');
    if (open) open.disabled = !L.exists || L.running;
  }
}

function openAddLauncher() {
  const name = h('input', { id: 'nlName', placeholder: 'Например, Majestic RP или TLauncher' });
  const path = h('input', { id: 'nlPath', placeholder: 'C:\\Games\\Launcher\\launcher.exe', spellcheck: 'false' });
  const delay = h('input', { id: 'nlDelay', type: 'number', min: '0', max: '120', value: '3' });
  openDrawer({
    eyebrow: 'Лаунчеры', title: 'Свой лаунчер',
    body: [
      h('p', { class: 'hint', style: 'color:var(--muted);margin:0' }, 'Например, лаунчер RP-проекта. Отметьте его в настройках игры, и он будет открываться перед ней.'),
      field('Название', name),
      h('div', { class: 'field' }, h('label', { for: 'nlPath' }, 'Путь к .exe'),
        h('div', { class: 'with-button' }, path, h('button', {
          class: 'secondary-button',
          onclick: async () => {
            const p = await browse('exe', 'Выберите .exe лаунчера', path.value);
            if (p) { path.value = p; if (!name.value) name.value = p.split(/[\\/]/).pop().replace(/\.exe$/i, ''); }
          },
        }, 'Обзор…'))),
      field('Пауза после запуска, секунд', delay),
    ],
    actions: [h('span', { class: 'spacer' }),
      h('button', { class: 'secondary-button', onclick: closeDrawer }, 'Отмена'),
      h('button', {
        class: 'primary-button',
        onclick: async () => {
          try {
            await api('POST', 'launchers', { name: name.value, path: path.value.trim().replace(/^"|"$/g, ''), delay: Number(delay.value) || 0 });
            closeDrawer();
            await load();
            notify('Лаунчер добавлен');
          } catch (e) { notify(e.message, 'bad'); }
        },
      }, 'Добавить лаунчер')],
  });
}

/* ================================================================ настройки */

async function saveSetting(patch, msg) {
  try {
    const r = await api('PUT', 'settings', patch);
    S.state.settings = r.settings;
    applyPrefs();
    if (S.view === 'settings') renderSettings();
    if (patch.sort || patch.view) renderLibrary();
    if (msg) notify(msg);
  } catch (e) {
    notify(e.message, 'bad');
    if (S.view === 'settings') renderSettings();
  }
}

function settingSwitch(id, title, note, checked, onChange) {
  return h('div', { class: 'setting-row' },
    h('div', null, h('b', { id: id + 'Label' }, title), h('small', null, note)),
    h('label', { class: 'switch' }, h('input', { type: 'checkbox', checked, 'aria-labelledby': id + 'Label', onchange: (e) => onChange(e.target.checked) }), h('span')));
}

function segmented(label, value, options, onPick) {
  return h('div', { class: 'segmented', role: 'group', 'aria-label': label },
    options.map(([v, l]) => h('button', { class: value === v ? 'active' : '', 'aria-pressed': String(value === v), onclick: () => onPick(v) }, l)));
}

function renderSettings() {
  if (!S.state) return;
  const st = S.state.settings;
  const accent = (key, label) => h('button', {
    class: 'accent-choice' + (st.accent === key ? ' active' : ''), 'aria-pressed': String(st.accent === key),
    onclick: () => saveSetting({ accent: key }, `Акцент: ${label.toLowerCase()}`),
  }, h('i', { class: key + '-dot' }), label);

  const addFolder = async (p) => {
    if (!p) return;
    try { const r = await api('POST', 'folders', { path: p }); S.state.library_folders = r.library_folders; renderSettings(); notify('Папка добавлена'); } catch (e) { notify(e.message, 'bad'); }
  };
  const folderInput = h('input', { placeholder: 'D:\\Games', spellcheck: 'false', 'aria-label': 'Путь к папке с играми' });
  const folders = S.state.library_folders;

  fill($('settingsGrid'),
    h('section', { class: 'settings-card' },
      h('h2', null, icon('image'), 'Внешний вид'),
      h('p', null, 'Цвет акцента и вид библиотеки.'),
      h('div', { class: 'accent-options' }, accent('amber', 'Янтарный'), accent('violet', 'Фиолетовый'), accent('cyan', 'Бирюзовый')),
      h('div', { class: 'setting-row' },
        h('div', null, h('b', null, 'Вид библиотеки'), h('small', null, 'Карточки с обложками или компактный список')),
        segmented('Вид библиотеки', st.view, [['grid', 'Сетка'], ['list', 'Список']], (v) => saveSetting({ view: v }))),
      settingSwitch('setCompact', 'Компактные карточки', 'Больше игр на экране', !!st.compact, (v) => saveSetting({ compact: v })),
      settingSwitch('setMotion', 'Анимации интерфейса', 'Плавные переходы и подсветка', !st.reduce_motion, (v) => saveSetting({ reduce_motion: !v }))),
    h('section', { class: 'settings-card' },
      h('h2', null, icon('rocket'), 'Поведение запуска'),
      h('p', null, 'Что происходит после нажатия «Запустить игру».'),
      settingSwitch('setConfirm', 'Подтверждать запуск', 'Спрашивать перед запуском игры', !!st.confirm_launch, (v) => saveSetting({ confirm_launch: v }, v ? 'Запуск будет требовать подтверждения' : 'Подтверждение отключено')),
      settingSwitch('setToasts', 'Показывать уведомления', 'Всплывающие сообщения о результатах. Ошибки показываются всегда', !st.hide_toasts, (v) => saveSetting({ hide_toasts: !v })),
      h('div', { class: 'setting-row' },
        h('div', null, h('b', null, 'После запуска игры'), h('small', null, 'Окно Zero Launcher можно закрывать автоматически')),
        segmented('После запуска игры', st.after_launch, [['none', 'Оставить'], ['close', 'Закрыть']],
          (v) => saveSetting({ after_launch: v }, v === 'close' ? 'Окно будет закрываться после запуска игры' : 'Окно будет оставаться открытым'))),
      h('div', { class: 'setting-row' },
        h('div', null, h('b', null, 'Ожидание лаунчера'), h('small', null, 'Если лаунчер не открылся за это время, игра всё равно запустится')),
        h('label', { class: 'number-field' }, h('input', {
          type: 'number', min: '10', max: '600', value: String(st.launcher_timeout), 'aria-label': 'Ожидание лаунчера, секунд',
          onchange: (e) => saveSetting({ launcher_timeout: e.target.value }, `Ожидание лаунчера: ${e.target.value} с`),
        }), 'с'))),
    h('section', { class: 'settings-card' },
      h('h2', null, icon('refresh'), 'Обложки и иконки'),
      h('p', null, 'Подбираются сами: картинки Steam (в том числе ваши), каталог Epic Games, метаданные Riot и иконки из .exe. Когда игра или лаунчер обновляет картинку, она меняется и здесь.'),
      settingSwitch('setOnline', 'Загружать обложки из интернета', 'Только картинки с серверов Steam и Epic Games, если на компьютере их нет', !st.no_online_art,
        (v) => saveSetting({ no_online_art: !v }, v ? 'Обложки будут подгружаться из интернета' : 'Только локальные картинки')),
      h('div', { class: 'setting-row' },
        h('div', null, h('b', null, 'Проверить сейчас'), h('small', null, 'Локальные источники сверяются каждые 3 минуты, интернет — раз в сутки')),
        h('button', {
          class: 'secondary-button small',
          onclick: async () => { try { await api('POST', 'art/refresh'); notify('Обложки обновляются — новые появятся через несколько секунд'); setTimeout(load, 4000); setTimeout(load, 12000); } catch (e) { notify(e.message, 'bad'); } },
        }, icon('refresh'), 'Обновить все'))),
    h('section', { class: 'settings-card' },
      h('h2', null, icon('folder'), 'Папки с играми'),
      h('p', null, 'Здесь «Найти игры» ищет игры и ярлыки.'),
      folders.length ? h('ul', { class: 'folder-list' }, folders.map((f) => h('li', null, icon('folder'), h('span', { title: f }, f),
        h('button', {
          class: 'ghost-button small',
          onclick: async () => { try { const r = await api('DELETE', 'folders', { path: f }); S.state.library_folders = r.library_folders; renderSettings(); } catch (e) { notify(e.message, 'bad'); } },
        }, 'Убрать')))) : h('p', { class: 'muted-note', style: 'margin:0 0 12px' }, 'Пока ни одной папки.'),
      S.state.is_windows
        ? h('button', { class: 'secondary-button', onclick: async () => addFolder(await browse('folder', 'Папка, где лежат игры')) }, icon('plus'), 'Добавить папку')
        : h('div', { class: 'inline-form' }, folderInput, h('button', { class: 'secondary-button', onclick: () => addFolder(folderInput.value.trim()) }, 'Добавить'))));
}

function renderAbout() {
  const st = S.state;
  fill($('aboutView'), h('div', { class: 'about' },
    h('section', { class: 'about-hero' }, logoZ(),
      h('div', null, h('h2', null, `Zero Launcher ${st.version}`),
        h('p', null, 'Все игры в одном окне и запуск одной кнопкой. Перед онлайн-игрой Zero Launcher сам открывает нужные лаунчеры и ждёт, пока они загрузятся. Для GTA всегда открывается Social Club.'))),
    h('section', { class: 'settings-card' },
      h('h2', null, icon('bolt'), 'Что умеет'),
      h('ul', { class: 'feature-list' },
        [['rocket', 'Steam, Epic Games, Riot, EA, Ubisoft, Battle.net, Rockstar и RAGE MP открываются по очереди, уже открытые пропускаются.'],
          ['lock', 'Для GTA всегда запускается Social Club.'],
          ['server', 'GTA 5 RP: сервер выбирается один раз, дальше — сразу подключение.'],
          ['play', 'VALORANT и League of Legends запускаются сразу в игру, а не только в клиент.'],
          ['image', 'Обложки и иконки подбираются сами и обновляются вместе с игрой.'],
          ['shield', '«Проверить файлы» находит игры и лаунчеры, у которых пропал путь.']].map(([ic, t]) => h('li', null, icon(ic), h('span', null, t))))),
    h('section', { class: 'settings-card' },
      h('h2', null, icon('folder'), 'Данные'),
      h('p', null, 'Библиотека, настройки, обложки и журнал запусков хранятся в этой папке. Интернет нужен только для загрузки обложек, и его можно отключить в настройках.'),
      h('div', { class: 'data-path' }, st.data_dir),
      h('button', { class: 'secondary-button', onclick: () => api('POST', 'data-dir').catch((e) => notify(e.message, 'bad')) }, icon('folder'), 'Открыть папку данных'),
      h('p', { class: 'muted-note' }, 'Go + HTML/CSS/JavaScript · окно Microsoft Edge · шрифт Inter (SIL OFL 1.1)'))));
}

/* ================================================================ события */

document.addEventListener('click', (e) => {
  const nav = e.target.closest('[data-view]');
  if (nav) { e.preventDefault(); setView(nav.dataset.view); return; }
  if (!e.target.closest('#menu') && !e.target.closest('.kebab') && !e.target.closest('#qaAdd')) closeMenu();
});
$('globalSearch').addEventListener('input', (e) => {
  S.search = e.target.value;
  if (S.view === 'launchers') renderLaunchers();
  else if (!LIB_VIEWS.has(S.view) || S.view === 'home' && S.search) setView(S.search ? 'all' : S.view);
  else renderGrid();
});
$('sortSelect').addEventListener('change', (e) => saveSetting({ sort: e.target.value }));
for (const b of document.querySelectorAll('.view-toggle button')) b.addEventListener('click', () => saveSetting({ view: b.dataset.mode }));
$('qaLaunch').addEventListener('click', () => { const g = quickGame(); if (g) launch(g.id); });
$('qaAdd').addEventListener('click', (e) => openAddMenu(e.currentTarget));
$('qaSettings').addEventListener('click', () => setView('settings'));
$('qaCheck').addEventListener('click', runCheck);
$('mobileMenu').addEventListener('click', () => {
  const open = $('topnav').classList.toggle('open');
  $('mobileMenu').setAttribute('aria-expanded', String(open));
});
$('main').addEventListener('scroll', positionMenu, { passive: true });
window.addEventListener('scroll', positionMenu, { passive: true });
$('filterPills').addEventListener('scroll', markOverflow, { passive: true });
window.addEventListener('resize', () => { closeMenu(); markOverflow(); });
$('drawer').addEventListener('click', (e) => { if (e.target === $('drawer')) closeDrawer(); });
$('dialog').addEventListener('click', (e) => { if (e.target === $('dialog')) $('dialog').classList.add('hidden'); });
$('menu').addEventListener('keydown', (e) => {
  const items = [...$('menu').querySelectorAll('button')];
  const i = items.indexOf(document.activeElement);
  if (e.key === 'ArrowDown') { e.preventDefault(); items[(i + 1) % items.length].focus(); }
  if (e.key === 'ArrowUp') { e.preventDefault(); items[(i - 1 + items.length) % items.length].focus(); }
  if (e.key === 'Tab') closeMenu();
});
// Окно лаунчера — не браузер: без контекстного меню, перетаскивания картинок, печати, сохранения страницы,
// поиска по странице и перезагрузки. Поиск Ctrl+F ведёт в поиск игр.
document.addEventListener('contextmenu', (e) => e.preventDefault());
document.addEventListener('dragstart', (e) => e.preventDefault());
window.addEventListener('dragover', (e) => e.preventDefault());
window.addEventListener('drop', (e) => e.preventDefault()); // иначе брошенный в окно файл откроется вместо лаунчера
document.addEventListener('mousedown', (e) => { if (e.button === 1) e.preventDefault(); }); // автопрокрутка колесом
document.addEventListener('auxclick', (e) => e.preventDefault());
document.addEventListener('mouseup', (e) => { if (e.button === 3 || e.button === 4) e.preventDefault(); }); // кнопки «назад/вперёд»
const BLOCKED_CTRL = new Set(['p', 's', 'u', 'o', 'n', 't', 'j', 'h', 'd', 'g', 'r', 'e', 'k', 'l']); // Ctrl+W оставляем: закрывает окно
document.addEventListener('keydown', (e) => {
  const k = (e.key || '').toLowerCase();
  const typing = e.target.closest && e.target.closest('input, textarea, select');
  if ((e.ctrlKey || e.metaKey) && !e.altKey) {
    if (k === 'f') { e.preventDefault(); $('globalSearch').focus(); $('globalSearch').select(); return; }
    if (e.shiftKey && ['i', 'j', 'c', 'n', 'p', 'delete', 'b', 'o'].includes(k)) { e.preventDefault(); return; }
    if (BLOCKED_CTRL.has(k)) { e.preventDefault(); return; }
  }
  if (['F3', 'F5', 'F7', 'F12', 'BrowserBack', 'BrowserForward', 'BrowserRefresh', 'BrowserSearch'].includes(e.key)) { e.preventDefault(); return; }
  if (e.altKey && (e.key === 'ArrowLeft' || e.key === 'ArrowRight' || e.key === 'Home')) { e.preventDefault(); return; }
  if (!typing && S.view === 'home' && !e.ctrlKey && !e.altKey && !e.metaKey &&
      $('drawer').classList.contains('hidden') && $('dialog').classList.contains('hidden') && $('menu').classList.contains('hidden') &&
      (e.key === 'ArrowLeft' || e.key === 'ArrowRight')) {
    e.preventDefault();
    heroStep(e.key === 'ArrowRight' ? 1 : -1);
  }
}, true);
document.addEventListener('keydown', (e) => {
  if (e.key === 'Escape') {
    if (!$('menu').classList.contains('hidden')) closeMenu();
    else if (!$('dialog').classList.contains('hidden')) $('dialog').classList.add('hidden');
    else if (!$('drawer').classList.contains('hidden')) closeDrawer();
    else if ($('topnav').classList.contains('open')) $('topnav').classList.remove('open');
    return;
  }
  if (e.key === '/' && document.activeElement === document.body) { e.preventDefault(); $('globalSearch').focus(); }
});

// Программа живёт, пока открыто окно: «пинг» каждые 10 с и «пока» при закрытии.
async function ping() {
  try { await api('POST', 'ping'); setConnected(true); } catch (_) { setConnected(false); }
}
window.addEventListener('pagehide', () => navigator.sendBeacon(`/api/bye?t=${encodeURIComponent(T)}`));
window.addEventListener('focus', () => { if ($('drawer').classList.contains('hidden')) refresh(); });

setInterval(ping, 10000);
setInterval(refresh, 5000);
setInterval(idleStatus, 60000);
ping();
setView('home');
api('GET', 'templates').then((r) => { S.templates = r.templates; if (S.state && !games().length) renderGrid(); }).catch(() => {});
load();
