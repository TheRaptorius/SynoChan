# SynoChan

<img align="right" width="96" height="96" src="assets/icon.png" alt="">

SynoChan, by TheRaptorius, archives 4chan threads and boards on a Synology NAS. It runs as a native DSM 7 package and opens as a window inside DSM.

- Track threads: every file is saved as it is posted, until the thread is archived or deleted.
- Track boards: every new thread on the board is tracked automatically. Threads you remove stay removed.
- Board filters: skip threads containing given words or phrases, or take only threads containing them.
  Whole words, case and accents ignored, `*` for partial words; sticky threads skipped. Each board can add
  its own filters. Filtered threads still on the board are listed and can be added anyway.
- Prioritise threads that are about to disappear: their files are downloaded before everything else.
- Folders named by thread number, number and subject, or subject and number. Threads without a subject
  get one from the start of the opening post.
- Optionally save the thread page (`Thread.html`) with links rewritten to the saved files.
- Choose how files are named: post number, original name, or both.
- Follows the [4chan API rules](https://github.com/4chan/4chan-API): at most one API request per second,
  at least 10 seconds between checks of a thread, conditional requests. Files download in parallel.
- Back-off on rate limiting and automatic retries. Lists survive restarts.
- Light and dark theme, filters, sorting and columns in the UI.

It is a single static Go binary with no dependencies, so it runs on x86_64, ARMv8 (aarch64), ARMv7 and i686 models.

## Install

Requires DSM 7.2 or later.

**From the package source (recommended, gets updates):**

1. Package Center > Settings > General: set Trust Level to "Any publisher".
2. Package Center > Settings > Package Sources > Add. Location: `https://theraptorius.github.io/SynoChan/index.json`
3. Find SynoChan under Community and install it.

**Manually:** download the `.spk` from [Releases](../../releases) and use Package Center > Manual Install.

The package creates a shared folder named `synochan` for downloads and runs as the unprivileged system user `synochan`.
To save elsewhere, pick another folder in SynoChan's settings and give the user `synochan` read/write access to that
shared folder (Control Panel > Shared Folder > Edit > Permissions, then show system internal users).

Only members of the `administrators` group can use the app.

## Use

Open SynoChan from the DSM main menu. Paste thread or board links into the field (several at once, separated by commas,
spaces or new lines), or drop links onto the window. With an empty field, Add takes the links from the clipboard.

Right-click a row (or use its menu) to show the folder in File Station, open the thread, copy its link, rename it or
remove it. "Copy all links" produces a comma separated list that can be pasted back in, e.g. to move a list between machines.

Logs are under Menu > Log, and in `/var/packages/synochan/var/synochan.log`.

## Browser extension

The extension for Chrome and Edge adds links with a right click on 4chan: **Add link to SynoChan** on thread and
board links, **Add this page to SynoChan** on thread and board pages (or <kbd>Alt</kbd>+<kbd>Shift</kbd>+<kbd>S</kbd>).
Its toolbar button adds the current page or a list of links, and opens SynoChan in DSM. When the NAS cannot be
reached, links are kept and sent once it can.

1. Download `synochan-extension-<version>.zip` from [Releases](../../releases) and unzip it.
2. Open `chrome://extensions`, turn on Developer mode, choose **Load unpacked** and pick the unzipped folder.
3. In SynoChan, open **⋯ > Settings > Browser extension**, create a token and copy it together with the server address
   into the extension's settings.

The extension signs in with that token, never with your DSM password. A token can only add links, and you can
revoke it in SynoChan at any time. With a self-signed certificate on the NAS, open its address in a tab once and
accept the certificate first.

## How it works

```
DSM window (ui/index.html)
      │  fetch api.cgi?path=…        DSM web server, DSM login checked by authenticate.cgi
      ▼
synochan cgi ───── unix socket ────▶ synochan daemon ──▶ 4chan API / CDN
                                         │
                                         ├─ settings.json, state.json, synochan.log  (/var/packages/synochan/var)
                                         └─ downloads                               (shared folder)
```

The same binary is the background service (`synochan daemon`) and the CGI front end that DSM's web server runs for
each API call. No network port is opened.

## Build

Needs Go (see `go.mod`).

```sh
make test
make spk VERSION=1.0.0      # writes dist/synochan-1.0.0-0001-noarch.spk
scripts/build-extension.sh 1.0.0   # writes dist/synochan-extension-1.0.0.zip
make run                    # local service with the UI on http://127.0.0.1:8580, no login
```

Releases: push a tag like `v1.0.0`. The `Release` workflow tests, builds the SPK, creates the GitHub release and
publishes the package source index to GitHub Pages. Enable Pages with source "GitHub Actions" in the repository
settings first.

## License

GPL-3.0-or-later. See [LICENSE](LICENSE) and [NOTICE](NOTICE) for the origin of this code.
