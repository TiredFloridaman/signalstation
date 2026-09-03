package main

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

// linkDesktop connects a Signal Desktop profile to an account.
//
// How the linking URI is obtained differs by platform, because Signal Desktop
// only ever shows it as an on-screen QR code:
//
//   - macOS and Linux: Signal Station screenshots the display and decodes the
//     QR automatically. This is the hands-off path.
//   - Windows 11: Signal marks its windows WDA_EXCLUDEFROMCAPTURE by default, so
//     every screen-capture API returns a blank where the window is. That
//     exclusion is enforced in the kernel and cannot be turned off from another
//     process by any supported means, so automatic reading is impossible. The
//     user reads the QR with a phone camera and pastes the link instead — the
//     camera reads light off the glass, which the DRM flag does not affect.
func (s *Station) linkDesktop(id string) {
	acct := s.store.Account(id)
	if acct == nil {
		return
	}
	cfg := s.store.Config()

	if !fileExists(cfg.DesktopPath) {
		s.showError(errors.New("Signal Desktop was not found. Set its location in Settings"))
		return
	}
	if !s.cli.Available() {
		s.showError(errors.New("signal-cli was not found. Set its location in Settings"))
		return
	}
	if acct.Stage != StageRegistered && acct.Stage != StageLinked {
		s.showError(errors.New("register " + acct.Number + " before linking a desktop profile"))
		return
	}

	// Every platform now attempts the automatic screen scan. On macOS and Linux
	// it is reliable. On Windows 11 it works only when Signal's screen-capture
	// protection happens to be off for that install; when the window is
	// protected the scan simply finds nothing and the always-present paste box
	// takes over. Trying and degrading gracefully is better than assuming
	// failure, since the assumption is not true on every Windows machine.
	s.linkWithScan(id)
}

// -----------------------------------------------------------------------------
// Automatic screen scan (all platforms), with paste fallback
// -----------------------------------------------------------------------------

// linkWithScan opens Signal Desktop and reads the QR code off the screen. If the
// scan finds nothing — which is what happens when Signal's Windows 11 screen
// protection is active — the always-present paste box lets the user finish with
// a phone camera instead.
func (s *Station) linkWithScan(id string) {
	acct := s.store.Account(id)
	if acct == nil {
		return
	}

	var extra string
	switch runtime.GOOS {
	case "darwin":
		extra = "\n\nmacOS will ask for Screen Recording permission the first time. Signal " +
			"Station needs it to read the QR code, and captures nothing else."
	case "windows":
		extra = "\n\nOn Windows 11, Signal sometimes hides its window from screen capture. If the " +
			"scan comes up empty, point your phone camera at the QR code and paste the sgnl:// " +
			"link it offers — a box for that appears automatically."
	}
	intro := widget.NewLabel(
		"Signal Station will open a Signal Desktop window for " + acct.Label + ", wait for its " +
			"QR code, read it off the screen, and approve it with signal-cli.\n\n" +
			"Leave that window visible and unobstructed while this runs." + extra)
	intro.Wrapping = fyne.TextWrapWord

	dialog.ShowCustomConfirm("Link Signal Desktop", "Start", "Cancel", intro, func(ok bool) {
		if ok {
			s.runScan(id)
		}
	}, s.win)
}

func (s *Station) runScan(id string) {
	acct := s.store.Account(id)
	if acct == nil {
		return
	}
	cfg := s.store.Config()

	// Best-effort: ask Signal to leave its window capturable, by writing
	// contentProtection:false into this profile before it launches. This is what
	// lets the screen scan work on Windows when the setting is not otherwise
	// locked; on macOS and Linux it is harmless. If Signal ignores or overwrites
	// it, the scan just fails and the paste box takes over.
	if s.store.ScreenSecurityDisabled() {
		_ = disableContentProtection(acct.ProfileDir)
	}

	if err := LaunchDesktop(cfg.DesktopPath, acct.ProfileDir); err != nil {
		s.showError(err)
		return
	}

	progress := widget.NewProgressBarInfinite()
	progress.Start()

	stateLbl := widget.NewLabel("Waiting for the Signal Desktop QR code to appear…")
	stateLbl.Wrapping = fyne.TextWrapWord

	manual := widget.NewMultiLineEntry()
	manual.SetPlaceHolder("sgnl://linkdevice?uuid=…&pub_key=…")
	manual.Wrapping = fyne.TextWrapBreak

	manualHelp := widget.NewLabel(
		"If the code is not found automatically, point a phone camera at it and paste the " +
			"sgnl:// link it offers here.")
	manualHelp.Wrapping = fyne.TextWrapWord

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)

	body := container.NewVBox(
		stateLbl, progress, widget.NewSeparator(), manualHelp, manual,
	)

	var d *dialog.CustomDialog
	done := false

	finish := func(uri string) {
		if done {
			return
		}
		done = true
		cancel()
		progress.Stop()
		if d != nil {
			d.Hide()
		}
		s.approveLink(id, uri)
	}

	useManual := widget.NewButton("Use pasted link", func() {
		uri := normalizeLinkText(manual.Text)
		if !IsLinkURI(uri) {
			s.showError(errors.New("that does not look like a Signal linking link. It should start with sgnl://linkdevice"))
			return
		}
		finish(uri)
	})

	cancelBtn := widget.NewButton("Cancel", func() {
		cancel()
		progress.Stop()
		if d != nil {
			d.Hide()
		}
	})

	body.Add(container.NewHBox(useManual, cancelBtn))

	d = dialog.NewCustomWithoutButtons("Linking "+acct.Label, body, s.win)
	d.Resize(fyne.NewSize(600, 440))
	d.SetOnClosed(cancel)
	d.Show()

	go func() {
		select {
		case <-time.After(6 * time.Second):
		case <-ctx.Done():
			return
		}
		fyne.Do(func() { stateLbl.SetText("Scanning the screen for the QR code…") })

		uri, err := WatchForLinkURI(ctx, 2500*time.Millisecond)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			fyne.Do(func() {
				progress.Stop()
				msg := "Could not find the QR code on screen. Point a phone camera at it and " +
					"paste the link below instead."
				if errors.Is(err, ErrNoDisplays) {
					msg = "Signal Station cannot capture the screen."
					if runtime.GOOS == "darwin" {
						msg += " Grant it Screen Recording permission in System Settings › " +
							"Privacy & Security, then quit and reopen Signal Station."
					}
					msg += " You can paste the link below instead."
				}
				stateLbl.SetText(msg)
			})
			return
		}
		fyne.Do(func() { finish(uri) })
	}()
}

// -----------------------------------------------------------------------------
// Shared
// -----------------------------------------------------------------------------

// approveLink hands the captured URI to signal-cli, which authorises the new
// device against the Signal service.
func (s *Station) approveLink(id, uri string) {
	acct := s.store.Account(id)
	if acct == nil {
		return
	}

	s.async("Approving the new device…", func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		return s.cli.AddDevice(ctx, acct.Number, uri)
	}, func(err error) {
		if err != nil {
			s.showError(err)
			return
		}
		_ = s.store.UpdateAccount(id, func(a *Account) { a.Stage = StageLinked })
		s.refreshAll()

		dialog.ShowConfirm("Linked",
			acct.Label+" is linked. The Signal Desktop window should finish setting itself up "+
				"in a moment.\n\nSend your contact list to it now? A freshly linked profile "+
				"starts out empty.",
			func(yes bool) {
				if yes {
					s.sendContacts(id)
				}
			}, s.win)
	})
}

// -----------------------------------------------------------------------------
// Linking-URI parsing
// -----------------------------------------------------------------------------
//
// These live here, next to the paste box that is their main consumer, rather
// than in qrscan.go. qrscan.go also calls IsLinkURI, but co-locating the
// definitions with the UI that handles untrusted pasted text keeps the build
// from breaking if one file is updated and the other is not.

// IsLinkURI reports whether text is a Signal device-linking URI. Current Signal
// Desktop emits sgnl://linkdevice?uuid=…&pub_key=…; older builds used
// tsdevice:/?uuid=…, which signal-cli still accepts.
func IsLinkURI(text string) bool {
	t := strings.TrimSpace(text)
	return strings.HasPrefix(t, "sgnl://linkdevice") || strings.HasPrefix(t, "tsdevice:")
}

// normalizeLinkText pulls a clean linking URI out of whatever the user pasted.
//
// A phone's camera or share sheet often hands over more than the bare link: a
// leading label, surrounding whitespace, quotes, or a trailing newline. This
// finds the sgnl:// (or tsdevice:) token within the text and trims it at the
// first whitespace, so the paste box accepts a slightly messy paste rather than
// demanding a surgically exact one.
func normalizeLinkText(text string) string {
	t := strings.TrimSpace(text)
	t = strings.Trim(t, "\"'")

	for _, scheme := range []string{"sgnl://linkdevice", "tsdevice:"} {
		if i := strings.Index(t, scheme); i >= 0 {
			t = t[i:]
			// Cut at the first whitespace: the URI itself contains none.
			if j := strings.IndexAny(t, " \t\r\n"); j >= 0 {
				t = t[:j]
			}
			return t
		}
	}
	return strings.TrimSpace(t)
}
