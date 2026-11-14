# SynoChan

<img align="right" width="96" height="96" src="assets/icon.png" alt="">

SynoChan archives 4chan threads and boards on a Synology NAS. It runs as a native DSM package and opens as a window
inside DSM.

- **Threads:** every file is saved as it is posted, until the thread is archived or deleted.
- **Boards:** every new thread on a board is tracked automatically. Threads you remove stay removed.
- **Board filters:** skip threads by words or phrases, or take only threads that contain them. Each board can have its
  own filters, and they are remembered when a board is added again.
- **Priority:** threads about to disappear can be downloaded before everything else.
- **Readable folders:** named by thread number and subject. Threads without a subject get one from the opening post,
  and renaming a thread renames its folder.
- **Saved thread page:** optionally `Thread.html` with links to the saved files, thumbnails included.
- **Browser extension:** add threads and boards from 4chan with a right click.
- **Polite to 4chan:** follows the [4chan API rules](https://github.com/4chan/4chan-API) (at most one API request per
  second, at least 10 seconds between checks of a thread, conditional requests); files download in parallel.

A single static binary with no dependencies, for x86_64, ARMv8 (aarch64), ARMv7 and i686 models.

## Installation

Requires DSM 7.2 or later.

### From the package source (recommended)

Updates then show up in Package Center like those of any other package.

1. Package Center > Settings > General: set Trust Level to “Any publisher”.
2. Package Center > Settings > Package Sources > Add, with the location
   `https://theraptorius.github.io/SynoChan/index.json`
3. Find SynoChan under Community and install it.

### Manually

1. Download the [latest package (synochan.spk)](https://github.com/TheRaptorius/SynoChan/releases/latest/download/synochan.spk).
   Older versions are on the [Releases](https://github.com/TheRaptorius/SynoChan/releases) page.
2. Package Center > Manual Install, and pick the downloaded file.

Updates are not offered this way; install a newer package the same way to update.

### Browser extension

The extension works in browsers built on Chromium: **Chrome, Edge, Brave, Opera and Vivaldi**. Firefox and Safari are
not supported.

1. Download the [latest extension (synochan-extension-chromium.zip)](https://github.com/TheRaptorius/SynoChan/releases/latest/download/synochan-extension-chromium.zip)
   and unzip it into a folder you keep.
2. Open the browser's extensions page (`chrome://extensions`, `edge://extensions`, `brave://extensions`,
   `opera://extensions` or `vivaldi://extensions`), turn on Developer mode, choose **Load unpacked** and pick that folder.
3. In SynoChan, open **⋯ > Settings > Browser extension**, create a token and copy it together with the server address
   into the extension's settings, which open after installing.

The extension signs in with that token, never with your DSM password. A token can only add links, and you can revoke
it in SynoChan at any time. With a self-signed certificate on the NAS, open its address in a tab once and accept the
certificate first.

To update, unzip a newer version over the same folder and click Reload on the extensions page. Settings are kept as long
as the extension stays installed from the same folder.

### After installing

The package creates a shared folder named `synochan` for downloads and runs as the unprivileged system user `synochan`.
To save elsewhere, pick another folder in SynoChan's settings and give the user `synochan` read/write access to that
shared folder (Control Panel > Shared Folder > Edit > Permissions, then show system internal users).

Only members of the `administrators` group can use the app.

## Use

Open SynoChan from the DSM main menu.

### Adding threads and boards

Type or paste into the field at the top and press Add. Several entries can go in at once, separated by commas, spaces
or new lines; links can also be dropped onto the window. With an empty field, Add takes the links from the clipboard.

Accepted forms:

| What | Examples |
|---|---|
| Thread | `https://boards.4chan.org/g/thread/123456`, with or without `#p123460` at the end; `/g/thread/123456` |
| Board | `https://boards.4chan.org/g/`, `https://boards.4chan.org/g/catalog`, `/g/`, `/g`, `g/`, `>>>/g/` |

`boards.4channel.org` links work too. A custom subject can be given as the last query value of a thread link, with
underscores for spaces: `…/thread/123456?subject=My_Subject`.

With the browser extension: right-click a thread or board link and choose **Add link to SynoChan**, right-click a thread
or board page and choose **Add this page to SynoChan**, or press <kbd>Ctrl</kbd>+<kbd>Shift</kbd>+<kbd>Y</kbd>. The
toolbar button adds the current page or a list of links and shows whether the NAS is reachable. Links that cannot be
sent are kept and sent again automatically.

### The lists

Right-click a row (or use its ⋯ button) to show its folder in File Station, open it on 4chan, copy its link, rename it
or remove it. Select several rows with Ctrl or Shift to act on all of them; the rows keep their place while something is
selected.

- **Prioritize** puts a thread's files before everything else. Prioritised threads are pinned on top with `#1`, `#2`,
  …, and lose the priority once all their files are saved.
- **Remove** stops tracking a thread and keeps its files; **Remove and delete folder** deletes them too.
- Threads keep being checked until 4chan removes them; then they leave the list and their files stay.

The log is under ⋯ > Log (with a filter for warnings and errors, copy and download), and in
`/var/packages/synochan/var/synochan.log`. Settings > Troubleshooting turns on detailed logging.

### Board filters

Filters decide which threads a board adds. They never touch threads you add yourself.

- **Skip threads containing** keeps out threads that contain any of the entries.
- **Only threads containing**, when not empty, lets in only threads that contain one of the entries.
- **Skip sticky threads** (on by default) keeps out pinned threads such as rules and FAQs.

Entries are words or phrases. They match whole words, ignoring case and accents (`cafe` matches “Café”, but `trap`
does not match “trapped”). A phrase matches its words in order. `*` at the start or end of a word allows more letters:
`trap*` matches “trapped”, `*trap` matches “mousetrap”, `*trap*` matches both. The subject, the poster's name and the
whole opening post are searched. When a thread matches both lists, it is skipped.

Settings > Board filters holds the global filters. **Filters…** in a board's menu adds filters for that board only:
its skip entries add to the global ones, and its only-list, when filled in, replaces the global one. **Copy** and
**Clear** under each list help move entries between them.

When filters change, boards are checked again right away:

- Threads the new filters let in are added, unless you removed them before.
- Tracked threads the new filters would keep out are listed in a dialog that offers to remove them, optionally with
  their files. Nothing is removed without confirmation.

The **Filtered** column of a board counts the threads on it that the filters keep out right now. Click it to see them
with the reason, open them on 4chan, or **Add anyway**.

A board's own filters are remembered after the board is removed. When it is added again in SynoChan, you are asked
whether to use them before the board is checked (without an answer, it starts with them after two minutes). Added from
the browser extension, it simply gets them back.

## Credits and origin

SynoChan started as a port of [GChan](https://github.com/Issung/GChan) **6.6.0** by Issung and contributors, a
Windows application that is itself a fork of YChan. Many thanks to the authors of both: the core of how threads and
boards are tracked and files are named comes from their work.

Compared with GChan 6.6.0, SynoChan:

- **Runs on the NAS:** a DSM package written in Go instead of a Windows Forms application. It runs as a background
  service and opens as a window inside DSM, signed in with your DSM account. Package Center installs and updates it.
- **Drops what only made sense on Windows:** system tray, starting with Windows, its own update check.
- **Adds board filters** (skip and only lists, per-board filters remembered after removal, skipped sticky threads,
  re-checking boards when filters change, a list of filtered threads with “add anyway”, and an offer to remove tracked
  threads that new filters match).
- **Adds download priority** for threads about to disappear.
- **Adds a browser extension** for Chromium browsers, with API tokens, a right-click menu, a keyboard shortcut, a popup
  with connection status, and a queue with automatic retries for when the NAS is unreachable.
- **Accepts short forms** of board and thread addresses (`/g/`, `>>>/g/`, `/g/thread/123456`).
- **Names threads better:** a subject from the opening post when there is none, folders named with the subject from
  the first file, and renaming a thread renames its folder.
- **Can remove threads together with their files,** and boards remember threads you removed.
- **Follows the 4chan API rules strictly** (one API request per second, 10 seconds between checks of a thread), checks
  threads and boards before downloading files, and downloads files in parallel.
- **Saves files safely:** downloads are written to a temporary file first, so interrupted downloads never leave
  broken files; names are kept short enough for encrypted shared folders and valid for Windows (SMB) clients.
- **Fixes the saved thread page:** video links and thumbnails, including MP4.
- **Keeps its lists in JSON** instead of SQLite, written atomically.
- **Extends the interface:** light and dark theme, filtering, sorting, keyboard shortcuts, a log viewer with filter,
  copy and download, detailed logging on demand, opening folders in File Station.

## License

GPL-3.0-or-later, like GChan. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
