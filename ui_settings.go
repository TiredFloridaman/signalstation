package main

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

func (s *Station) showSettings() {
	cfg := s.store.Config()

	cliEntry := widget.NewEntry()
	cliEntry.SetText(cfg.SignalCLIPath)
	cliEntry.SetPlaceHolder("Path to signal-cli")

	deskEntry := widget.NewEntry()
	deskEntry.SetText(cfg.DesktopPath)
	deskEntry.SetPlaceHolder("Path to the Signal Desktop executable")

	javaEntry := widget.NewEntry()
	javaEntry.SetText(cfg.JavaHome)
	javaEntry.SetPlaceHolder("Optional — only for JVM builds of signal-cli")

	keepOnline := widget.NewCheck(
		"Keep accounts online in the background", nil)
	keepOnline.SetChecked(cfg.KeepOnline)

	screenSec := widget.NewCheck(
		"Auto-read the QR code by screen capture where supported", nil)
	screenSec.SetChecked(s.store.ScreenSecurityDisabled())
	screenSecHelp := widget.NewLabel(
		"On macOS and Linux, Signal Station reads the linking QR code straight off the screen. " +
			"On Windows 11 this cannot work — Signal hides its window from all screen capture to " +
			"keep chats out of Microsoft Recall, and that is enforced by the system — so Windows " +
			"always uses the phone-camera paste method instead, regardless of this setting.")
	screenSecHelp.Wrapping = fyne.TextWrapWord

	keepHelp := widget.NewLabel(
		"With this on, Signal Station holds a live signal-cli connection for each account the " +
			"whole time it is open, receiving messages in real time so linked Signal Desktop " +
			"windows stay in sync. With it off, no background connection is held. The daily " +
			"export also turns this on, since it needs the message stream.")
	keepHelp.Wrapping = fyne.TextWrapWord

	// --- Daily message export ---
	exportOn := widget.NewCheck("Export messages daily for an AI agent", nil)
	exportOn.SetChecked(cfg.ExportEnabled)

	exportDir := widget.NewEntry()
	exportDir.SetText(cfg.ExportDir)
	exportDir.SetPlaceHolder("Folder for daily bundles (leave blank for the app data folder)")

	exportHelp := widget.NewLabel(
		"Writes one folder per day containing messages.json (structured), transcript.md " +
			"(readable), and an attachments folder with every image — for both messages you " +
			"receive and messages you send from your phone. Point this at a folder your agent " +
			"watches. Turning this on also keeps accounts online so messages are captured.")
	exportHelp.Wrapping = fyne.TextWrapWord

	exportStatus := widget.NewLabel("")
	exportStatus.Wrapping = fyne.TextWrapWord

	exportNow := widget.NewButton("Export now", func() {
		// Persist current export settings first so the build uses them.
		if err := s.store.SetExport(true, exportDir.Text); err != nil {
			s.showError(err)
			return
		}
		exportOn.SetChecked(true)
		exportStatus.SetText("Building today's bundle…")
		go func() {
			cfg := s.store.Config()
			day := time.Now().Local().Format("2006-01-02")
			dir, n, err := BuildDailyBundle(cfg, day)
			fyne.Do(func() {
				if err != nil {
					exportStatus.SetText("Export failed: " + err.Error())
					return
				}
				exportStatus.SetText(fmt.Sprintf("Exported %d messages for %s to:\n%s", n, day, dir))
			})
		}()
	})

	openExport := widget.NewButton("Open export folder", func() {
		openInFileManager(exportBundleRoot(s.store.Config()))
	})

	version := widget.NewLabel("")
	version.Wrapping = fyne.TextWrapWord

	testBtn := widget.NewButton("Test signal-cli", func() {
		probe := &CLI{
			Bin:      resolveBinary(cliEntry.Text),
			DataDir:  signalCLIDataDir(),
			JavaHome: javaEntry.Text,
		}
		version.SetText("Checking…")
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			out, err := probe.Version(ctx)
			fyne.Do(func() {
				method := probe.LaunchMethod()
				if err != nil {
					version.SetText("signal-cli did not run: " + err.Error() +
						"\n\nLaunch method: " + method)
					return
				}
				version.SetText("Working — " + out + "\nLaunch method: " + method)
			})
		}()
	})

	detectBtn := widget.NewButton("Detect automatically", func() {
		cliEntry.SetText(DetectSignalCLI())
		deskEntry.SetText(DetectSignalDesktop())
	})

	form := widget.NewForm(
		widget.NewFormItem("signal-cli", withBrowse(s.win, cliEntry)),
		widget.NewFormItem("Signal Desktop", withBrowse(s.win, deskEntry)),
		widget.NewFormItem("JAVA_HOME", withBrowse(s.win, javaEntry)),
	)

	links := container.NewHBox(
		widget.NewButton("Get signal-cli", func() { s.openURL(SignalCLIReleasesURL) }),
		widget.NewButton("Get Signal Desktop", func() { s.openURL(DesktopDownloadURL) }),
		widget.NewButton("Open data folder", func() { openInFileManager(appDataDir()) }),
	)

	body := container.NewVBox(
		form,
		container.NewHBox(detectBtn, testBtn),
		version,
		widget.NewSeparator(),
		keepOnline,
		keepHelp,
		widget.NewSeparator(),
		exportOn,
		container.NewBorder(nil, nil, nil,
			widget.NewButton("Browse", func() {
				dialog.ShowFolderOpen(func(u fyne.ListableURI, err error) {
					if err != nil || u == nil {
						return
					}
					exportDir.SetText(u.Path())
				}, s.win)
			}), exportDir),
		exportHelp,
		container.NewHBox(exportNow, openExport),
		exportStatus,
		widget.NewSeparator(),
		screenSec,
		screenSecHelp,
		widget.NewSeparator(),
		links,
		widget.NewLabel("Signal Station "+appVersion+" — not affiliated with Signal Messenger LLC"),
	)

	d := dialog.NewCustomConfirm("Settings", "Save", "Cancel",
		container.NewVScroll(body), func(ok bool) {
			if !ok {
				return
			}
			if err := s.store.SetPaths(cliEntry.Text, deskEntry.Text, javaEntry.Text); err != nil {
				s.showError(err)
				return
			}
			if err := s.store.SetKeepOnline(keepOnline.Checked); err != nil {
				s.showError(err)
				return
			}
			if err := s.store.SetScreenSecurityDisabled(screenSec.Checked); err != nil {
				s.showError(err)
				return
			}
			if err := s.store.SetExport(exportOn.Checked, exportDir.Text); err != nil {
				s.showError(err)
				return
			}
			s.cli = NewCLI(s.store.Config())
			s.reconcileDaemons()
			s.startDailyExport()
			s.refreshAll()
		}, s.win)
	d.Resize(fyne.NewSize(680, 560))
	d.Show()
}

// withBrowse pairs a path entry with a file picker, because typing a path into
// a bundle like /Applications/Signal.app/Contents/MacOS/Signal from memory is
// unreasonable.
func withBrowse(win fyne.Window, entry *widget.Entry) fyne.CanvasObject {
	browse := widget.NewButton("Browse", func() {
		dialog.ShowFileOpen(func(rc fyne.URIReadCloser, err error) {
			if err != nil || rc == nil {
				return
			}
			defer rc.Close()
			entry.SetText(rc.URI().Path())
		}, win)
	})
	return container.NewBorder(nil, nil, nil, browse, entry)
}

// openInFileManager reveals a directory in Finder or File Explorer.
func openInFileManager(path string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", path)
	case "windows":
		cmd = exec.Command("explorer.exe", path)
	default:
		cmd = exec.Command("xdg-open", path)
	}
	hideConsole(cmd)
	_ = cmd.Start()
	go func() { _ = cmd.Wait() }()
}
