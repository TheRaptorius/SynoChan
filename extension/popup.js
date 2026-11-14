'use strict';

const $ = (sel) => document.querySelector(sel);

function setStatus(text, isError) {
  const s = $('#status');
  s.textContent = text;
  s.className = isError ? 'status error' : 'status';
}

async function refreshQueue() {
  const queue = await SynoChan.getQueue();
  $('#queue').hidden = queue.length === 0;
  $('#queue-text').textContent = queue.length === 1 ? '1 link waiting for the NAS' : `${queue.length} links waiting for the NAS`;
  const next = await SynoChan.nextRetry();
  let hint = '';
  if (queue.length && next) {
    const minutes = Math.max(0, Math.ceil((next - Date.now()) / 60000));
    hint = minutes <= 0 ? 'Next automatic try in less than a minute.' : `Next automatic try in about ${minutes} min.`;
  }
  $('#queue-next').textContent = hint;
}

async function add(urls, button) {
  button.disabled = true;
  setStatus('Sending…');
  try {
    const result = await SynoChan.addUrls(urls);
    setStatus(SynoChan.describeResult(result), result.queued !== undefined || (result.invalid && !result.added && !result.duplicate));
    await refreshQueue();
    return result;
  } catch (e) {
    setStatus(e.message, true);
    return null;
  } finally {
    button.disabled = false;
  }
}

async function init() {
  const { server, token } = await SynoChan.getConfig();
  const configured = !!(server && token);
  $('#setup').hidden = configured;
  $('#main').hidden = !configured;

  const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
  const kind = SynoChan.classify(tab && tab.url);
  $('#current-label').textContent = kind ? kind.label : 'This page is not a 4chan thread or board';
  $('#add-current').disabled = !kind;
  $('#add-current').onclick = () => add([tab.url], $('#add-current'));

  // The popup closes as soon as it loses focus, e.g. while copying the next link; keep what was typed.
  const { draft = '' } = await chrome.storage.local.get('draft');
  $('#bulk').value = draft;
  // Saved on every change: the popup closes the moment it loses focus, so a delayed save could be lost.
  // One small write to extension storage per change costs next to nothing.
  $('#bulk').addEventListener('input', () => chrome.storage.local.set({ draft: $('#bulk').value }));

  $('#add-bulk').onclick = async () => {
    const lines = $('#bulk').value.split(/[\s,]+/).filter(Boolean);
    if (!lines.length) {
      setStatus('Paste some links first.', true);
      return;
    }
    const result = await add(lines, $('#add-bulk'));
    if (result && !result.invalid) {
      $('#bulk').value = '';
      chrome.storage.local.remove('draft');
    }
  };

  $('#retry').onclick = async () => {
    $('#retry').disabled = true;
    try {
      const sent = await SynoChan.flush();
      setStatus(sent ? `${sent} waiting links sent.` : 'The NAS is still not reachable.', !sent);
    } catch (e) {
      setStatus(e.message, true);
    } finally {
      $('#retry').disabled = false;
      refreshQueue();
    }
  };

  $('#open-synochan').onclick = () => SynoChan.openSynoChan();
  $('#open-options').onclick = () => chrome.runtime.openOptionsPage();
  $('#setup-button').onclick = () => chrome.runtime.openOptionsPage();
  await refreshQueue();
}

init();
