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

  $('#add-bulk').onclick = async () => {
    const lines = $('#bulk').value.split(/[\s,]+/).filter(Boolean);
    if (!lines.length) {
      setStatus('Paste some links first.', true);
      return;
    }
    const result = await add(lines, $('#add-bulk'));
    if (result && !result.invalid) $('#bulk').value = '';
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
