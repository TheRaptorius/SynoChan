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

  const ICONS = {
    folder: '<svg viewBox="0 0 20 20" aria-hidden="true"><path d="M2 5.5A1.5 1.5 0 0 1 3.5 4h4l1.6 1.8h7.4A1.5 1.5 0 0 1 18 7.3v7.2a1.5 1.5 0 0 1-1.5 1.5h-13A1.5 1.5 0 0 1 2 14.5z"/></svg>',
    more: '<svg viewBox="0 0 20 20" aria-hidden="true"><circle cx="4" cy="10" r="1.6"/><circle cx="10" cy="10" r="1.6"/><circle cx="16" cy="10" r="1.6"/></svg>',
  };

  // ---------- API ----------

  function synoToken() {
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
      { key: 'code', label: 'Board', sort: (b) => b.code, cell: (b) => el('td', { class: 'board' }, `/${b.code}/`), fixed: true },
      { key: 'thread_count', label: 'Threads on board', num: true, sort: (b) => b.thread_count, cell: (b) => el('td', { class: 'num' }, b.thread_count) },
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
    if (state.snapshot.queue.paused) return 'paused';
    if (!item.next_check) return 'soon';
    return Date.parse(item.next_check) <= Date.now() ? 'waiting in queue' : relTime(item.next_check);
  }

  function subjectCell(t) {
    const text = el('span', { class: t.loading ? 'text loading' : 'text', title: t.subject }, t.subject);
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
    return [...list].sort((a, b) => {
      const x = col.sort(a), y = col.sort(b);
      return (x < y ? -1 : x > y ? 1 : 0) * dir;
    });
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
        class: [sel.has(x.key) ? 'selected' : '', x.last_error ? 'has-error' : ''].join(' ').trim(),
        dataset: { key: x.key },
        onclick: (e) => toggleSelect(x.key, { additive: e.ctrlKey || e.metaKey, range: e.shiftKey }),
        ondblclick: () => openFolder(x),
        oncontextmenu: (e) => {
          e.preventDefault();
          if (!sel.has(x.key)) { sel.clear(); sel.add(x.key); render(); }
          showContextMenu(x, e.clientX, e.clientY);
        },
      }, el('td', { class: 'sel' }, checkbox));
      for (const c of cols) tr.append(c.cell(x));
      const folderBtn = el('button', { type: 'button', title: 'Show folder in File Station', onclick: (e) => { e.stopPropagation(); openFolder(x); } });
      folderBtn.innerHTML = ICONS.folder;
      const moreBtn = el('button', { type: 'button', title: 'More actions', 'aria-haspopup': 'menu', onclick: (e) => {
        e.stopPropagation();
        if (!sel.has(x.key)) { sel.clear(); sel.add(x.key); render(); }
        const r = e.currentTarget.getBoundingClientRect();
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
          : 'Paste a board link (like https://boards.4chan.org/wg/) above. Every new thread on the board is tracked automatically.');
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
      state.connectionError = e.status === 401
        ? 'Your DSM session has expired. Sign in to DSM again and reopen SynoChan.'
        : e.message;
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

  async function addText(text) {
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
        if (threads === 0 && boards > 0 && state.tab !== 'boards') switchTab('boards');
        if (boards === 0 && threads > 0 && state.tab !== 'threads') switchTab('threads');
      }
      for (const d of dups) {
        toast(`Already tracking ${d.kind === 'thread' ? 'this thread' : 'this board'}.`, {
          action: { label: 'Show folder', run: () => openFolder({ share_path: d.share_path, save_to: d.save_to }) },
        });
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

  async function removeItems(list) {
    if (!list.length) return;
    const tab = state.tab;
    const noun = tab === 'threads' ? 'thread' : 'board';
    if (list.length > 1) {
      const ok = await confirmDialog(`Remove ${list.length} ${noun}s?`,
        tab === 'threads'
          ? 'They stop being checked. Files already saved stay where they are.'
          : 'They stop being checked. Threads they added stay in the thread list.',
        `Remove ${list.length} ${noun}s`);
      if (!ok) return;
    }
    try {
      const res = await api(`${tab}/remove`, { method: 'POST', body: { keys: list.map((x) => x.key) } });
      for (const err of (res.errors || [])) toast(err.error, { error: true, timeout: 10000 });
      list.forEach((x) => state.selected[tab].delete(x.key));
      toast(list.length === 1 ? `Removed ${describe(list[0])}.` : `Removed ${list.length} ${noun}s.`);
      refreshSoon();
    } catch (e) {
      toast(e.message, { error: true });
    }
  }

  const describe = (x) => (x.board !== undefined && x.id ? `“${x.subject}”` : `/${x.code}/`);

  $('#remove-selected').addEventListener('click', () => removeItems(selectedItems()));

  $('#clear-all').addEventListener('click', async () => {
    const tab = state.tab;
    const count = (state.snapshot[tab] || []).length;
    if (!count) return;
    const ok = await confirmDialog(`Remove all ${tab}?`,
      tab === 'threads'
        ? `All ${count} threads stop being checked. Files already saved stay where they are.`
        : `All ${count} boards stop being checked. Threads they added stay in the thread list.`,
      `Remove all ${tab}`);
    if (!ok) return;
    try {
      const res = await api(`${tab}/clear`, { method: 'POST' });
      for (const err of (res.errors || []).slice(0, 3)) toast(err.error, { error: true, timeout: 10000 });
      state.selected[tab].clear();
      toast(`Removed all ${tab}.`);
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
    const share = x.share_path;
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

  function confirmDialog(title, text, okLabel) {
    const dialog = $('#confirm-dialog');
    $('#confirm-title').textContent = title;
    $('#confirm-text').textContent = text;
    $('#confirm-ok').textContent = okLabel;
    dialog.returnValue = '';
    dialog.showModal();
    return new Promise((resolve) => {
      dialog.onclose = () => resolve(dialog.returnValue === 'ok');
    });
  }

  // ---------- Context menu ----------

  function showContextMenu(x, cx, cy) {
    const menu = $('#context-menu');
    const many = state.selected[state.tab].size > 1;
    const btn = (label, fn, extra = {}) => el('button', { role: 'menuitem', type: 'button', ...extra, onclick: () => { hideMenus(); fn(); } }, label);
    const entries = [];
    if (many) {
      const sel = selectedItems();
      entries.push(btn('Copy links', async () => {
        const ok = await copyText(sel.map((s) => s.url).join(','));
        toast(ok ? `Copied ${sel.length} links.` : 'Copying failed.', { error: !ok });
      }));
      entries.push(el('hr'));
      entries.push(btn(`Remove ${sel.length} ${state.tab}`, () => removeItems(sel), { class: 'danger' }));
    } else {
      entries.push(btn('Show folder in File Station', () => openFolder(x)));
      entries.push(btn('Open on 4chan', () => window.open(x.url, '_blank', 'noopener')));
      entries.push(btn('Copy link', async () => {
        const ok = await copyText(x.url);
        toast(ok ? 'Link copied.' : 'Copying failed.', { error: !ok });
      }));
      if (state.tab === 'threads') entries.push(btn('Rename…', () => renameThread(x)));
      if (x.last_error) entries.push(btn('Copy problem details', () => copyText(x.last_error).then(() => toast('Copied.'))));
      entries.push(el('hr'));
      entries.push(btn(state.tab === 'threads' ? 'Remove thread' : 'Remove board', () => removeItems([x]), { class: 'danger' }));
    }
    menu.replaceChildren(...entries);
    menu.hidden = false;
    const r = menu.getBoundingClientRect();
    menu.style.left = Math.min(cx, window.innerWidth - r.width - 6) + 'px';
    menu.style.top = Math.min(cy, window.innerHeight - r.height - 6) + 'px';
    const first = menu.querySelector('button');
    if (first) first.focus();
  }

  function hideMenus() {
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

  let dragDepth = 0;
  const veil = $('#drop-veil');
  const hasText = (e) => [...e.dataTransfer.types].some((t) => t === 'text/plain' || t === 'text/uri-list');
  window.addEventListener('dragenter', (e) => { if (hasText(e)) { dragDepth++; veil.hidden = false; } });
  window.addEventListener('dragleave', () => { dragDepth = Math.max(0, dragDepth - 1); if (!dragDepth) veil.hidden = true; });
  window.addEventListener('dragover', (e) => { if (hasText(e)) { e.preventDefault(); e.dataTransfer.dropEffect = 'copy'; } });
  window.addEventListener('drop', (e) => {
    dragDepth = 0;
    veil.hidden = true;
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
    syncSettingsForm();
    dialog.showModal();
  }

  function syncSettingsForm() {
    const f = $('#settings-form').elements;
    f.save_thumbnails.disabled = !f.save_html.checked;
    f.thread_folder_name_format.disabled = !f.add_thread_subject_to_folder.checked;
  }
  $('#settings-form').addEventListener('change', syncSettingsForm);

  $('#settings-form').addEventListener('submit', async (e) => {
    if (e.submitter && e.submitter.value === 'cancel') return;
    e.preventDefault();
    const f = e.target.elements;
    const body = {
      save_path: f.save_path.value.trim(),
      image_filename_format: Number(f.image_filename_format.value),
      thread_folder_name_format: Number(f.thread_folder_name_format.value),
      add_thread_subject_to_folder: f.add_thread_subject_to_folder.checked,
      save_html: f.save_html.checked,
      save_thumbnails: f.save_thumbnails.checked,
      min_seconds_between_scrapes: Number(f.min_seconds_between_scrapes.value),
      max_concurrent_downloads: Number(f.max_concurrent_downloads.value),
      max_1_request_per_second: f.max_1_request_per_second.checked,
      user_agent: f.user_agent.value.trim(),
      keep_lists: f.keep_lists.checked,
      start_paused: f.start_paused.checked,
    };
    const save = $('#settings-save');
    save.disabled = true;
    try {
      await api('settings', { method: 'POST', body });
      $('#settings-dialog').close('save');
      toast('Settings saved.');
      refreshSoon();
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

  async function loadLogs() {
    const pre = $('#log-text');
    try {
      const { lines } = await api('logs', { query: { lines: 400 } });
      pre.textContent = lines.length ? lines.join('\n') : 'The log is empty.';
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

  async function openAbout() {
    const facts = $('#about-facts');
    facts.replaceChildren();
    $('#about-dialog').showModal();
    try {
      const info = await api('info');
      $('#about-version').textContent = info.author ? `Version ${info.version} · by ${info.author}` : `Version ${info.version}`;
      if (/^https:\/\//.test(info.repo_url || '')) {
        $('#about-repo').href = info.repo_url;
        $('#about-issues').href = info.repo_url + '/issues';
        $('#about-releases').href = info.repo_url + '/releases';
        $('#about-links').hidden = false;
      }
      const rows = [['Signed in as', info.user], ['Running since', absTime(info.started)], ['Data folder', info.data_dir], ['Platform', `${info.arch}, ${info.go_version}`]];
      facts.replaceChildren(...rows.flatMap(([k, v]) => [el('dt', {}, k), el('dd', {}, v || '–')]));
    } catch (e) {
      facts.replaceChildren(el('dd', {}, e.message));
    }
  }

  // ---------- Start ----------

  render();
  poll();
  $('#add-input').focus();
})();
