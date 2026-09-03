package main

import (
	"context"
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
		"Disable Signal's screen-capture protection (needed to auto-read the QR code)", nil)
	screenSec.SetChecked(s.store.ScreenSecurityDisabled())
	screenSecHelp := widget.NewLabel(
		"On Windows 11, Signal Desktop blocks screen capture by default, which stops Signal " +
			"Station from reading the linking QR code. With this on, Signal Station turns that " +
			"protection off in each profile it creates so linking can be automatic. It applies " +
			"to profiles Signal Station manages; you can still toggle it inside Signal at any " +
			"time under Settings, Privacy, Screen security.")
	screenSecHelp.Wrapping = fyne.TextWrapWord

	keepHelp := widget.NewLabel(
		"Signal expects a primary device to check in regularly. With this on, Signal Station " +
			"fetches messages for each registered account every few minutes so linked Signal " +
			"Desktop windows stay in sync.")
	keepHelp.Wrapping = fyne.TextWrapWord

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
			s.cli = NewCLI(s.store.Config())
			s.startKeepOnline()
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
