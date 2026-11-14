# SynoChan

<img align="right" width="96" height="96" src="assets/icon.png" alt="">

SynoChan archives 4chan threads and boards on a Synology NAS. It runs as a native DSM package and opens as a window
inside DSM.

- Track threads: every file is saved as it is posted, until the thread is archived or deleted.
- Track boards: every new thread on the board is tracked automatically. Threads you remove stay removed.
- Board filters: skip threads containing given words or phrases, or take only threads containing them.
  Whole words, case and accents ignored, `*` for partial words; sticky threads skipped. Each board can have
  its own filters, remembered when the board is added again. Filtered threads still on the board are listed
  and can be added anyway.
- Prioritise threads that are about to disappear: their files are downloaded before everything else.
- Folders named by thread number, number and subject, or subject and number. Threads without a subject
  get one from the start of the opening post.
- Optionally save the thread page (`Thread.html`) with links rewritten to the saved files.
- Browser extension: add threads and boards from 4chan with a right click.
- Follows the [4chan API rules](https://github.com/4chan/4chan-API): at most one API request per second,
  at least 10 seconds between checks of a thread, conditional requests. Files download in parallel.

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

### Browser extension (Chrome, Edge, Brave)

1. Download the [latest extension (synochan-extension-chrome.zip)](https://github.com/TheRaptorius/SynoChan/releases/latest/download/synochan-extension-chrome.zip)
   and unzip it.
2. Open `chrome://extensions`, turn on Developer mode, choose **Load unpacked** and pick the unzipped folder.
3. In SynoChan, open **⋯ > Settings > Browser extension**, create a token and copy it together with the server address
   into the extension's settings, which open after installing.

The extension signs in with that token, never with your DSM password. A token can only add links, and you can revoke
it in SynoChan at any time. With a self-signed certificate on the NAS, open its address in a tab once and accept the
certificate first. To update, unzip a newer version over the same folder and click Reload on `chrome://extensions`.

### After installing

The package creates a shared folder named `synochan` for downloads and runs as the unprivileged system user `synochan`.
To save elsewhere, pick another folder in SynoChan's settings and give the user `synochan` read/write access to that
shared folder (Control Panel > Shared Folder > Edit > Permissions, then show system internal users).

Only members of the `administrators` group can use the app.

## Use

Open SynoChan from the DSM main menu. Paste thread or board links into the field (several at once, separated by commas,
spaces or new lines), or drop links onto the window. With an empty field, Add takes the links from the clipboard.

Right-click a row (or use its ⋯ menu) to show the folder in File Station, prioritise a thread, open it on 4chan, copy
its link, rename it or remove it. Boards have their own filters and the list of filtered threads in the same menu.

The log is under ⋯ > Log, and in `/var/packages/synochan/var/synochan.log`.

## Credits and origin

SynoChan started as a port of [GChan](https://github.com/Issung/GChan) **6.6.0** by Issung and contributors, a
Windows application that is itself a fork of YChan. Many thanks to the authors of both: the core of how threads and
boards are tracked and files are named comes from their work.

Compared with GChan 6.6.0, SynoChan:

- **Runs on the NAS:** a DSM package written in Go instead of a Windows Forms application. It runs as a background
  service and opens as a window inside DSM, signed in with your DSM account. Package Center installs and updates it.
- **Drops what only made sense on Windows:** system tray, starting with Windows, its own update check.
- **Adds board filters** (skip and only lists, per-board filters remembered after removal, skipped sticky threads,
  a list of filtered threads with “add anyway”, and an offer to remove tracked threads that new filters match).
- **Adds download priority** for threads about to disappear.
- **Adds a browser extension** with API tokens, a right-click menu, a popup and a queue for when the NAS is unreachable.
- **Names threads better:** a subject from the opening post when there is none, folders named with the subject from
  the first file, and renaming a thread renames its folder.
- **Can remove threads together with their files.**
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
