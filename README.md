# Signal Station

A desktop app for running several Signal accounts side by side. It handles
registration and device linking through `signal-cli`, gives each account its own
Signal Desktop profile, and then opens whichever one you click.

Written in Go with Fyne. It compiles to a single native executable on macOS and
Windows with nothing for the end user to install alongside it.

Not affiliated with Signal Messenger LLC.

---

## What it actually does

Without this app, adding one account means roughly this sequence, per account:

```
signal-cli --config DIR -a +1555... register
# hit a captcha wall, open a browser, solve it, copy a signalcaptcha:// link
signal-cli --config DIR -a +1555... register --captcha "signalcaptcha://..."
signal-cli --config DIR -a +1555... verify 123456
mkdir -p ~/.config/Signal-work
/Applications/Signal.app/Contents/MacOS/Signal --user-data-dir=~/.config/Signal-work
# scan the QR code with something that can read it back as text
signal-cli --config DIR -a +1555... addDevice --uri "sgnl://linkdevice?uuid=...&pub_key=..."
signal-cli --config DIR -a +1555... sendContacts
```

Signal Station wraps that in a two-pane window: a list of accounts on the left,
and a single next-step button on the right that does whatever that account is
waiting for. Once an account is set up, its row has an **Open** button that
launches Signal Desktop against that account's profile.

### How the pieces fit

Signal Desktop is an Electron app, and Electron keys its single-instance lock on
`--user-data-dir`. Point two launches at two directories and you get two
independent Signal windows. That is the whole trick behind running accounts in
parallel, and it is the same mechanism the app uses.

`signal-cli` is the primary device for each account. Signal Desktop is linked to
it as a secondary device, exactly as it would normally be linked to a phone.

The one genuinely awkward step is that Signal Desktop only ever exposes its
linking URI as an on-screen QR code — there is no file, flag, or socket that
hands it over. So the app screenshots the display and decodes the QR, sweeping
the frame in overlapping tiles because a QR occupying a small patch of a 4K
screenshot does not decode from the full image. If that fails, there is a box to
paste the link into manually.

---

## Before you start: the constraints Signal imposes

These are not app limitations, and no tool can work around them.

**Every account needs its own phone number that can receive an SMS or a call.**
Signal ties one account to one number. The app cannot conjure numbers, skip the
captcha, or bypass verification — you solve a captcha and enter a real code for
each account, same as anywhere else.

**Registering a number here makes `signal-cli` its primary device.** If you are
already running Signal on a phone with that number, doing this signs the phone
out. Use numbers you are not using elsewhere, or accept that the phone loses the
account.

**Five linked devices per account, maximum.** Attempting a sixth returns
`StatusCode: 411`. The app reports this in plain language when it happens.

**Registration is rate limited.** Repeated attempts on a number return
`StatusCode: 429`, and only waiting clears it. Retrying immediately extends the
wait, so the app tells you to stop rather than retrying for you.

**Captcha tokens expire in about two minutes.** The app asks for one only after
the server demands it, rather than up front, so you are not racing the clock.

---

## Prerequisites

**signal-cli** — https://github.com/AsamK/signal-cli/releases

The native builds need no Java. The plain archive needs a JRE 21 or newer; if
you use that, set `JAVA_HOME` in Settings.

- macOS: `brew install signal-cli`, or unpack the archive anywhere
- Windows: unpack to `C:\signal-cli`, or anywhere you like

**Signal Desktop** — https://signal.org/download/

Signal Station finds both automatically in the usual install locations. If it
cannot, set the paths in Settings; there is a Browse button and a **Test
signal-cli** button that reports the version it can actually run.

---

## Building

Fyne binds to native UI toolkits through cgo, which changes what can be built
where:

| Building on | macOS app | Windows .exe |
|---|---|---|
| macOS | yes, natively | yes, via mingw-w64 |
| Windows | **no** | yes, natively |
| Linux | **no** | yes, via mingw-w64 |

Producing a macOS build needs Apple's SDK, and Apple's licence restricts that
SDK to Apple hardware. No script gets around it. If you are on Windows, use the
GitHub Actions workflow for the Mac build.

### One command

**macOS or Linux** — installs Go, mingw-w64, and the fyne tool, then builds
everything the host supports:

```bash
./build.sh
```

On a Mac that gives you both `Signal Station.app` (universal: Apple Silicon and
Intel, ad-hoc signed) and `Signal Station.exe`, in `dist/`.

```bash
./build.sh --mac        # macOS only
./build.sh --windows    # Windows only
./build.sh --skip-deps  # don't touch package managers
./build.sh --clean      # wipe dist/ first
```

Homebrew and the Xcode command line tools are the two things it will not install
for you; it prints the exact command and stops.

**Windows** — installs Go and mingw-w64 through winget, then builds
`Signal Station.exe` into `dist\`:

```powershell
.\build.ps1
```

If PowerShell blocks the script, either run
`powershell -ExecutionPolicy Bypass -File .\build.ps1`, or unblock it once with
`Unblock-File .\build.ps1`.

All package installs are pinned to `--source winget`. Managed machines often
have the Microsoft Store disabled by policy, and an unpinned `winget install`
will either prompt to disambiguate or resolve to `msstore` and fail. If the
script reports that the winget source itself is missing, add it with:

```powershell
winget source add --name winget --arg https://cdn.winget.microsoft.com/cache --type Microsoft.PreIndexed.Package
```

Or install Go and MSYS2 by hand and run `.\build.ps1 -SkipDeps`.

**If it says gcc is not found:** MSYS2 ships several parallel toolchains, and
installing one does not put it on PATH. Current MSYS2 defaults to the UCRT
environment (`ucrt64\bin`), not the older `mingw64\bin`, so the script checks
both along with the standalone locations. If you already have a compiler, skip
the search entirely:

```powershell
.\build.ps1 -CC "C:\mingw64\bin"
```

`-SkipDeps` skips *installing* prerequisites, not *finding* them: the script
still locates Go and gcc and puts them on PATH, and refuses to start a build
without a compiler rather than failing halfway through with a cgo error.

The least fiddly toolchain on Windows is a standalone [WinLibs](https://winlibs.com/)
build — download the UCRT runtime zip, extract it to `C:\mingw64`, and pass that
`bin` folder with `-CC`. No MSYS2, no pacman.

Run `.\build.ps1 -Mac` for the options on getting a macOS build.

### By hand

```bash
go mod tidy
go install fyne.io/tools/cmd/fyne@latest   # moved out of the main module at Fyne 2.5

# macOS, universal
CGO_ENABLED=1 GOARCH=arm64 go build -trimpath -ldflags "-s -w" -o dist/signal-station-arm64 .
CGO_ENABLED=1 GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o dist/signal-station-amd64 .
lipo -create -output dist/signal-station dist/signal-station-arm64 dist/signal-station-amd64

# Windows, cross-compiled from macOS or Linux
CGO_ENABLED=1 GOOS=windows GOARCH=amd64 \
  CC=x86_64-w64-mingw32-gcc CXX=x86_64-w64-mingw32-g++ \
  go build -trimpath -ldflags "-s -w -H=windowsgui" -o dist/signal-station.exe .

# Windows, built on Windows
go build -trimpath -ldflags "-s -w -H=windowsgui" -o signal-station.exe .
```

`-H=windowsgui` is what stops a console window opening behind the app.

### With GitHub Actions (the answer if you only own one machine)

Push the repo, then run the **Build executables** workflow. It builds macOS and
Windows on hosted runners and attaches both as artifacts. See
`.github/workflows/build.yml`.

### Running from source

```bash
go mod tidy && go run .
```

---

## First run

1. **Add account** — give it a name and a phone number in `+` international form.
2. The app requests a code. When Signal demands a captcha, it opens the captcha
   page for you. Solve it, then **click the "Open Signal" link** — Signal Station
   registers itself as the handler for those links, so the token comes straight
   back and registration continues on its own. (If clicking does nothing because
   the handler is not registered, right-click the link, copy the link address,
   and paste it into the app instead.)
3. Enter the code that arrives by SMS. If the number cannot receive SMS, there
   is a **Call me with the code instead** button.
4. **Link Signal Desktop.** The app opens a Signal Desktop window with a fresh
   profile, waits for its QR code, reads it off the screen, and approves it.
   Leave that window visible while this runs.
5. Say yes to sending contacts — a freshly linked profile starts out empty.

From then on, click **Open** on any account row.

### macOS Screen Recording permission

Reading the QR code needs it. macOS prompts on the first attempt; grant it under
**System Settings › Privacy & Security › Screen Recording**, then quit and
reopen Signal Station, because macOS only applies the change on relaunch.

The app captures the screen solely while it is hunting for a linking QR code,
and stops as soon as it finds one or the dialog closes.

### macOS Gatekeeper

An unsigned app is blocked on first launch. Right-click it and choose **Open**,
or:

```bash
xattr -dr com.apple.quarantine "Signal Station.app"
```

---

## Where things are kept

| | |
|---|---|
| macOS | `~/Library/Application Support/SignalStation` |
| Windows | `%APPDATA%\SignalStation` |

Inside: `accounts.json`, `signal-cli-data/` (the signal-cli account store, kept
separate from any signal-cli setup you already have), `profiles/` (one Signal
Desktop profile per account), and `logs/`.

To back up or move an account, copy its folder from `profiles/` along with
`signal-cli-data/`. Both are unencrypted at rest to the same degree Signal
Desktop's own profile is, so treat that folder as sensitive.

The log redacts verification codes, captcha tokens, and PINs, so it is safe to
attach to a bug report.

**Screen security.** By default, Signal Station disables Signal's screen-capture
protection in the profiles it manages, so it can read the linking QR code on
Windows 11. If you would rather keep that protection on and link by phone camera
instead, turn off "Disable Signal's screen-capture protection" in Settings. It
only ever affects profiles Signal Station creates, and you can change it per
profile from inside Signal afterwards.

**Removing an account** from Signal Station does not unregister the number with
Signal. To give a number up entirely:

```
signal-cli --config <data dir> -a +1555... unregister
```

---

## Staying connected (persistent per-account daemons)

While Signal Station is open, each registered or linked account holds its own
**long-lived `signal-cli` connection** — one `signal-cli -a <number> jsonRpc`
process per account, started when the app opens and kept running. Each connects
to Signal once and **streams messages in real time** as they arrive, rather than
the app polling every few minutes.

This is what keeps accounts online, and it feeds the daily export instantly. It
also means `libsignal` is unpacked once per process instead of on every poll, so
the temp-file churn is gone. If a connection drops, it is restarted
automatically with backoff. When you close Signal Station, every connection is
shut down.

**Settings › Keep accounts online in the background** is the on/off switch for
these connections (the export also turns them on, since it needs the stream).
With it off, no background connection is held and nothing is received until you
act manually.

---

## Daily message export (for an AI agent)

**Settings › Export messages daily for an AI agent** writes one folder per day
that an AI agent can read to produce daily summaries. Point it at any folder your
agent watches — for example a synced folder.

Because `signal-cli` is a linked device, it sees both directions: messages you
**receive** and messages you **send** from your phone (delivered to linked
devices as sync messages). Both are captured, across every registered account.

Capture is a continuous journal: while export is on, every background receive is
recorded as it happens, so nothing is missed between builds. Turning export on
also keeps accounts online, since a steady receive stream is what feeds it.

### How it is laid out

One folder per account, then per day, then per chat — so you can point an agent
at a single conversation, a single account, or the whole tree.

```
<export folder>/
└── Work/                         one folder per account (its label, else number)
    └── 2026-09-24/               one folder per day
        ├── index.md             the day's chats at a glance, with links
        ├── Bob/                  one folder per chat
        │   ├── messages.json     structured records for this chat
        │   ├── transcript.md     readable log for this chat
        │   └── attachments/      this chat's images and files
        └── Launch Crew/
            ├── messages.json
            ├── transcript.md
            └── attachments/
```

The account folder is named after the account's label (set when you add it),
falling back to its phone number. Each chat folder is named after the contact or
group; a chat is one folder even though messages flow both ways.

`messages.json` is the machine-readable feed for that chat. Each message carries
`direction` (`sent`/`received`), the local `time`, a stable `conversation_id`,
the sender (`from`, plus `from_number` and `from_uuid` where known), and for
each attachment a relative `file` path into that chat's `attachments/`.
`transcript.md` embeds images inline so a multimodal agent sees them. `index.md`
summarises the account's day and links to each chat.

### Group chats and senders

Group chats are exported like any other conversation — one folder per group —
and every message shows **who sent it**, which matters when several people are
talking. The sender is the person's Signal profile name; when they haven't
shared one, it falls back to their phone number, and failing that a short label
from their account id, so a sender is never blank. (signal-cli's message data
does not include Signal @usernames, so the profile name is the closest available
identifier; the raw number and account id are in `messages.json` for exact
matching.)

Signal's received-message data usually omits the group's name and carries only
its id. Signal Station resolves real names separately via `signal-cli
listGroups`, caches them, and names each group folder accordingly — so you get
`Q4 Planning/` rather than `Group aBcD1234/`. A brand-new group may show the id
placeholder until the next background refresh fills its name in, after which the
next rebuild renames the folder.

### How it builds

- **Continuously**: messages are journaled as they arrive.
- **At startup**: any past day that was never built (the app was closed at
  midnight) is assembled, and today is (re)built.
- **Every 30 minutes**: today's bundle is refreshed as more arrives.
- **On demand**: **Settings › Export now** builds today immediately.

The journal lives in the app data folder; only the finished daily bundles go to
your chosen folder. Capture begins when you enable it — earlier history is not
available, because signal-cli only receives messages sent while it is linked.

### Handing it to an agent

Point your agent at whatever scope you want summarised: one chat's folder, an
account's day folder (read `index.md`, then each chat's `transcript.md`), or the
whole tree. A prompt as simple as *"summarise each chat's transcript.md under
today's folder and flag anything needing a reply"* works; for structured
processing, parse the per-chat `messages.json` files.

---

## Captcha links

Signal's captcha page finishes with an "Open Signal" link whose address is
`signalcaptcha://<token>`. Signal Station registers itself as the handler for
that scheme — on Windows via a per-user registry key written at first launch, on
macOS via a `CFBundleURLTypes` entry in the app bundle — so clicking the link
hands the token directly to the app. No copy-paste.

If a second copy of Signal Station is launched by the click, it detects the
already-running instance, passes the token to it through a small file in the data
folder, and exits, so you never get a duplicate window. The manual paste box is
always there as a fallback, which matters when running as a bare executable
(no bundle or registry entry) or if a browser refuses to open custom schemes.

## Troubleshooting

**"redeclared in this block"** — there is a duplicate source file in the folder,
such as `signalcli(1).go`, from downloading the same file twice. Go compiles
every `.go` file in the directory, so each declaration appears twice. Delete the
copies:

```powershell
Get-ChildItem -Path '*(*).go' | Remove-Item     # Windows
```
```bash
rm ./*\(*\).go                                  # macOS / Linux
```

The folder needs exactly one copy of each `.go` file. Cloning the repo, or
downloading it as a zip and extracting it, avoids this entirely.

**Linking reads the QR code off the screen automatically, on every platform.**
Signal Station opens the Signal Desktop window, scans for the QR, and approves
it. Leave the window visible and unobstructed while it works. On macOS, grant
Screen Recording permission the first time (System Settings → Privacy & Security
→ Screen Recording), then quit and reopen Signal Station.

**If the scan finds nothing, a paste box takes over.** This is the expected path
when Signal's Windows 11 screen-capture protection is active for that install:
the window is excluded from capture, so the scan sees blank where the QR is.
Point your phone camera at the code, and it offers a link starting with
`sgnl://` — paste that into the box that appears and linking continues. Signal
Station also writes `contentProtection: false` into each profile it creates
before first launch, which on many Windows setups is enough to keep the window
capturable and let the scan succeed; when Signal overrides that, the paste box
is the reliable fallback.

**Windows: "is not recognized as an internal or external command, operable
program or batch file"** — a Signal linking URI contains an `&`. Run through
`cmd.exe`, that `&` is read as a command separator, so the shell tries to
execute the rest of the URI as a second command.

Signal Station now avoids `cmd.exe` altogether. It starts signal-cli as a plain
Java process (`java -classpath "…\lib\*" org.asamk.signal.Main`), so no shell
ever parses the arguments. **Settings → Test signal-cli** reports which method is
in use; you want "native executable" or "java directly", not "cmd.exe".

If it still says cmd.exe, no JVM was found. Either set `JAVA_HOME` in Settings,
or download the `signal-cli.exe` native build, which needs no Java at all.

There is a subtler version of this trap worth knowing about. signal-cli ships
`bin/signal-cli` (a shell script) beside `bin/signal-cli.bat`, and pointing at
the extensionless one looks harmless. Windows resolves it through `PATHEXT` to
the `.bat` and `CreateProcess` runs batch files by spawning `cmd.exe` internally
— so a shell parses the arguments even though nothing asked for one, and the URI
is truncated at its first `&`. Signal Station now resolves the extension itself
before deciding how to launch, and refuses outright to send an argument
containing `&` through a shell rather than transmitting a corrupted one.

**"Authentication failed" when linking** — `signal-cli` is not the primary
device for that number. This happens if the number was registered elsewhere.

**`StatusCode: 411`** — the account is at five linked devices. Use **Show linked
devices**, then remove one.

**Signal Desktop opens the wrong account** — something launched it without
`--user-data-dir`, so it used the default profile. Always launch from Signal
Station.

**Empty contact list in a new profile** — use **Send contacts to linked
devices**, then wait a minute.

**Windows: `signal-cli.bat` does nothing** — usually a missing or too-old Java.
Use **Test signal-cli** in Settings, which reports what actually went wrong.

---

## Layout

```
build.sh         installs prerequisites, builds macOS + Windows (run on macOS)
build.ps1        installs prerequisites, builds Windows (run on Windows)
main.go          app startup, background keep-online loop
ui_main.go       two-pane window, account rows, detail pane
ui_add.go        add account, registration, captcha, verification
ui_link.go       Signal Desktop linking with QR capture
ui_settings.go   tool paths and options
store.go         account model and atomic JSON persistence
signalcli.go     signal-cli wrapper and error translation
desktop.go       Signal Desktop discovery and per-profile launch
qrscan.go        screen capture and QR decoding
protocol.go      registers the signalcaptcha:// URL scheme handler
captcha_ipc.go   delivers a clicked captcha link to the running instance
export.go        journals messages and builds the daily export bundles
screensecurity.go writes Signal's contentProtection flag so the QR is readable
theme.go         colour and type scale
widgets.go       tappable row container
exec_windows.go  hides console windows
exec_unix.go     detaches child processes
```
