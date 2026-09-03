package main

import (
	"context"
	"fmt"
	"image/color"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

func (s *Station) buildUI() {
	s.listBox = container.NewVBox()
	s.detailBox = container.NewVBox()
	s.statusBar = widget.NewLabel("")
	s.banner = container.NewVBox()

	title := canvas.NewText("Signal Station", colText)
	title.TextSize = 18
	title.TextStyle = fyne.TextStyle{Bold: true}

	settingsBtn := widget.NewButtonWithIcon("Settings", theme.SettingsIcon(), s.showSettings)
	settingsBtn.Importance = widget.LowImportance

	header := container.NewBorder(nil, nil,
		container.NewPadded(title),
		container.NewHBox(settingsBtn),
	)

	addBtn := widget.NewButtonWithIcon("Add account", theme.ContentAddIcon(), s.showAddAccount)
	addBtn.Importance = widget.HighImportance

	left := container.NewBorder(
		container.NewPadded(sectionLabel("Accounts")),
		container.NewPadded(addBtn),
		nil, nil,
		container.NewVScroll(container.NewPadded(s.listBox)),
	)

	right := container.NewVScroll(container.NewPadded(s.detailBox))

	split := container.NewHSplit(left, right)
	split.Offset = 0.34

	s.win.SetContent(container.NewBorder(
		container.NewVBox(header, widget.NewSeparator(), s.banner),
		container.NewVBox(widget.NewSeparator(), container.NewPadded(s.statusBar)),
		nil, nil,
		split,
	))
}

func sectionLabel(text string) *canvas.Text {
	t := canvas.NewText(text, colMuted)
	t.TextSize = 11.5
	return t
}

func (s *Station) refreshAll() {
	s.refreshBanner()
	s.refreshList()
	s.refreshDetail()
	s.setStatus("")
}

// refreshBanner surfaces missing prerequisites at the top of the window. Signal
// Station cannot do anything useful without both tools, so this is stated once,
// prominently, with the fix attached rather than as an error on every action.
func (s *Station) refreshBanner() {
	s.banner.RemoveAll()
	cfg := s.store.Config()

	missing := ""
	action := func() {}
	switch {
	case !fileExists(cfg.SignalCLIPath) && !fileExists(cfg.DesktopPath):
		missing = "signal-cli and Signal Desktop are both missing. Signal Station needs them to run."
		action = s.showSettings
	case !fileExists(cfg.SignalCLIPath):
		missing = "signal-cli was not found. Install it, or point Signal Station at it in Settings."
		action = s.showSettings
	case !fileExists(cfg.DesktopPath):
		missing = "Signal Desktop was not found. Install it, or point Signal Station at it in Settings."
		action = s.showSettings
	}
	if missing == "" {
		s.banner.Refresh()
		return
	}

	msg := canvas.NewText(missing, colWarn)
	msg.TextSize = 12.5
	fix := widget.NewButton("Open settings", action)
	fix.Importance = widget.LowImportance

	bg := canvas.NewRectangle(color.NRGBA{R: 0xC9, G: 0xA2, B: 0x27, A: 0x1E})
	row := container.NewBorder(nil, nil, nil, fix, container.NewPadded(msg))
	s.banner.Add(container.NewStack(bg, container.NewPadded(row)))
	s.banner.Refresh()
}

// refreshList rebuilds the account rows. The list is short by nature (Signal
// ties one account to one phone number), so rebuilding wholesale is cheaper to
// reason about than incremental updates.
func (s *Station) refreshList() {
	s.listBox.RemoveAll()

	accounts := s.store.Accounts()
	if len(accounts) == 0 {
		hint := widget.NewLabel("Add your first account to get started.")
		hint.Wrapping = fyne.TextWrapWord
		s.listBox.Add(container.NewPadded(hint))
		s.listBox.Refresh()
		return
	}

	for _, acct := range accounts {
		s.listBox.Add(s.accountRow(acct))
	}
	s.listBox.Refresh()
}

func (s *Station) accountRow(acct *Account) fyne.CanvasObject {
	name := canvas.NewText(acct.Label, colText)
	name.TextSize = 14
	name.TextStyle = fyne.TextStyle{Bold: true}

	sub := canvas.NewText(acct.Number+"  ·  "+acct.Stage.Label(), stageColor(acct.Stage))
	sub.TextSize = 11.5

	info := container.NewVBox(name, sub)

	// The row itself selects; the button on the right is the one-click launch
	// the whole app exists for.
	var trailing fyne.CanvasObject
	if acct.Stage == StageLinked {
		open := widget.NewButtonWithIcon("Open", theme.NavigateNextIcon(), func() {
			s.openAccount(acct.ID)
		})
		open.Importance = widget.HighImportance
		trailing = open
	} else {
		cont := widget.NewButton("Continue", func() {
			s.selected = acct.ID
			s.refreshDetail()
			s.continueSetup(acct.ID)
		})
		cont.Importance = widget.MediumImportance
		trailing = cont
	}

	fill := color.NRGBA{R: 0x23, G: 0x27, B: 0x2A, A: 0xFF}
	if acct.ID == s.selected {
		fill = color.NRGBA{R: 0x7F, G: 0xA9, B: 0x8C, A: 0x2E}
	}
	bg := canvas.NewRectangle(fill)
	bg.CornerRadius = 6

	content := container.NewBorder(nil, nil, nil, trailing, container.NewPadded(info))
	return newTappableBox(container.NewStack(bg, container.NewPadded(content)), func() {
		s.selected = acct.ID
		s.refreshList()
		s.refreshDetail()
	})
}

func stageColor(st Stage) color.Color {
	switch st {
	case StageLinked:
		return colAccent
	case StageRegistered, StageAwaitCode:
		return colWarn
	default:
		return colMuted
	}
}

// refreshDetail redraws the right-hand pane for whatever is selected.
func (s *Station) refreshDetail() {
	s.detailBox.RemoveAll()
	defer s.detailBox.Refresh()

	acct := s.store.Account(s.selected)
	if acct == nil {
		intro := widget.NewLabel(
			"Signal Station keeps each Signal account in its own Signal Desktop profile.\n\n" +
				"Adding an account registers its phone number through signal-cli, then links a " +
				"fresh Signal Desktop profile to it. After that, one click opens that account.")
		intro.Wrapping = fyne.TextWrapWord
		s.detailBox.Add(container.NewPadded(intro))
		return
	}

	head := canvas.NewText(acct.Label, colText)
	head.TextSize = 20
	head.TextStyle = fyne.TextStyle{Bold: true}

	status := canvas.NewText(acct.Stage.Label(), stageColor(acct.Stage))
	status.TextSize = 12.5

	s.detailBox.Add(container.NewPadded(head))
	s.detailBox.Add(container.NewPadded(status))
	s.detailBox.Add(container.NewPadded(widget.NewSeparator()))

	s.detailBox.Add(detailRow("Phone number", acct.Number))
	s.detailBox.Add(detailRow("Desktop profile", acct.ProfileDir))
	if !acct.LastOpened.IsZero() {
		s.detailBox.Add(detailRow("Last opened", acct.LastOpened.Format("2 Jan 2006, 15:04")))
	}
	s.detailBox.Add(container.NewPadded(widget.NewSeparator()))

	s.detailBox.Add(container.NewPadded(sectionLabel("Next step")))
	s.detailBox.Add(container.NewPadded(s.primaryAction(acct)))

	s.detailBox.Add(container.NewPadded(widget.NewSeparator()))
	s.detailBox.Add(container.NewPadded(sectionLabel("Also available")))

	tools := container.NewVBox()
	if acct.Stage == StageRegistered || acct.Stage == StageLinked {
		tools.Add(widget.NewButton("Show linked devices", func() { s.showDevices(acct.ID) }))
		tools.Add(widget.NewButton("Send contacts to linked devices", func() { s.sendContacts(acct.ID) }))
		tools.Add(widget.NewButton("Link another Signal Desktop profile", func() { s.linkDesktop(acct.ID) }))
	}
	if acct.Stage == StageLinked {
		tools.Add(widget.NewButton("Open Signal Desktop", func() { s.openAccount(acct.ID) }))
	}
	remove := widget.NewButton("Remove from Signal Station", func() { s.confirmRemove(acct.ID) })
	remove.Importance = widget.DangerImportance
	tools.Add(remove)
	s.detailBox.Add(container.NewPadded(tools))
}

func detailRow(label, value string) fyne.CanvasObject {
	l := canvas.NewText(label, colMuted)
	l.TextSize = 11.5

	v := widget.NewLabel(value)
	v.Wrapping = fyne.TextWrapBreak
	v.TextStyle = fyne.TextStyle{Monospace: true}

	return container.NewPadded(container.NewVBox(l, v))
}

// primaryAction returns the single button that moves this account forward.
// Showing one obvious next step is the whole point: the alternative is the
// sequence of signal-cli invocations this app exists to replace.
func (s *Station) primaryAction(acct *Account) fyne.CanvasObject {
	var btn *widget.Button
	var note string

	switch acct.Stage {
	case StagePending:
		note = "Register this number with signal-cli. You will need to receive an SMS or call on it."
		btn = widget.NewButton("Register "+acct.Number, func() { s.startRegistration(acct.ID) })
	case StageAwaitCode:
		note = "Signal sent a verification code to this number. Enter it to finish registering."
		btn = widget.NewButton("Enter verification code", func() { s.showVerify(acct.ID) })
	case StageRegistered:
		note = "Link a Signal Desktop profile so you can read and write messages in a window."
		btn = widget.NewButton("Link Signal Desktop", func() { s.linkDesktop(acct.ID) })
	case StageLinked:
		note = "This account is ready."
		btn = widget.NewButton("Open Signal", func() { s.openAccount(acct.ID) })
	default:
		// Reached only if a newer build wrote a stage this one does not know.
		note = "This account is in a state Signal Station does not recognise. " +
			"Remove it and add it again."
		btn = widget.NewButton("Remove account", func() { s.confirmRemove(acct.ID) })
	}
	btn.Importance = widget.HighImportance

	n := widget.NewLabel(note)
	n.Wrapping = fyne.TextWrapWord
	return container.NewVBox(n, btn)
}

// openAccount launches Signal Desktop against this account's profile directory.
func (s *Station) openAccount(id string) {
	acct := s.store.Account(id)
	if acct == nil {
		return
	}
	cfg := s.store.Config()

	if err := LaunchDesktop(cfg.DesktopPath, acct.ProfileDir); err != nil {
		s.showError(err)
		return
	}
	_ = s.store.UpdateAccount(id, func(a *Account) { a.LastOpened = time.Now() })
	s.setStatus("Opened " + acct.Label)
	s.refreshList()
	s.refreshDetail()
}

// continueSetup jumps straight to whatever step this account is waiting on.
func (s *Station) continueSetup(id string) {
	acct := s.store.Account(id)
	if acct == nil {
		return
	}
	switch acct.Stage {
	case StagePending:
		s.startRegistration(id)
	case StageAwaitCode:
		s.showVerify(id)
	case StageRegistered:
		s.linkDesktop(id)
	case StageLinked:
		s.openAccount(id)
	}
}

func (s *Station) showDevices(id string) {
	acct := s.store.Account(id)
	if acct == nil {
		return
	}
	out := widget.NewMultiLineEntry()
	out.SetText("Loading…")
	out.Wrapping = fyne.TextWrapWord

	d := dialog.NewCustom("Linked devices — "+acct.Label, "Close",
		container.NewVScroll(out), s.win)
	d.Resize(fyne.NewSize(620, 380))
	d.Show()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		text, err := s.cli.ListDevices(ctx, acct.Number)
		fyne.Do(func() {
			if err != nil {
				out.SetText("Could not list devices:\n\n" + err.Error())
				return
			}
			out.SetText(text)
		})
	}()
}

func (s *Station) sendContacts(id string) {
	acct := s.store.Account(id)
	if acct == nil {
		return
	}
	s.async("Sending contacts to linked devices…", func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		return s.cli.SendContacts(ctx, acct.Number)
	}, func(err error) {
		if err != nil {
			s.showError(err)
			return
		}
		dialog.ShowInformation("Contacts sent",
			"Linked devices for "+acct.Label+" will pick up the contact list shortly.", s.win)
	})
}

func (s *Station) confirmRemove(id string) {
	acct := s.store.Account(id)
	if acct == nil {
		return
	}
	deleteData := widget.NewCheck("Also delete this account's Signal Desktop profile from disk", nil)
	explain := widget.NewLabel(fmt.Sprintf(
		"Removing %s takes it out of Signal Station.\n\n"+
			"The number stays registered with Signal. To give it up entirely, unregister it "+
			"with signal-cli separately.", acct.Label))
	explain.Wrapping = fyne.TextWrapWord

	body := container.NewVBox(explain, deleteData)
	d := dialog.NewCustomConfirm("Remove account", "Remove", "Keep", body, func(ok bool) {
		if !ok {
			return
		}
		if err := s.store.RemoveAccount(id, deleteData.Checked); err != nil {
			s.showError(err)
			return
		}
		if s.selected == id {
			s.selected = ""
		}
		s.refreshAll()
	}, s.win)
	d.Resize(fyne.NewSize(480, 260))
	d.Show()
}
