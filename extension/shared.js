// Shared by the background worker, the popup and the options page.
'use strict';

const SynoChan = (() => {
  const THREAD_RE = /^https?:\/\/boards\.(?:4chan|4channel)\.org\/([a-zA-Z0-9]+)\/thread\/(\d+)/;
  const BOARD_RE = /^https?:\/\/boards\.(?:4chan|4channel)\.org\/([a-zA-Z0-9]+)\/*(?:catalog\/*)?(?:[?#].*)?$/;
  const QUEUE_MAX_AGE_MS = 7 * 24 * 3600 * 1000;
  const FLUSH_ALARM = 'synochan-flush';
  // An unreachable NAS (no VPN) would otherwise keep a request open until the network gives up.
  const REQUEST_TIMEOUT_MS = 8000;
  const RETRY_CHOICES = [1, 2, 5, 10, 30];

  async function getRetryMinutes() {
    const { retryMinutes = 1 } = await chrome.storage.local.get('retryMinutes');
    return RETRY_CHOICES.includes(retryMinutes) ? retryMinutes : 1;
  }

  // Describes a URL: { kind: 'thread' | 'board', board, id, label } or null for anything else.
  function classify(url) {
    if (!url) return null;
    const t = THREAD_RE.exec(url);
    if (t) return { kind: 'thread', board: t[1], id: t[2], label: `Thread /${t[1]}/${t[2]}` };
    const b = BOARD_RE.exec(url);
    if (b) return { kind: 'board', board: b[1], label: `Board /${b[1]}/` };
    return null;
  }

  // "https://nas.local:5001/anything" becomes "https://nas.local:5001".
  function normaliseServer(input) {
    let s = (input || '').trim();
    if (!s) return '';
    if (!/^https?:\/\//i.test(s)) s = 'https://' + s;
    const u = new URL(s);
    return u.origin;
  }

  async function getConfig() {
    const { server = '', token = '' } = await chrome.storage.local.get(['server', 'token']);
    return { server, token };
  }

  function apiURL(server, path) {
    return `${server}/webman/3rdparty/synochan/app/api.cgi?path=${encodeURIComponent(path)}`;
  }

  // Calls the extension API. Throws an Error with .kind: 'config' (not set up), 'auth' (bad token),
  // 'offline' (NAS or SynoChan not reachable; worth retrying later) or 'server' (anything else).
  async function call(path, { method = 'GET', body } = {}) {
    const { server, token } = await getConfig();
    if (!server || !token) throw Object.assign(new Error('SynoChan is not set up yet. Open the extension settings.'), { kind: 'config' });
    let res;
    try {
      res = await fetch(apiURL(server, path), {
        method,
        headers: { 'X-SynoChan-Token': token, ...(body ? { 'Content-Type': 'application/json' } : {}) },
        body: body ? JSON.stringify(body) : undefined,
        credentials: 'omit',
        cache: 'no-store',
        signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS),
      });
    } catch (e) {
      throw Object.assign(new Error(`Cannot reach ${server}. Is the NAS reachable from here (VPN?) and is its certificate trusted?`), { kind: 'offline' });
    }
    let data = null;
    try { data = await res.json(); } catch (e) { /* Not JSON. */ }
    if (res.status === 401) throw Object.assign(new Error((data && data.error) || 'The API token was rejected.'), { kind: 'auth' });
    if (res.status === 503 || res.status === 502 || res.status === 504) {
      throw Object.assign(new Error((data && data.error) || 'SynoChan is not running on the NAS.'), { kind: 'offline' });
    }
    if (res.status === 404 && !data) {
      throw Object.assign(new Error('SynoChan was not found on this server. Check the address and that the package is installed.'), { kind: 'server' });
    }
    if (!res.ok) throw Object.assign(new Error((data && data.error) || `Request failed (HTTP ${res.status})`), { kind: 'server' });
    return data;
  }

  async function ping() {
    return call('ext/ping');
  }

  // ---- Queue of links that could not be sent ----

  async function getQueue() {
    const { queue = [] } = await chrome.storage.local.get('queue');
    const now = Date.now();
    return queue.filter((q) => now - q.added < QUEUE_MAX_AGE_MS);
  }

  async function setQueue(queue) {
    await chrome.storage.local.set({ queue });
    await chrome.action.setBadgeBackgroundColor({ color: '#3d8a2b' });
    await chrome.action.setBadgeText({ text: queue.length ? String(queue.length) : '' });
    if (queue.length) {
      const minutes = await getRetryMinutes();
      const existing = await chrome.alarms.get(FLUSH_ALARM);
      // Keep a running schedule; only (re)create it when missing or when the interval changed.
      if (!existing || existing.periodInMinutes !== minutes) {
        chrome.alarms.create(FLUSH_ALARM, { periodInMinutes: minutes, delayInMinutes: minutes });
      }
    } else {
      chrome.alarms.clear(FLUSH_ALARM);
    }
  }

  // When the next automatic attempt is due (a Date), or null when nothing waits.
  async function nextRetry() {
    const alarm = await chrome.alarms.get(FLUSH_ALARM);
    return alarm ? new Date(alarm.scheduledTime) : null;
  }

  async function setRetryMinutes(minutes) {
    await chrome.storage.local.set({ retryMinutes: minutes });
    await chrome.alarms.clear(FLUSH_ALARM);
    await setQueue(await getQueue()); // Reschedules with the new interval if anything waits.
  }

  async function enqueue(urls) {
    const queue = await getQueue();
    const known = new Set(queue.map((q) => q.url));
    for (const url of urls) if (!known.has(url)) queue.push({ url, added: Date.now() });
    await setQueue(queue);
    return queue.length;
  }

  function summarise(results) {
    const count = (s) => results.filter((r) => r.status === s).length;
    return {
      added: count('added'), duplicate: count('duplicate'), invalid: count('invalid'),
      remembered: results.filter((r) => r.remembered_filters).length,
    };
  }

  // Sends links. When the NAS is unreachable they are kept and sent later; returns { queued: n } then.
  async function addUrls(urls) {
    urls = [...new Set(urls.map((u) => u.trim()).filter(Boolean))];
    if (!urls.length) return { added: 0, duplicate: 0, invalid: 0 };
    try {
      const { results } = await call('ext/add', { method: 'POST', body: { urls } });
      flush().catch(() => {}); // Connection works again: send what was waiting.
      return summarise(results);
    } catch (e) {
      if (e.kind === 'offline') return { queued: await enqueue(urls), error: e.message };
      throw e;
    }
  }

  // Sends waiting links. Returns how many were sent.
  async function flush() {
    const queue = await getQueue();
    if (!queue.length) {
      await setQueue([]);
      return 0;
    }
    try {
      await call('ext/add', { method: 'POST', body: { urls: queue.map((q) => q.url) } });
    } catch (e) {
      if (e.kind === 'offline') {
        await setQueue(queue);
        return 0;
      }
      throw e;
    }
    // Links queued while sending stay for the next round.
    const sent = new Set(queue.map((q) => q.url));
    await setQueue((await getQueue()).filter((q) => !sent.has(q.url)));
    return queue.length;
  }

  function describeResult(r) {
    if (r.queued !== undefined) return `NAS not reachable, the link will be sent later (${r.queued} waiting).`;
    const parts = [];
    if (r.added) parts.push(r.added === 1 ? 'Added to SynoChan.' : `${r.added} links added.`);
    if (r.duplicate) parts.push(r.duplicate === 1 ? 'Already tracked.' : `${r.duplicate} already tracked.`);
    if (r.invalid) parts.push(r.invalid === 1 ? 'Not a 4chan thread or board link.' : `${r.invalid} were not thread or board links.`);
    if (r.remembered) parts.push('A board was added with its earlier filters; change them in SynoChan.');
    return parts.join(' ') || 'Nothing to add.';
  }

  async function openSynoChan() {
    const { server } = await getConfig();
    if (!server) {
      chrome.runtime.openOptionsPage();
      return;
    }
    chrome.tabs.create({ url: `${server}/?launchApp=SYNO.SDS.SynoChan.Application` });
  }

  return {
    classify, normaliseServer, getConfig, ping, addUrls, flush, getQueue, describeResult, openSynoChan,
    getRetryMinutes, setRetryMinutes, nextRetry, RETRY_CHOICES, FLUSH_ALARM,
  };
})();
