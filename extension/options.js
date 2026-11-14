'use strict';

const $ = (sel) => document.querySelector(sel);

function setStatus(text, kind) {
  const s = $('#status');
  s.textContent = text;
  s.className = `status ${kind || ''}`;
}

async function init() {
  const { server, token } = await SynoChan.getConfig();
  $('#server').value = server;
  $('#token').value = token;
  const current = await SynoChan.getRetryMinutes();
  $('#retry').replaceChildren(...SynoChan.RETRY_CHOICES.map((m) => {
    const o = document.createElement('option');
    o.value = String(m);
    o.textContent = m === 1 ? 'Try again every minute' : `Try again every ${m} minutes`;
    o.selected = m === current;
    return o;
  }));
}

// The interval applies right away, without "Save and test".
$('#retry').addEventListener('change', async () => {
  await SynoChan.setRetryMinutes(Number($('#retry').value));
  setStatus('Retry interval saved.', 'ok');
});

$('#form').addEventListener('submit', async (e) => {
  e.preventDefault();
  let server;
  try {
    server = SynoChan.normaliseServer($('#server').value);
  } catch (err) {
    setStatus('That is not a valid address.', 'error');
    return;
  }
  const token = $('#token').value.trim();
  if (!token.startsWith('sc_')) {
    setStatus('API tokens start with "sc_". Copy it from SynoChan’s settings.', 'error');
    return;
  }
  // The extension may only talk to the NAS after you allow it, and only to that one address.
  const granted = await chrome.permissions.request({ origins: [`${server}/*`] });
  if (!granted) {
    setStatus('Without access to the NAS address the extension cannot send links.', 'error');
    return;
  }
  $('#server').value = server;
  await chrome.storage.local.set({ server, token });
  setStatus('Testing the connection…');
  try {
    const info = await SynoChan.ping();
    setStatus(`Connected to SynoChan ${info.version}.`, 'ok');
    SynoChan.flush().catch(() => {});
  } catch (err) {
    setStatus(err.message, 'error');
  }
});

// Extension pages may open chrome:// pages only through the tabs API.
$('#shortcuts').addEventListener('click', (e) => {
  e.preventDefault();
  chrome.tabs.create({ url: 'chrome://extensions/shortcuts' });
});

init();
