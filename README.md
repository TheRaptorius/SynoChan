# SynoChan

<img align="right" width="96" height="96" src="assets/icon.png" alt="">

SynoChan, by TheRaptorius, archives 4chan threads and boards on a Synology NAS. It runs as a native DSM 7 package and opens as a window inside DSM.

- Track threads: every file is saved as it is posted, until the thread is archived or deleted.
- Track boards: every new thread on the board is tracked automatically.
- Optionally save the thread page (`Thread.html`) with links rewritten to the saved files.
- Choose how files are named: post number, original name, or both.
- Optionally rename a thread's folder to include its subject when the thread is removed.
- Conditional requests, request spacing, back-off on rate limiting and automatic retries.
- Lists survive restarts. Settings, filters, sorting and columns in the UI.

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
make run                    # local service with the UI on http://127.0.0.1:8580, no login
```

Releases: push a tag like `v1.0.0`. The `Release` workflow tests, builds the SPK, creates the GitHub release and
publishes the package source index to GitHub Pages. Enable Pages with source "GitHub Actions" in the repository
settings first.

## License

GPL-3.0-or-later. See [LICENSE](LICENSE) and [NOTICE](NOTICE) for the origin of this code.
