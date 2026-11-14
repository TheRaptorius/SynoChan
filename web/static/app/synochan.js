'use strict';

(() => {
  // Only run inside the SynoChan page, never in a page that merely loaded this script.
  if (!document.documentElement.classList.contains('synochan') || !document.getElementById('app')) return;

  const $ = (sel) => document.querySelector(sel);
  const el = (tag, props = {}, ...children) => {
    const node = document.createElement(tag);
    for (const [k, v] of Object.entries(props)) {
      if (k === 'class') node.className = v;
      else if (k === 'dataset') Object.assign(node.dataset, v);
      else if (k.startsWith('on')) node.addEventListener(k.slice(2), v);
      else if (v !== undefined && v !== null && v !== false) node.setAttribute(k, v === true ? '' : v);
    }
    for (const c of children.flat()) {
      if (c !== null && c !== undefined && c !== false) node.append(c instanceof Node ? c : document.createTextNode(String(c)));
    }
    return node;
  };

  const NO_FOLDER_YET = 'The folder is created with the first file';

  const ICONS = {
    folder: '<svg viewBox="0 0 20 20" aria-hidden="true"><path d="M2 5.5A1.5 1.5 0 0 1 3.5 4h4l1.6 1.8h7.4A1.5 1.5 0 0 1 18 7.3v7.2a1.5 1.5 0 0 1-1.5 1.5h-13A1.5 1.5 0 0 1 2 14.5z"/></svg>',
    more: '<svg viewBox="0 0 20 20" aria-hidden="true"><circle cx="4" cy="10" r="1.6"/><circle cx="10" cy="10" r="1.6"/><circle cx="16" cy="10" r="1.6"/></svg>',
  };

  // ---------- API ----------

  function synoToken() {
    // Passed in the page URL by the DSM window; read from DSM itself as a fallback.
    const fromURL = new URLSearchParams(window.location.search).get('SynoToken');
    if (fromURL) return fromURL;
    try {
      const s = window.top.SYNO && window.top.SYNO.SDS && window.top.SYNO.SDS.Session;
      return (s && s.SynoToken) || '';
    } catch (e) {
      return '';
    }
  }

  async function api(path, { method = 'GET', body, query = {} } = {}) {
    const params = new URLSearchParams({ path, ...query });
    const headers = { 'Accept': 'application/json' };
    const token = synoToken();
    if (token) headers['X-SYNO-TOKEN'] = token;
    if (body !== undefined) headers['Content-Type'] = 'application/json';
    const res = await fetch('api.cgi?' + params.toString(), {
      method,
      headers,
      credentials: 'same-origin',
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    let data = null;
    try { data = await res.json(); } catch (e) { /* Non JSON error page. */ }
    if (!res.ok) {
      const err = new Error((data && data.error) || `Request failed (HTTP ${res.status})`);
      err.status = res.status;
      throw err;
    }
    return data;
  }

  // ---------- State ----------

  const state = {
    tab: 'threads',
    rev: null,
    snapshot: { threads: [], boards: [], queue: {} },
    selected: { threads: new Set(), boards: new Set() },
    anchor: null,
    sort: loadJSON('sort', { threads: { key: 'added_at', dir: 'desc' }, boards: { key: 'code', dir: 'asc' } }),
    hidden: loadJSON('hiddenColumns', { threads: [], boards: [] }),
    filter: '',
    connectionError: null,
    frozenOrder: { threads: null, boards: null },
  };

  function loadJSON(key, fallback) {
    try {
      const v = JSON.parse(localStorage.getItem('synochan.' + key));
      return v && typeof v === 'object' ? { ...fallback, ...v } : fallback;
    } catch (e) {
      return fallback;
    }
  }
  function saveJSON(key, value) {
    try { localStorage.setItem('synochan.' + key, JSON.stringify(value)); } catch (e) { /* Storage unavailable. */ }
  }

  // ---------- Formatting ----------

  function relTime(iso, now = Date.now()) {
    if (!iso) return '';
    const t = Date.parse(iso);
    if (Number.isNaN(t)) return '';
    const diff = Math.round((t - now) / 1000);
    const abs = Math.abs(diff);
    let text;
    if (abs < 5) return diff >= 0 ? 'now' : 'just now';
    if (abs < 60) text = `${abs} s`;
    else if (abs < 3600) text = `${Math.round(abs / 60)} min`;
    else if (abs < 86400) text = `${Math.round(abs / 3600)} h`;
    else text = `${Math.round(abs / 86400)} d`;
    return diff >= 0 ? `in ${text}` : `${text} ago`;
  }
  const absTime = (iso) => (iso ? new Date(iso).toLocaleString() : '');

  // ---------- Columns ----------

  const COLUMNS = {
    threads: [
      { key: 'subject', label: 'Subject', sort: (t) => t.subject.toLowerCase(), cell: subjectCell, fixed: true },
      { key: 'board', label: 'Board', sort: (t) => t.board, cell: (t) => el('td', { class: 'board' }, `/${t.board}/`) },
      { key: 'id', label: 'Number', num: true, sort: (t) => t.id, cell: (t) => el('td', { class: 'num muted' }, t.id) },
      { key: 'files', label: 'Files', num: true, sort: (t) => t.file_count ?? -1, cell: filesCell },
      { key: 'last_check', label: 'Checked', sort: (t) => t.last_check || '', cell: (t) => el('td', { class: 'when', title: absTime(t.last_check) }, relTime(t.last_check)) },
      { key: 'next_check', label: 'Next check', sort: (t) => t.next_check || '', cell: (t) => el('td', { class: 'when', title: absTime(t.next_check) }, nextCheck(t)) },
      { key: 'added_at', label: 'Added', sort: (t) => t.added_at || '', cell: (t) => el('td', { class: 'when', title: absTime(t.added_at) }, relTime(t.added_at)) },
      { key: 'last_error', label: 'Problem', sort: (t) => t.last_error || '', cell: (t) => el('td', { class: 'err', title: t.last_error || '' }, t.last_error || '') },
      { key: 'save_to', label: 'Folder', hiddenByDefault: true, sort: (t) => t.save_to, cell: (t) => el('td', { class: 'muted' }, t.share_path || t.save_to) },
    ],
    boards: [
      { key: 'code', label: 'Board', sort: (b) => b.code, fixed: true, cell: (b) => el('td', { class: 'board' }, `/${b.code}/`,
        hasOwnFilters(b) ? el('span', { class: 'own-filters', title: 'This board has its own filters' }, 'filters') : null) },
      { key: 'thread_count', label: 'Threads on board', num: true, sort: (b) => b.thread_count, cell: (b) => el('td', { class: 'num' }, b.thread_count) },
      { key: 'filtered', label: 'Filtered', num: true, sort: (b) => b.filtered || 0, cell: (b) => el('td', { class: 'num' },
        b.filtered ? el('button', { type: 'button', class: 'count-link', title: 'Show filtered threads', onclick: (e) => { e.stopPropagation(); showFiltered(b); } }, b.filtered) : '0') },
      { key: 'last_check', label: 'Checked', sort: (b) => b.last_check || '', cell: (b) => el('td', { class: 'when', title: absTime(b.last_check) }, relTime(b.last_check)) },
      { key: 'next_check', label: 'Next check', sort: (b) => b.next_check || '', cell: (b) => el('td', { class: 'when', title: absTime(b.next_check) }, nextCheck(b)) },
      { key: 'added_at', label: 'Added', sort: (b) => b.added_at || '', cell: (b) => el('td', { class: 'when', title: absTime(b.added_at) }, relTime(b.added_at)) },
      { key: 'last_error', label: 'Problem', sort: (b) => b.last_error || '', cell: (b) => el('td', { class: 'err', title: b.last_error || '' }, b.last_error || '') },
      { key: 'save_to', label: 'Folder', hiddenByDefault: true, sort: (b) => b.save_to, cell: (b) => el('td', { class: 'muted' }, b.share_path || b.save_to) },
    ],
  };
  // Columns hidden by default start hidden unless the user changed the column set.
  for (const tab of ['threads', 'boards']) {
    if (!localStorage.getItem('synochan.hiddenColumns')) {
      state.hidden[tab] = COLUMNS[tab].filter((c) => c.hiddenByDefault).map((c) => c.key);
    }
  }

  function nextCheck(item) {
    if (item.pending) return 'waiting for your decision';
    if (state.snapshot.queue.paused) return 'paused';
    if (!item.next_check) return 'soon';
    return Date.parse(item.next_check) <= Date.now() ? 'waiting in queue' : relTime(item.next_check);
  }

  function subjectCell(t) {
    const badge = t.priority ? el('span', { class: 'prio', title: `Download priority ${t.priority}` }, `#${t.priority}`) : null;
    const text = el('span', { class: t.loading ? 'text loading' : 'text', title: t.subject }, badge, el('span', { class: 'label' }, t.subject));
    return el('td', { class: 'subject' }, text);
  }

  function filesCell(t) {
    if (t.file_count === null || t.file_count === undefined) return el('td', { class: 'num muted' }, '–');
    const total = t.file_count;
    const saved = Math.min(t.saved, total);
    const pct = total > 0 ? Math.round((saved / total) * 100) : 100;
    const done = saved >= total;
    const title = `${saved} of ${total} files saved` + (t.pending ? `, ${t.pending} queued` : '');
    return el('td', { class: 'num', title },
      el('span', { class: done ? 'files done' : 'files' },
        el('span', {}, `${saved} / ${total}`),
        el('span', { class: 'bar-track' }, el('span', { class: 'bar-fill', style: `width:${pct}%` }))));
  }

  const visibleColumns = (tab) => COLUMNS[tab].filter((c) => c.fixed || !state.hidden[tab].includes(c.key));

  // ---------- Rendering ----------

  function items(tab = state.tab) {
    let list = state.snapshot[tab] || [];
    const f = state.filter.trim().toLowerCase();
    if (f) {
      list = list.filter((x) => [x.subject, x.board, x.code, String(x.id || ''), x.url]
        .some((v) => v && String(v).toLowerCase().includes(f)));
    }
    const sort = state.sort[tab];
    const col = COLUMNS[tab].find((c) => c.key === sort.key) || COLUMNS[tab][0];
    const dir = sort.dir === 'asc' ? 1 : -1;
    // While something is selected, rows keep their place, so a row does not move under the pointer between
    // clicks. New rows go to the end; the sort applies again once the selection is cleared.
    const frozen = state.selected[tab].size > 0 ? state.frozenOrder[tab] : null;
    if (!frozen) state.frozenOrder[tab] = null;
    const sorted = [...list].sort((a, b) => {
      // Prioritised threads are pinned on top, by priority.
      const pa = a.priority || Infinity, pb = b.priority || Infinity;
      if (pa !== pb) return pa - pb;
      if (frozen) {
        const ia = frozen.has(a.key) ? frozen.get(a.key) : Infinity;
        const ib = frozen.has(b.key) ? frozen.get(b.key) : Infinity;
        if (ia !== ib) return ia - ib;
      }
      const x = col.sort(a), y = col.sort(b);
      return (x < y ? -1 : x > y ? 1 : 0) * dir;
    });
    if (state.selected[tab].size > 0 && !frozen) {
      state.frozenOrder[tab] = new Map(sorted.map((x, i) => [x.key, i]));
    }
    return sorted;
  }

  function renderHead() {
    const tab = state.tab;
    const all = items();
    const sel = state.selected[tab];
    const allChecked = all.length > 0 && all.every((x) => sel.has(x.key));
    const selectAll = el('input', { type: 'checkbox', 'aria-label': 'Select all', onchange: (e) => {
      sel.clear();
      if (e.target.checked) all.forEach((x) => sel.add(x.key));
      render();
    } });
    selectAll.checked = allChecked;
    selectAll.indeterminate = !allChecked && all.some((x) => sel.has(x.key));
    const sort = state.sort[tab];
    const tr = el('tr', {}, el('th', { class: 'sel' }, selectAll));
    for (const c of visibleColumns(tab)) {
      const ariaSort = sort.key === c.key ? (sort.dir === 'asc' ? 'ascending' : 'descending') : undefined;
      tr.append(el('th', {
        class: c.num ? 'num' : '', 'data-sort': c.key, 'aria-sort': ariaSort, scope: 'col', tabindex: 0,
        onclick: () => toggleSort(c.key), onkeydown: (e) => { if (e.key === 'Enter') toggleSort(c.key); },
      }, c.label));
    }
    tr.append(el('th', {}, el('span', { class: 'visually-hidden' }, 'Actions')));
    $('#thead').replaceChildren(tr);
  }

  function toggleSort(key) {
    const s = state.sort[state.tab];
    if (s.key === key) s.dir = s.dir === 'asc' ? 'desc' : 'asc';
    else { s.key = key; s.dir = 'asc'; }
    state.frozenOrder[state.tab] = null;
    saveJSON('sort', state.sort);
    render();
  }

  function renderBody() {
    const tab = state.tab;
    const list = items();
    const sel = state.selected[tab];
    const cols = visibleColumns(tab);
    const rows = list.map((x) => {
      const checkbox = el('input', { type: 'checkbox', 'aria-label': 'Select', onclick: (e) => {
        e.stopPropagation();
        toggleSelect(x.key, { additive: true, range: e.shiftKey });
      } });
      checkbox.checked = sel.has(x.key);
      const tr = el('tr', {
        class: [sel.has(x.key) ? 'selected' : '', x.last_error ? 'has-error' : '', x.priority ? 'pinned' : ''].join(' ').trim(),
        dataset: { key: x.key },
        onclick: (e) => toggleSelect(x.key, { additive: e.ctrlKey || e.metaKey, range: e.shiftKey }),
        ondblclick: () => { if (x.folder_exists) openFolder(x); },
        oncontextmenu: (e) => {
          e.preventDefault();
          if (!sel.has(x.key)) { sel.clear(); sel.add(x.key); render(); }
          showContextMenu(x, e.clientX, e.clientY);
        },
      }, el('td', { class: 'sel' }, checkbox));
      for (const c of cols) tr.append(c.cell(x));
      const folderBtn = el('button', {
        type: 'button',
        title: x.folder_exists ? 'Show folder in File Station' : NO_FOLDER_YET,
        disabled: !x.folder_exists,
        onclick: (e) => { e.stopPropagation(); openFolder(x); },
      });
      folderBtn.innerHTML = ICONS.folder;
      const moreBtn = el('button', { type: 'button', title: 'More actions', 'aria-haspopup': 'menu', onclick: (e) => {
        e.stopPropagation();
        // A second click on the same button closes the menu again.
        if (!$('#context-menu').hidden && openMenuKey === x.key) { hideMenus(); return; }
        // Measured before re-rendering, which replaces this button.
        const r = e.currentTarget.getBoundingClientRect();
        if (!sel.has(x.key)) { sel.clear(); sel.add(x.key); render(); }
        showContextMenu(x, r.right, r.bottom);
      } });
      moreBtn.innerHTML = ICONS.more;
      tr.append(el('td', { class: 'actions' }, folderBtn, moreBtn));
      return tr;
    });
    $('#tbody').replaceChildren(...rows);

    const empty = $('#empty');
    const total = (state.snapshot[tab] || []).length;
    if (state.connectionError) {
      empty.replaceChildren(el('strong', {}, 'Cannot reach SynoChan'), state.connectionError);
      empty.hidden = false;
    } else if (total === 0) {
      empty.replaceChildren(
        el('strong', {}, tab === 'threads' ? 'No threads tracked' : 'No boards tracked'),
        tab === 'threads'
          ? 'Paste a thread link above, or drop links anywhere in this window. Files are saved as they are posted until the thread is archived.'
          : 'Paste a board link (like https://boards.4chan.org/wg/catalog) above. Every new thread on the board is tracked automatically.',
        state.snapshot.save_exists
          ? el('div', {}, el('button', { type: 'button', class: 'link-button', onclick: openRoot }, 'Open download folder'))
          : null);
      empty.hidden = false;
    } else if (list.length === 0) {
      empty.replaceChildren(el('strong', {}, 'Nothing matches the filter'));
      empty.hidden = false;
    } else {
      empty.hidden = true;
    }
  }

  function renderChrome() {
    const snap = state.snapshot;
    $('#count-threads').textContent = snap.threads.length;
    $('#count-boards').textContent = snap.boards.length;
    for (const t of document.querySelectorAll('[role="tab"]')) {
      t.setAttribute('aria-selected', String(t.dataset.tab === state.tab));
    }
    const paused = !!snap.queue.paused;
    const pause = $('#pause-button');
    pause.setAttribute('aria-pressed', String(paused));
    pause.textContent = paused ? 'Resume' : 'Pause';
    pause.title = paused ? 'Resume checking and downloading' : 'Pause all checking and downloading';

    const rootOK = !!(snap.save_exists && snap.save_share_path);
    for (const b of [$('#open-root'), $('#menu-open-root')]) {
      b.disabled = !rootOK;
      b.title = rootOK ? snap.save_path : 'The download folder does not exist yet';
    }

    const selCount = state.selected[state.tab].size;
    $('#remove-selected').hidden = selCount === 0;
    $('#remove-selected').textContent = `Remove selected (${selCount})`;
    $('#clear-all').hidden = selCount > 0;

    const q = snap.queue;
    const status = $('#status');
    const parts = [];
    if (state.connectionError) {
      parts.push(el('span', {}, el('span', { class: 'dot wait' }), 'Disconnected'));
    } else if (paused) {
      parts.push(el('span', {}, el('span', { class: 'dot paused' }), 'Paused'));
    } else if (q.backoff_until) {
      parts.push(el('span', {}, el('span', { class: 'dot wait' }), `Rate limited by the site, continuing ${relTime(q.backoff_until)}`));
    } else {
      const pending = snap.threads.reduce((n, t) => n + (t.pending || 0), 0);
      let label = 'Idle';
      if (pending) label = `Downloading, ${pending} file${pending > 1 ? 's' : ''} left`;
      else if (q.active) label = 'Checking for new posts';
      parts.push(el('span', {}, el('span', { class: 'dot' }), label));
    }
    const next = [...snap.threads, ...snap.boards].map((x) => x.next_check).filter(Boolean).sort()[0];
    if (!paused && next && Date.parse(next) > Date.now()) parts.push(el('span', {}, `Next check ${relTime(next)}`));
    parts.push(el('span', { class: 'spacer' }));
    status.replaceChildren(...parts);
  }

  function render() {
    renderChrome();
    renderHead();
    renderBody();
  }

  function toggleSelect(key, { additive, range }) {
    const sel = state.selected[state.tab];
    const list = items();
    if (range && state.anchor) {
      const a = list.findIndex((x) => x.key === state.anchor);
      const b = list.findIndex((x) => x.key === key);
      if (a >= 0 && b >= 0) {
        if (!additive) sel.clear();
        for (let i = Math.min(a, b); i <= Math.max(a, b); i++) sel.add(list[i].key);
        render();
        return;
      }
    }
    if (additive) {
      if (sel.has(key)) sel.delete(key); else sel.add(key);
    } else {
      const only = sel.size === 1 && sel.has(key);
      sel.clear();
      if (!only) sel.add(key);
    }
    state.anchor = key;
    render();
  }

  // ---------- Polling ----------

  let pollTimer = null;
  async function poll() {
    clearTimeout(pollTimer);
    try {
      const query = state.rev === null ? {} : { rev: state.rev };
      const data = await api('state', { query });
      if (state.connectionError) { state.connectionError = null; }
      if (data.unchanged) {
        state.snapshot.queue = data.queue;
      } else {
        state.snapshot = data;
        state.rev = data.rev;
        // Drop selections of items that are gone.
        for (const tab of ['threads', 'boards']) {
          const keys = new Set(data[tab].map((x) => x.key));
          for (const k of state.selected[tab]) if (!keys.has(k)) state.selected[tab].delete(k);
        }
      }
      if (data.unchanged) renderChrome();
      else render();
    } catch (e) {
      state.connectionError = e.message;
      render();
    }
    pollTimer = setTimeout(poll, document.hidden ? 10000 : 2000);
  }
  document.addEventListener('visibilitychange', () => { if (!document.hidden) poll(); });
  // Relative times tick even when nothing changes.
  setInterval(() => { if (!document.hidden) renderBody(); }, 15000);

  function refreshSoon() { state.rev = null; poll(); }

  // ---------- Toasts ----------

  function toast(message, { error = false, action, timeout = 5000 } = {}) {
    const node = el('div', { class: error ? 'toast error' : 'toast', role: error ? 'alert' : 'status' }, el('span', {}, message));
    if (action) {
      node.append(el('button', { type: 'button', onclick: () => { action.run(); node.remove(); } }, action.label));
    }
    $('#toasts').append(node);
    setTimeout(() => node.remove(), action ? Math.max(timeout, 9000) : timeout);
  }

  // ---------- Clipboard ----------

  async function copyText(text) {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch (e) {
      const ta = el('textarea', { style: 'position:fixed;opacity:0' });
      ta.value = text;
      document.body.append(ta);
      ta.select();
      let ok = false;
      try { ok = document.execCommand('copy'); } catch (err) { ok = false; }
      ta.remove();
      return ok;
    }
  }

  async function readClipboard() {
    try {
      return await navigator.clipboard.readText();
    } catch (e) {
      return null;
    }
  }

  // ---------- Actions ----------

  async function addText(text, { stayOnTab = false } = {}) {
    try {
      const { results } = await api('add', { method: 'POST', body: { text } });
      const added = results.filter((r) => r.status === 'added');
      const dups = results.filter((r) => r.status === 'duplicate');
      const invalid = results.filter((r) => r.status === 'invalid');
      if (added.length) {
        const threads = added.filter((r) => r.kind === 'thread').length;
        const boards = added.length - threads;
        const what = [threads && `${threads} thread${threads > 1 ? 's' : ''}`, boards && `${boards} board${boards > 1 ? 's' : ''}`].filter(Boolean).join(' and ');
        toast(`Tracking ${what}.`);
        if (!stayOnTab && threads === 0 && boards > 0 && state.tab !== 'boards') switchTab('boards');
        if (!stayOnTab && boards === 0 && threads > 0 && state.tab !== 'threads') switchTab('threads');
      }
      for (const d of dups) {
        toast(`Already tracking ${d.kind === 'thread' ? 'this thread' : 'this board'}.`, d.folder_exists ? {
          action: { label: 'Show folder', run: () => openFolder({ share_path: d.share_path, save_to: d.save_to }) },
        } : {});
      }
      for (const r of added.filter((x) => x.pending && x.remembered_filters)) {
        await askAboutRememberedFilters(r);
      }
      if (invalid.length) {
        const shown = invalid.slice(0, 2).map((r) => `“${r.input.length > 60 ? r.input.slice(0, 57) + '…' : r.input}”`).join(', ');
        toast(`Not a 4chan thread or board link: ${shown}${invalid.length > 2 ? ` and ${invalid.length - 2} more` : ''}.`, { error: true, timeout: 8000 });
      }
      refreshSoon();
      return added.length > 0 || dups.length > 0;
    } catch (e) {
      toast(e.message, { error: true });
      return false;
    }
  }

  $('#add-form').addEventListener('submit', async (e) => {
    e.preventDefault();
    const input = $('#add-input');
    let text = input.value.trim();
    if (!text) {
      const clip = await readClipboard();
      if (clip === null) {
        toast('The browser did not allow reading the clipboard. Paste the links into the field instead.', { error: true });
        input.focus();
        return;
      }
      text = clip.trim();
      if (!text) { toast('The clipboard is empty.'); return; }
    }
    input.value = '';
    await addText(text);
    input.focus();
  });

  $('#pause-button').addEventListener('click', async () => {
    const paused = !state.snapshot.queue.paused;
    try {
      state.snapshot.queue = await api('pause', { method: 'POST', body: { paused } });
      renderChrome();
      toast(paused ? 'Paused. Running downloads finish, nothing new starts.' : 'Resumed.');
    } catch (e) {
      toast(e.message, { error: true });
    }
  });

  function selectedItems() {
    const sel = state.selected[state.tab];
    return (state.snapshot[state.tab] || []).filter((x) => sel.has(x.key));
  }

  // deleteFiles: true deletes without asking (the caller confirmed), undefined asks for several items.
  async function removeItems(list, deleteFiles) {
    if (!list.length) return;
    const tab = state.tab;
    const noun = tab === 'threads' ? 'thread' : 'board';
    if (list.length > 1 && deleteFiles === undefined) {
      const answer = await confirmDialog(`Remove ${list.length} ${noun}s?`,
        tab === 'threads'
          ? 'They stop being checked. Saved files stay where they are unless you delete them too.'
          : 'They stop being checked. Threads they added stay in the thread list.',
        `Remove ${list.length} ${noun}s`,
        tab === 'threads' ? 'Also delete their downloaded files' : null);
      if (!answer) return;
      deleteFiles = answer.checked;
    }
    deleteFiles = !!deleteFiles && tab === 'threads';
    try {
      const res = await api(`${tab}/remove`, { method: 'POST', body: { keys: list.map((x) => x.key), delete_files: deleteFiles } });
      for (const err of (res.errors || [])) toast(err.error, { error: true, timeout: 10000 });
      list.forEach((x) => state.selected[tab].delete(x.key));
      const what = list.length === 1 ? describe(list[0]) : `${list.length} ${noun}s`;
      toast(deleteFiles ? `Removed ${what} and deleted the files.` : `Removed ${what}.`);
      refreshSoon();
    } catch (e) {
      toast(e.message, { error: true });
    }
  }

  async function removeWithFolder(x) {
    const answer = await confirmDialog('Remove thread and delete its files?',
      `“${x.subject}” stops being checked and its folder is deleted with everything in it. This cannot be undone.`,
      'Remove and delete');
    if (answer) removeItems([x], true);
  }

  const describe = (x) => (x.board !== undefined && x.id ? `“${x.subject}”` : `/${x.code}/`);

  $('#remove-selected').addEventListener('click', () => removeItems(selectedItems()));

  $('#clear-all').addEventListener('click', async () => {
    const tab = state.tab;
    const count = (state.snapshot[tab] || []).length;
    if (!count) return;
    const answer = await confirmDialog(`Remove all ${tab}?`,
      tab === 'threads'
        ? `All ${count} threads stop being checked. Saved files stay where they are unless you delete them too.`
        : `All ${count} boards stop being checked. Threads they added stay in the thread list.`,
      `Remove all ${tab}`,
      tab === 'threads' ? 'Also delete their downloaded files' : null);
    if (!answer) return;
    try {
      const res = await api(`${tab}/clear`, { method: 'POST', body: { delete_files: answer.checked } });
      for (const err of ((res && res.errors) || []).slice(0, 3)) toast(err.error, { error: true, timeout: 10000 });
      state.selected[tab].clear();
      toast(answer.checked ? `Removed all ${tab} and deleted their files.` : `Removed all ${tab}.`);
      refreshSoon();
    } catch (e) {
      toast(e.message, { error: true });
    }
  });

  $('#copy-all').addEventListener('click', async () => {
    const urls = (state.snapshot[state.tab] || []).map((x) => x.url);
    if (!urls.length) { toast('Nothing to copy.'); return; }
    // Comma separated, so the list can be pasted back into the link field.
    const ok = await copyText(urls.join(','));
    toast(ok ? `Copied ${urls.length} link${urls.length > 1 ? 's' : ''}.` : 'Copying failed.', { error: !ok });
  });

  function openFolder(x) {
    const share = x.share_path ? x.share_path.replace(/\/*$/, '/') : '';
    try {
      const top = window.top;
      if (share && top.SYNO && top.SYNO.SDS && typeof top.SYNO.SDS.AppLaunch === 'function') {
        top.SYNO.SDS.AppLaunch('SYNO.SDS.App.FileStation3.Instance', { openfile: share });
        return;
      }
    } catch (e) { /* Not inside DSM, fall through. */ }
    if (share) {
      const param = 'openfile=' + encodeURIComponent(share);
      window.open('/?launchApp=SYNO.SDS.App.FileStation3.Instance&launchParam=' + encodeURIComponent(param), '_blank', 'noopener');
      return;
    }
    copyText(x.save_to).then((ok) => toast(ok ? `Folder path copied: ${x.save_to}` : x.save_to));
  }

  function openRoot() {
    const snap = state.snapshot;
    if (snap.save_exists) openFolder({ share_path: snap.save_share_path, save_to: snap.save_path });
  }
  $('#open-root').addEventListener('click', openRoot);

  async function setPriority(list, mode) {
    const keys = list.map((x) => x.key);
    try {
      if (mode === 'none') await api('threads/unprioritize', { method: 'POST', body: { keys } });
      else await api('threads/prioritize', { method: 'POST', body: { keys, first: mode === 'first' } });
      refreshSoon();
    } catch (e) {
      toast(e.message, { error: true });
    }
  }

  function renameThread(t) {
    const dialog = $('#rename-dialog');
    const input = dialog.querySelector('input[name="subject"]');
    input.value = t.loading || t.subject === 'No Subject' ? '' : t.subject;
    dialog.onclose = async () => {
      if (dialog.returnValue !== 'save') return;
      try {
        await api('threads/rename', { method: 'POST', body: { key: t.key, subject: input.value } });
        refreshSoon();
      } catch (e) {
        toast(e.message, { error: true });
      }
    };
    dialog.returnValue = '';
    dialog.showModal();
    input.select();
  }

  // Resolves to false when cancelled, otherwise to { checked } (the optional checkbox's state).
  function confirmDialog(title, text, okLabel, checkboxLabel) {
    const dialog = $('#confirm-dialog');
    $('#confirm-title').textContent = title;
    $('#confirm-text').textContent = text;
    $('#confirm-ok').textContent = okLabel;
    const row = $('#confirm-check-row');
    const box = $('#confirm-check');
    row.hidden = !checkboxLabel;
    box.checked = false;
    $('#confirm-check-label').textContent = checkboxLabel || '';
    dialog.returnValue = '';
    dialog.showModal();
    return new Promise((resolve) => {
      dialog.onclose = () => resolve(dialog.returnValue === 'ok' ? { checked: !row.hidden && box.checked } : false);
    });
  }

  // ---------- Context menu ----------

  let openMenuKey = null;

  function showContextMenu(x, cx, cy) {
    const menu = $('#context-menu');
    openMenuKey = x.key;
    const many = state.selected[state.tab].size > 1;
    const btn = (label, fn, extra = {}) => el('button', { role: 'menuitem', type: 'button', ...extra, onclick: () => { hideMenus(); fn(); } }, label);
    const entries = [];
    if (many) {
      const sel = selectedItems();
      if (state.tab === 'threads') {
        entries.push(btn(`Prioritize ${sel.length} threads`, () => setPriority(sel, 'last')));
        if (sel.some((s) => s.priority)) entries.push(btn('Remove priority', () => setPriority(sel, 'none')));
        entries.push(el('hr'));
      }
      entries.push(btn('Copy links', async () => {
        const ok = await copyText(sel.map((s) => s.url).join(','));
        toast(ok ? `Copied ${sel.length} links.` : 'Copying failed.', { error: !ok });
      }));
      entries.push(el('hr'));
      entries.push(btn(`Remove ${sel.length} ${state.tab}`, () => removeItems(sel), { class: 'danger' }));
    } else {
      entries.push(btn('Show folder in File Station', () => openFolder(x),
        x.folder_exists ? {} : { disabled: true, title: NO_FOLDER_YET }));
      if (state.tab === 'threads') {
        if (!x.priority) entries.push(btn('Prioritize', () => setPriority([x], 'last')));
        if (x.priority !== 1) entries.push(btn('Download first', () => setPriority([x], 'first')));
        if (x.priority) entries.push(btn('Remove priority', () => setPriority([x], 'none')));
      }
      entries.push(btn('Open on 4chan', () => window.open(x.url, '_blank', 'noopener')));
      entries.push(btn('Copy link', async () => {
        const ok = await copyText(x.url);
        toast(ok ? 'Link copied.' : 'Copying failed.', { error: !ok });
      }));
      if (state.tab === 'threads') entries.push(btn('Rename…', () => renameThread(x)));
      if (state.tab === 'boards') {
        entries.push(btn('Filters…', () => editBoardFilters(x)));
        entries.push(btn(`Show filtered threads (${x.filtered || 0})`, () => showFiltered(x), x.filtered ? {} : { disabled: true }));
      }
      if (x.last_error) entries.push(btn('Copy problem details', () => copyText(x.last_error).then(() => toast('Copied.'))));
      entries.push(el('hr'));
      entries.push(btn(state.tab === 'threads' ? 'Remove thread' : 'Remove board', () => removeItems([x], false), { class: 'danger' }));
      if (state.tab === 'threads') {
        entries.push(btn('Remove and delete folder', () => removeWithFolder(x), { class: 'danger' }));
      }
    }
    menu.replaceChildren(...entries);
    // Measure at the top left first, so the size is not affected by a previous position.
    menu.style.left = '0px';
    menu.style.top = '0px';
    menu.hidden = false;
    const r = menu.getBoundingClientRect();
    menu.style.left = Math.max(6, Math.min(cx, window.innerWidth - r.width - 6)) + 'px';
    menu.style.top = Math.max(6, Math.min(cy, window.innerHeight - r.height - 6)) + 'px';
    const first = menu.querySelector('button');
    if (first) first.focus();
  }

  function hideMenus() {
    openMenuKey = null;
    $('#context-menu').hidden = true;
    $('#main-menu').hidden = true;
    $('#menu-button').setAttribute('aria-expanded', 'false');
  }
  document.addEventListener('click', (e) => {
    if (!e.target.closest('.menu') && !e.target.closest('#menu-button')) hideMenus();
  });
  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') hideMenus();
    if (document.querySelector('dialog[open]')) return;
    const typing = e.target.matches('input, textarea, select');
    if (typing) return;
    if (e.key === 'Delete') removeItems(selectedItems());
    if (e.key === 'F2' && state.tab === 'threads') {
      const sel = selectedItems();
      if (sel.length === 1) { e.preventDefault(); renameThread(sel[0]); }
    }
    if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'a') {
      e.preventDefault();
      items().forEach((x) => state.selected[state.tab].add(x.key));
      render();
    }
  });
  window.addEventListener('blur', hideMenus);

  $('#menu-button').addEventListener('click', () => {
    const menu = $('#main-menu');
    menu.hidden = !menu.hidden;
    $('#menu-button').setAttribute('aria-expanded', String(!menu.hidden));
  });
  $('#main-menu').addEventListener('click', (e) => {
    const action = e.target.dataset.action;
    hideMenus();
    if (action === 'open-root') openRoot();
    if (action === 'settings') openSettings();
    if (action === 'columns') openColumns();
    if (action === 'logs') openLogs();
    if (action === 'about') openAbout();
  });

  // ---------- Tabs and filter ----------

  function switchTab(tab) {
    state.tab = tab;
    state.anchor = null;
    render();
  }
  for (const t of document.querySelectorAll('[role="tab"]')) {
    t.addEventListener('click', () => switchTab(t.dataset.tab));
  }
  $('#filter').addEventListener('input', (e) => { state.filter = e.target.value; renderBody(); renderHead(); });

  // ---------- Drag and drop ----------

  const veil = $('#drop-veil');
  let veilTimer = null;
  const hasText = (e) => e.dataTransfer && [...e.dataTransfer.types].some((t) => t === 'text/plain' || t === 'text/uri-list');
  const hideVeil = () => { clearTimeout(veilTimer); veilTimer = null; veil.hidden = true; };
  // Browsers send dragover continuously while dragging over the page; when they stop (drop, Esc, leaving the
  // window or a cancelled drag), the veil goes away on its own.
  const keepVeil = () => {
    veil.hidden = false;
    clearTimeout(veilTimer);
    veilTimer = setTimeout(hideVeil, 300);
  };
  window.addEventListener('dragenter', (e) => { if (hasText(e)) keepVeil(); });
  window.addEventListener('dragover', (e) => {
    if (!hasText(e)) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = 'copy';
    keepVeil();
  });
  window.addEventListener('dragleave', (e) => { if (!e.relatedTarget) hideVeil(); });
  window.addEventListener('dragend', hideVeil);
  window.addEventListener('blur', hideVeil);
  document.addEventListener('keydown', (e) => { if (e.key === 'Escape') hideVeil(); });
  window.addEventListener('drop', (e) => {
    hideVeil();
    if (!hasText(e)) return;
    e.preventDefault();
    const text = e.dataTransfer.getData('text/uri-list') || e.dataTransfer.getData('text/plain');
    // uri-list may contain comment lines.
    const cleaned = text.split(/\r?\n/).filter((l) => !l.startsWith('#')).join('\n');
    if (cleaned.trim()) addText(cleaned);
  });

  // ---------- Settings ----------

  async function openSettings() {
    const dialog = $('#settings-dialog');
    const form = $('#settings-form');
    const errorBox = $('#settings-error');
    errorBox.hidden = true;
    let settings;
    try {
      settings = await api('settings');
    } catch (e) {
      toast(e.message, { error: true });
      return;
    }
    for (const [k, v] of Object.entries(settings)) {
      const input = form.elements[k];
      if (!input) continue;
      if (input.type === 'checkbox') input.checked = !!v;
      else input.value = String(v);
    }
    settingsSkip.set(settings.filters.skip);
    settingsOnly.set(settings.filters.only);
    state.filtersBefore = JSON.stringify(settings.filters) + settings.skip_sticky;
    syncSettingsForm();
    $('#token-new').hidden = true;
    $('#token-secret').textContent = '';
    loadTokens();
    dialog.showModal();
  }

  function syncSettingsForm() {
    const f = $('#settings-form').elements;
    f.save_thumbnails.disabled = !f.save_html.checked;
  }

  // ---------- Tag input ----------

  const foldText = (s) => s.normalize('NFD').replace(/\p{M}/gu, '').toLowerCase().replace(/\s+/g, ' ').trim();

  // A list of words or phrases edited as chips. Enter or a comma adds what is typed; pasting adds every
  // comma or line separated entry; Backspace in the empty field removes the last chip.
  function tagInput(root) {
    let values = [];
    const entry = el('input', { type: 'text', class: 'tag-entry', spellcheck: 'false', placeholder: root.dataset.placeholder || '', 'aria-label': root.previousElementSibling ? root.previousElementSibling.textContent : 'Entries' });
    entry.setAttribute('data-gramm', 'false');
    const bubble = el('div', { class: 'tag-suggest', hidden: true });

    const problem = (text) => {
      if (!/[\p{L}\p{N}]/u.test(text)) return 'Needs a letter or digit';
      if (text.split(' ').some((w) => w.replace(/^\*|\*$/g, '').includes('*'))) return '“*” only at the start or end of a word';
      if (text.length > 100) return 'At most 100 characters';
      return null;
    };
    // Only the chips are replaced; the text field stays where it is, so it keeps the focus.
    const render = () => {
      root.querySelectorAll('.chip').forEach((c) => c.remove());
      values.forEach((v, i) => root.insertBefore(el('span', { class: 'chip', dataset: { i } }, el('span', {}, v),
        el('button', { type: 'button', 'aria-label': `Remove ${v}`, title: 'Remove', onclick: (e) => {
          e.stopPropagation(); values.splice(i, 1); render(); entry.focus();
        } }, '×')), entry));
      entry.placeholder = values.length ? '' : (root.dataset.placeholder || '');
      tools.hidden = values.length === 0;
    };
    const add = (raw) => {
      const text = raw.replace(/\s+/g, ' ').trim();
      if (!text) return true;
      const err = problem(text);
      if (err) { showBubble(err, true); return false; }
      const dup = values.findIndex((v) => foldText(v) === foldText(text));
      if (dup >= 0) {
        const chip = root.querySelector(`.chip[data-i="${dup}"]`);
        if (chip) { chip.classList.remove('flash'); void chip.offsetWidth; chip.classList.add('flash'); }
        return true;
      }
      values.push(text);
      render();
      return true;
    };
    const showBubble = (text, error) => {
      bubble.textContent = text;
      bubble.className = error ? 'tag-suggest error' : 'tag-suggest';
      bubble.style.left = Math.max(0, entry.offsetLeft) + 'px';
      bubble.hidden = false;
    };
    const updateBubble = () => {
      const text = entry.value.trim();
      if (!text) { bubble.hidden = true; return; }
      const err = problem(text.replace(/\s+/g, ' '));
      if (err) showBubble(err, true);
      else showBubble(`Add “${text}”`, false);
    };
    const commit = () => {
      if (add(entry.value)) { entry.value = ''; bubble.hidden = true; }
    };

    entry.addEventListener('input', () => {
      if (entry.value.includes(',')) {
        const parts = entry.value.split(',');
        const rest = parts.pop();
        parts.forEach(add);
        entry.value = rest;
      }
      updateBubble();
    });
    entry.addEventListener('keydown', (e) => {
      if (e.key === 'Enter') { e.preventDefault(); commit(); }
      else if (e.key === 'Escape' && entry.value) { e.preventDefault(); e.stopPropagation(); entry.value = ''; bubble.hidden = true; }
      else if (e.key === 'Backspace' && !entry.value && values.length) { values.pop(); render(); entry.focus(); }
    });
    entry.addEventListener('paste', (e) => {
      const text = e.clipboardData.getData('text');
      if (!/[,\n]/.test(text)) return;
      e.preventDefault();
      text.split(/[,\r\n]+/).forEach(add);
      updateBubble();
    });
    entry.addEventListener('blur', () => setTimeout(() => { if (document.activeElement !== entry) bubble.hidden = true; }, 150));
    bubble.addEventListener('mousedown', (e) => { e.preventDefault(); if (!bubble.classList.contains('error')) commit(); });
    root.addEventListener('click', (e) => { if (e.target === root) entry.focus(); });

    // Copy gives a comma separated list, which pasted into any of these fields adds every entry.
    const tools = el('div', { class: 'tag-tools' },
      el('button', { type: 'button', class: 'quiet', title: 'Copy all entries as a list you can paste into another filter', onclick: async () => {
        const ok = await copyText(values.join(', '));
        toast(ok ? `Copied ${values.length} entr${values.length === 1 ? 'y' : 'ies'}.` : 'Copying failed.', { error: !ok });
      } }, 'Copy'),
      el('button', { type: 'button', class: 'quiet danger', onclick: async () => {
        const answer = await confirmDialog('Clear this list?', `All ${values.length} entries are removed from the list. Nothing is saved until you save.`, 'Clear list');
        if (answer) { values = []; render(); }
        entry.focus();
      } }, 'Clear'));
    root.after(tools);
    root.append(entry, bubble);

    render();
    return {
      // What is still typed but not added counts too, so it is not lost on Save.
      get: () => { if (entry.value.trim()) commit(); return [...values]; },
      set: (list) => { values = [...(list || [])]; entry.value = ''; bubble.hidden = true; render(); },
    };
  }

  const settingsSkip = tagInput($('#settings-skip'));
  const settingsOnly = tagInput($('#settings-only'));
  const boardSkip = tagInput($('#board-skip'));
  const boardOnly = tagInput($('#board-only'));

  // ---------- Board filters ----------

  const hasOwnFilters = (b) => !!(b.filters && ((b.filters.skip || []).length || (b.filters.only || []).length));

  function editBoardFilters(b) {
    const dialog = $('#board-filters-dialog');
    $('#board-filters-title').textContent = `Filters for /${b.code}/`;
    boardSkip.set(b.filters && b.filters.skip);
    boardOnly.set(b.filters && b.filters.only);
    $('#board-filters-error').hidden = true;
    dialog.dataset.key = b.key;
    dialog.showModal();
  }

  $('#board-filters-form').addEventListener('submit', async (e) => {
    if (e.submitter && e.submitter.value === 'cancel') return;
    e.preventDefault();
    const dialog = $('#board-filters-dialog');
    const key = dialog.dataset.key;
    try {
      await api('boards/filters', { method: 'POST', body: { key, skip: boardSkip.get(), only: boardOnly.get() } });
      dialog.close('save');
      toast('Board filters saved.');
      refreshSoon();
      offerFilterCleanup(key);
    } catch (err) {
      const box = $('#board-filters-error');
      box.textContent = err.message;
      box.hidden = false;
    }
  });

  async function showFiltered(b) {
    const dialog = $('#filtered-dialog');
    const list = $('#filtered-list');
    $('#filtered-title').textContent = `Filtered threads on /${b.code}/`;
    list.replaceChildren(el('p', { class: 'hint' }, 'Loading…'));
    dialog.showModal();
    try {
      const { threads } = await api('boards/filtered', { query: { key: b.key } });
      if (!threads.length) {
        list.replaceChildren(el('p', { class: 'hint' }, 'No threads on the board are filtered right now.'));
        return;
      }
      list.replaceChildren(...threads.map((t) => {
        const row = el('div', { class: 'filtered-row' },
          el('span', { class: 'subject', title: t.subject }, t.subject),
          el('span', { class: 'reason' }, `#${t.id} · ${t.reason}`),
          el('span', { class: 'row-actions' },
            el('a', { href: t.url, target: '_blank', rel: 'noopener' }, 'Open'),
            el('button', { type: 'button', onclick: async (e) => {
              e.currentTarget.disabled = true;
              const ok = await addText(t.url, { stayOnTab: true });
              if (ok) row.remove();
              if (!list.querySelector('.filtered-row')) list.replaceChildren(el('p', { class: 'hint' }, 'No threads on the board are filtered right now.'));
            } }, 'Add anyway')));
        return row;
      }));
    } catch (err) {
      list.replaceChildren(el('p', { class: 'form-error' }, err.message));
    }
  }

  // After filters change, offer to remove tracked threads (added by boards) that the new filters keep out.
  async function offerFilterCleanup(boardKey) {
    let threads;
    try {
      ({ threads } = await api('filters/matches', { query: boardKey ? { board: boardKey } : {} }));
    } catch (e) {
      return;
    }
    if (!threads.length) return;
    const shown = threads.slice(0, 6).map((t) => `“${t.subject}”`).join(', ');
    const more = threads.length > 6 ? ` and ${threads.length - 6} more` : '';
    const n = threads.length;
    const answer = await confirmDialog(
      `${n} tracked thread${n > 1 ? 's match' : ' matches'} the new filters`,
      `${shown}${more} ${n > 1 ? 'were' : 'was'} added by a board and would be filtered now. Remove ${n > 1 ? 'them' : 'it'}? Threads you added yourself are never removed by filters.`,
      `Remove ${n} thread${n > 1 ? 's' : ''}`,
      'Also delete their downloaded files');
    if (!answer) return;
    try {
      const res = await api('threads/remove', { method: 'POST', body: { keys: threads.map((t) => t.key), delete_files: answer.checked } });
      for (const err of (res.errors || []).slice(0, 3)) toast(err.error, { error: true, timeout: 10000 });
      toast(`Removed ${n} thread${n > 1 ? 's' : ''}.`);
      refreshSoon();
    } catch (e) {
      toast(e.message, { error: true });
    }
  }

  // A board added back had filters before; it waits until the user keeps or drops them.
  function askAboutRememberedFilters(r) {
    const dialog = $('#remembered-dialog');
    const f = r.remembered_filters;
    const board = r.key.split('/').pop();
    $('#remembered-title').textContent = `Filters for /${board}/ from before`;
    $('#remembered-text').textContent = `When you tracked /${board}/ before, it had these filters. They are applied; checking the board starts once you decide.`;
    const chips = (list) => (list || []).map((v) => el('span', { class: 'chip' }, el('span', {}, v)));
    $('#remembered-skip').replaceChildren(...chips(f.skip));
    $('#remembered-only').replaceChildren(...chips(f.only));
    $('#remembered-skip-row').hidden = !(f.skip || []).length;
    $('#remembered-only-row').hidden = !(f.only || []).length;
    dialog.returnValue = '';
    dialog.showModal();
    return new Promise((resolve) => {
      dialog.onclose = async () => {
        // Closing without a choice keeps the filters, like the timeout does.
        const useFilters = dialog.returnValue !== 'drop';
        try {
          await api('boards/confirm', { method: 'POST', body: { key: r.key, use_filters: useFilters } });
          toast(useFilters ? `/${board}/ started with its earlier filters.` : `/${board}/ started without filters.`);
        } catch (e) {
          toast(e.message, { error: true });
        }
        refreshSoon();
        resolve();
      };
    });
  }

  // ---------- Browser extension tokens ----------

  // The address the extension uses is the one DSM is open on.
  function dsmOrigin() {
    try { return window.top.location.origin; } catch (e) { return window.location.origin; }
  }

  async function loadTokens() {
    $('#ext-server').textContent = dsmOrigin();
    const list = $('#token-list');
    try {
      const { tokens } = await api('tokens');
      list.replaceChildren(...(tokens.length ? tokens.map((t) => el('div', { class: 'token-row' },
        el('span', { class: 'name' }, t.name),
        el('span', { class: 'meta' }, `created ${absTime(t.created)}` + (t.last_used ? `, last used ${relTime(t.last_used)}` : ', never used')),
        el('button', { type: 'button', class: 'quiet danger', onclick: async () => {
          const answer = await confirmDialog(`Revoke “${t.name}”?`, 'Extensions using this token can no longer add links.', 'Revoke token');
          if (!answer) return;
          try { await api('tokens/revoke', { method: 'POST', body: { id: t.id } }); } catch (e) { toast(e.message, { error: true }); }
          loadTokens();
        } }, 'Revoke'))) : [el('p', { class: 'hint' }, 'No tokens yet.')]));
    } catch (e) {
      list.replaceChildren(el('p', { class: 'form-error' }, e.message));
    }
  }

  $('#ext-server-copy').addEventListener('click', async () => {
    const ok = await copyText(dsmOrigin());
    toast(ok ? 'Address copied.' : 'Copying failed.', { error: !ok });
  });
  $('#token-create').addEventListener('click', async () => {
    try {
      const { secret } = await api('tokens/create', { method: 'POST', body: { name: $('#token-name').value } });
      $('#token-secret').textContent = secret;
      $('#token-new').hidden = false;
      $('#token-name').value = '';
      loadTokens();
    } catch (e) {
      toast(e.message, { error: true });
    }
  });
  // Enter in the name field creates the token instead of submitting the settings form.
  $('#token-name').addEventListener('keydown', (e) => {
    if (e.key === 'Enter') { e.preventDefault(); $('#token-create').click(); }
  });
  $('#token-copy').addEventListener('click', async () => {
    const ok = await copyText($('#token-secret').textContent);
    toast(ok ? 'Token copied.' : 'Copying failed.', { error: !ok });
  });

  // ---------- Theme ----------

  function applyTheme(theme) {
    const t = theme === 'dark' ? 'dark' : 'light';
    document.documentElement.dataset.theme = t;
    saveJSON('theme', { value: t });
  }
  // The last used theme right away, to avoid a flash; the saved setting once it has loaded.
  applyTheme(loadJSON('theme', { value: 'light' }).value);
  api('settings').then((s) => applyTheme(s.theme)).catch(() => {});
  $('#settings-form').addEventListener('change', syncSettingsForm);

  $('#settings-form').addEventListener('submit', async (e) => {
    if (e.submitter && e.submitter.value === 'cancel') return;
    e.preventDefault();
    const f = e.target.elements;
    const body = {
      save_path: f.save_path.value.trim(),
      image_filename_format: Number(f.image_filename_format.value),
      folder_name: Number(f.folder_name.value),
      theme: f.theme.value,
      save_html: f.save_html.checked,
      save_thumbnails: f.save_thumbnails.checked,
      min_seconds_between_scrapes: Number(f.min_seconds_between_scrapes.value),
      max_concurrent_downloads: Number(f.max_concurrent_downloads.value),
      space_downloads: f.space_downloads.checked,
      user_agent: f.user_agent.value.trim(),
      keep_lists: f.keep_lists.checked,
      debug_logging: f.debug_logging.checked,
      filters: { skip: settingsSkip.get(), only: settingsOnly.get() },
      skip_sticky: f.skip_sticky.checked,
      start_paused: f.start_paused.checked,
    };
    const save = $('#settings-save');
    save.disabled = true;
    try {
      const saved = await api('settings', { method: 'POST', body });
      applyTheme(saved.theme);
      $('#settings-dialog').close('save');
      toast('Settings saved.');
      refreshSoon();
      if (JSON.stringify(saved.filters) + saved.skip_sticky !== state.filtersBefore) offerFilterCleanup('');
    } catch (err) {
      const box = $('#settings-error');
      box.textContent = err.message;
      box.hidden = false;
    } finally {
      save.disabled = false;
    }
  });

  // ---------- Columns ----------

  function openColumns() {
    const list = $('#columns-list');
    const tab = state.tab;
    list.replaceChildren(el('p', { class: 'muted' }, `Columns shown in the ${tab} list.`),
      ...COLUMNS[tab].filter((c) => !c.fixed).map((c) => {
        const box = el('input', { type: 'checkbox', onchange: (e) => {
          const hidden = new Set(state.hidden[tab]);
          if (e.target.checked) hidden.delete(c.key); else hidden.add(c.key);
          state.hidden[tab] = [...hidden];
          saveJSON('hiddenColumns', state.hidden);
          render();
        } });
        box.checked = !state.hidden[tab].includes(c.key);
        return el('label', { class: 'check' }, box, c.label);
      }));
    $('#columns-dialog').showModal();
  }

  // ---------- Log and about ----------

  const PROBLEM_LINE = /level=(WARN|ERROR)/;

  async function loadLogs() {
    const pre = $('#log-text');
    const problemsOnly = $('#log-level').value === 'problems';
    try {
      let { lines } = await api('logs', { query: { lines: problemsOnly ? 5000 : 400 } });
      if (problemsOnly) lines = lines.filter((l) => PROBLEM_LINE.test(l));
      pre.textContent = lines.length ? lines.join('\n') : (problemsOnly ? 'No warnings or errors.' : 'The log is empty.');
      pre.scrollTop = pre.scrollHeight;
    } catch (e) {
      pre.textContent = e.message;
    }
  }
  function openLogs() {
    $('#log-text').textContent = 'Loading…';
    $('#logs-dialog').showModal();
    loadLogs();
  }
  $('#log-refresh').addEventListener('click', loadLogs);
  $('#log-level').addEventListener('change', loadLogs);

  async function fullLog() {
    const { lines } = await api('logs', { query: { lines: 5000 } });
    return lines.join('\n') + '\n';
  }
  $('#log-copy').addEventListener('click', async () => {
    try {
      const ok = await copyText(await fullLog());
      toast(ok ? 'Log copied.' : 'Copying failed.', { error: !ok });
    } catch (e) {
      toast(e.message, { error: true });
    }
  });
  $('#log-download').addEventListener('click', async () => {
    try {
      const blob = new Blob([await fullLog()], { type: 'text/plain' });
      const a = el('a', { href: URL.createObjectURL(blob), download: `synochan-${new Date().toISOString().slice(0, 10)}.log` });
      document.body.append(a);
      a.click();
      a.remove();
      setTimeout(() => URL.revokeObjectURL(a.href), 10000);
    } catch (e) {
      toast(e.message, { error: true });
    }
  });

  async function openAbout() {
    const facts = $('#about-facts');
    facts.replaceChildren();
    $('#about-dialog').showModal();
    try {
      const info = await api('info');
      $('#about-version').textContent = info.author ? `Version ${info.version} · by ${info.author}` : `Version ${info.version}`;
      if (/^https:\/\//.test(info.repo_url || '')) {
        $('#about-repo').href = info.repo_url;
        const body = `**SynoChan version:** ${info.version}\n**Platform:** ${info.arch}\n\n**What happened:**\n\n\n**What you expected:**\n\n\n**Steps to reproduce:**\n\n`;
        $('#about-issues').href = info.repo_url + '/issues/new?body=' + encodeURIComponent(body);
        $('#about-releases').href = info.repo_url + '/releases';
        $('#about-links').hidden = false;
      }
      const rows = [['Signed in as', info.user], ['Running since', absTime(info.started)], ['Data folder', info.data_dir], ['Platform', `${info.arch}, ${info.go_version}`]];
      facts.replaceChildren(...rows.flatMap(([k, v]) => [el('dt', {}, k), el('dd', {}, v || '–')]));
    } catch (e) {
      facts.replaceChildren(el('dd', {}, e.message));
    }
  }

  // ---------- DSM window ----------

  // Writing assistant extensions (Grammarly) attach to text fields and insert their own elements into the DSM
  // window around this frame, which breaks DSM's window sizing. Opt every field out.
  for (const field of document.querySelectorAll('input[type="text"], input[type="search"], input[type="number"], textarea')) {
    field.setAttribute('data-gramm', 'false');
    field.setAttribute('data-gramm_editor', 'false');
    field.setAttribute('data-enable-grammarly', 'false');
  }

  // DSM measures a window's border from the first child of its frame wrapper and expects its own border element
  // there. If an extension (Grammarly) inserts an element in front of it, maximize and resize fail for this window
  // ("Cannot read properties of null (reading 'getFrameWidth')"). Such elements are moved behind DSM's.
  // DSM's own element is never moved: it contains this frame, and moving a frame's ancestor reloads the frame.
  (function keepWindowFrameOrder() {
    let frame;
    try {
      frame = window.frameElement;
    } catch (e) {
      return;
    }
    const bwrap = frame && frame.closest ? frame.closest('.x-window-bwrap') : null;
    if (!bwrap || typeof MutationObserver !== 'function') return;

    const isDSM = (n) => n.nodeType !== 1 || n.contains(frame) || Array.from(n.classList).some((c) => c.startsWith('x-'));
    let moved = 0;
    const fix = () => {
      const own = Array.from(bwrap.children).find((n) => n.classList.contains('x-window-ml'));
      if (!own || bwrap.firstChild === own) return;
      for (let n = bwrap.firstChild; n && n !== own; ) {
        const next = n.nextSibling;
        if (n.nodeType === 3 && !n.textContent.trim()) {
          n.remove(); // Whitespace in front would be the first child too.
        } else if (!isDSM(n)) {
          if (++moved > 50) return; // Something keeps putting it back; stop rather than fight.
          bwrap.appendChild(n);
        }
        n = next;
      }
    };
    fix();
    const observer = new MutationObserver(fix);
    observer.observe(bwrap, { childList: true });
    window.addEventListener('pagehide', () => observer.disconnect());
  })();

  // An iframe swallows mouse events. When a DSM window is dragged or resized across it, DSM never sees the mouse
  // move or the button release and the drag gets stuck, which blocks moving and resizing every window. So while
  // a mouse button is held down anywhere in DSM itself, this frame ignores the mouse. Clicks inside the frame
  // never reach DSM's document and are unaffected. Installed from here rather than from the DSM side, because
  // this file is always loaded fresh.
  (function installDragGuard() {
    let frame, parentDoc, parentWin;
    try {
      frame = window.frameElement;
      parentWin = window.parent;
      parentDoc = parentWin.document;
    } catch (e) {
      return;
    }
    if (!frame || !parentDoc || parentWin === window) return;

    let disabled = false;
    const setEnabled = (enabled) => {
      if (disabled === !enabled) return;
      disabled = !enabled;
      frame.style.pointerEvents = enabled ? '' : 'none';
    };
    const disable = () => setEnabled(false);
    const enable = () => setEnabled(true);
    // A button released outside the browser sends no mouseup; the next move without buttons restores the frame.
    const onMove = (e) => { if (disabled && e.buttons === 0) enable(); };

    const docEvents = [['mousedown', disable], ['touchstart', disable], ['mouseup', enable],
      ['touchend', enable], ['touchcancel', enable], ['mousemove', onMove]];
    docEvents.forEach(([type, fn]) => parentDoc.addEventListener(type, fn, true));
    parentWin.addEventListener('blur', enable);

    // Remove the listeners when this page goes away (window closed or reloaded), so none pile up in DSM.
    window.addEventListener('pagehide', () => {
      docEvents.forEach(([type, fn]) => parentDoc.removeEventListener(type, fn, true));
      parentWin.removeEventListener('blur', enable);
      frame.style.pointerEvents = '';
    });
  })();

  // A click inside the iframe does not reach DSM, so bring the DSM window to the front ourselves.
  document.addEventListener('mousedown', () => {
    try {
      const win = window.parent.SYNO.SDS.SynoChan.Utils.ApplicationWindow;
      if (win && typeof win.toFront === 'function') win.toFront();
    } catch (e) { /* Not inside a DSM window. */ }
  }, true);

  // ---------- Start ----------

  render();
  poll();
  $('#add-input').focus();
})();
