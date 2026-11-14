// Context menus, keyboard shortcut and retrying of links that could not be sent.
'use strict';

importScripts('shared.js');

const HOSTS = ['boards.4chan.org', 'boards.4channel.org'];
// Menus appear only for thread and board links; the URL is checked again when clicked.
const LINK_PATTERNS = HOSTS.flatMap((h) => [`*://${h}/*/thread/*`, `*://${h}/*/catalog`, `*://${h}/*/`]);

function setUpMenus() {
  chrome.contextMenus.removeAll(() => {
    chrome.contextMenus.create({
      id: 'add-link',
      title: 'Add link to SynoChan',
      contexts: ['link'],
      targetUrlPatterns: LINK_PATTERNS,
    });
    chrome.contextMenus.create({
      id: 'add-page',
      title: 'Add this page to SynoChan',
      contexts: ['page'],
      documentUrlPatterns: LINK_PATTERNS,
    });
  });
}

chrome.runtime.onInstalled.addListener(async (details) => {
  setUpMenus();
  if (details.reason === 'install') chrome.runtime.openOptionsPage();
});
chrome.runtime.onStartup.addListener(setUpMenus);

function notify(message, isError) {
  chrome.notifications.create({
    type: 'basic',
    iconUrl: 'icons/icon_128.png',
    title: isError ? 'SynoChan: could not add' : 'SynoChan',
    message,
    priority: isError ? 1 : 0,
  });
}

async function addAndNotify(url) {
  if (!SynoChan.classify(url)) {
    notify('This is not a 4chan thread or board link.', true);
    return;
  }
  try {
    const result = await SynoChan.addUrls([url]);
    notify(SynoChan.describeResult(result), false);
  } catch (e) {
    notify(e.message, true);
    if (e.kind === 'config') chrome.runtime.openOptionsPage();
  }
}

chrome.contextMenus.onClicked.addListener((info) => {
  if (info.menuItemId === 'add-link') addAndNotify(info.linkUrl);
  if (info.menuItemId === 'add-page') addAndNotify(info.pageUrl);
});

chrome.commands.onCommand.addListener(async (command, tab) => {
  if (command !== 'add-current-page') return;
  const active = tab || (await chrome.tabs.query({ active: true, currentWindow: true }))[0];
  if (active && active.url) addAndNotify(active.url);
});

chrome.alarms.onAlarm.addListener(async (alarm) => {
  if (alarm.name !== SynoChan.FLUSH_ALARM) return;
  try {
    const sent = await SynoChan.flush();
    if (sent) notify(sent === 1 ? 'A waiting link was sent to SynoChan.' : `${sent} waiting links were sent to SynoChan.`, false);
  } catch (e) {
    // A rejected token or a setup problem: keep the links and stop retrying until something changes.
    chrome.alarms.clear(SynoChan.FLUSH_ALARM);
    notify(`Waiting links could not be sent: ${e.message}`, true);
  }
});
