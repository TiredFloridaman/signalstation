package main

import (
	"context"
	"errors"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

// showAddAccount collects a name and phone number, then creates the account
// record. Registration is a separate, explicit step because it is destructive:
// it moves the number's primary device to signal-cli.
func (s *Station) showAddAccount() {
	label := widget.NewEntry()
	label.SetPlaceHolder("Work, Personal, Press desk…")

	number := widget.NewEntry()
	number.SetPlaceHolder("+14155550123")
	number.Validator = func(v string) error { return validateNumber(v) }

	warn := widget.NewLabel(
		"Signal allows one account per phone number, and one primary device per account. " +
			"Registering a number here makes signal-cli its primary device, which signs that " +
			"number out of the Signal app on a phone if one is using it. Use a number you are " +
			"not running Signal on elsewhere.")
	warn.Wrapping = fyne.TextWrapWord

	form := widget.NewForm(
		widget.NewFormItem("Name", label),
		widget.NewFormItem("Phone number", number),
	)

	d := dialog.NewCustomConfirm("Add account", "Add", "Cancel",
		container.NewVBox(form, widget.NewSeparator(), warn),
		func(ok bool) {
			if !ok {
				return
			}
			acct, err := s.store.AddAccount(label.Text, number.Text)
			if err != nil {
				s.showError(err)
				return
			}
			s.selected = acct.ID
			s.refreshAll()
			s.startRegistration(acct.ID)
		}, s.win)
	d.Resize(fyne.NewSize(520, 340))
	d.Show()
}

// startRegistration asks the Signal server to send a verification code.
//
// The first attempt goes out without a captcha token. The server usually
// demands one, and tokens expire in a couple of minutes, so it is better to
// fetch a fresh token in response to that demand than to make the user solve a
// captcha up front and race the clock.
func (s *Station) startRegistration(id string) {
	acct := s.store.Account(id)
	if acct == nil {
		return
	}
	if !s.cli.Available() {
		s.showError(errors.New("signal-cli was not found. Set its location in Settings"))
		return
	}
	s.register(id, "", false)
}

func (s *Station) register(id, captcha string, voice bool) {
	acct := s.store.Account(id)
	if acct == nil {
		return
	}
	status := "Requesting a verification code by SMS…"
	if voice {
		status = "Requesting a verification call…"
	}

	s.async(status, func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		return s.cli.Register(ctx, acct.Number, captcha, voice)
	}, func(err error) {
		switch {
		case err == nil:
			// Registration accepted; we are past the captcha entirely. Clear all
			// captcha state so a late or buffered token cannot reopen the captcha
			// dialog on top of the verification step and re-trigger register().
			s.captchaAcct = ""
			s.captchaPending = nil
			s.bufferedCaptcha = ""
			_ = s.store.UpdateAccount(id, func(a *Account) { a.Stage = StageAwaitCode })
			s.refreshAll()
			s.showVerify(id)
		case errors.Is(err, ErrCaptchaRequired):
			s.showCaptcha(id, voice)
		case errors.Is(err, ErrRateLimited):
			// Do not leave captcha state armed after a rate-limit, or an arriving
			// token would immediately retry and extend the limit.
			s.captchaAcct = ""
			s.bufferedCaptcha = ""
			dialog.ShowInformation("Too many attempts",
				"Signal is rate limiting registrations for this number. This clears on its "+
					"own; try again in a few hours. Retrying now will extend the wait.", s.win)
		default:
			s.captchaAcct = ""
			s.bufferedCaptcha = ""
			s.showError(err)
		}
	})
}

// showCaptcha walks the user through fetching a token. Signal's captcha page
// ends with an "Open Signal" link targeting signalcaptcha://<token>. Because
// Signal Station registers itself as the handler for that scheme, the user can
// simply click it: the token is delivered back to this dialog automatically. The
// manual copy-paste remains as a fallback for when the handler is not registered
// (e.g. running as a bare binary, or the browser blocks the launch).
func (s *Station) showCaptcha(id string, voice bool) {
	acct := s.store.Account(id)
	if acct == nil {
		return
	}

	// Remember this account so a token clicked in the browser can reopen this
	// dialog if it is closed, or was never opened, when the token arrives.
	s.captchaAcct = id

	steps := widget.NewLabel(
		"Signal wants a captcha before it will send a code.\n\n" +
			"1. Open the captcha page and solve it.\n" +
			"2. When the \"Open Signal\" link appears, just click it. Signal Station will catch " +
			"the result and continue automatically.\n\n" +
			"If clicking does nothing, right-click the link, copy the link address, and paste it " +
			"below instead. Tokens expire after a couple of minutes, so be prompt.")
	steps.Wrapping = fyne.TextWrapWord

	openBtn := widget.NewButton("Open captcha page", func() { s.openURL(CaptchaURL) })
	openBtn.Importance = widget.HighImportance

	token := widget.NewMultiLineEntry()
	token.SetPlaceHolder("signalcaptcha://signal-hcaptcha…  (fills in automatically when you click the link)")
	token.Wrapping = fyne.TextWrapBreak

	useVoice := widget.NewCheck("Send the code by phone call instead of SMS", nil)
	useVoice.SetChecked(voice)

	waiting := widget.NewLabel("Waiting for you to solve the captcha…")
	waiting.Wrapping = fyne.TextWrapWord

	var d *dialog.CustomDialog
	done := false

	proceed := func(captcha string) {
		if done {
			return
		}
		done = true
		s.captchaPending = nil
		s.captchaAcct = "" // registration is proceeding; no reopen needed
		if d != nil {
			d.Hide()
		}
		s.register(id, captcha, useVoice.Checked)
	}

	// Register a pending handler so a clicked signalcaptcha:// link — delivered
	// through the OS scheme handler and the token watcher — lands straight here.
	s.captchaPending = func(uri string) {
		token.SetText(uri)
		waiting.SetText("Captcha received — continuing…")
		proceed(uri)
	}

	// If a token already arrived before this dialog opened (the user clicked the
	// link first), it is valid and ready — use it immediately rather than making
	// the user press Continue.
	if buffered := s.takeBufferedCaptcha(); buffered != "" {
		token.SetText(buffered)
		waiting.SetText("Captcha received — continuing…")
		proceed(buffered)
	}

	continueBtn := widget.NewButton("Continue", func() {
		if normalizeCaptcha(token.Text) == "" {
			s.showError(errors.New("solve the captcha and click Open Signal, or paste the signalcaptcha:// link"))
			return
		}
		proceed(token.Text)
	})
	continueBtn.Importance = widget.HighImportance

	cancelBtn := widget.NewButton("Cancel", func() {
		done = true
		s.captchaPending = nil
		s.captchaAcct = "" // user gave up; do not reopen on a late token
		if d != nil {
			d.Hide()
		}
	})

	body := container.NewVBox(
		steps, openBtn, widget.NewSeparator(),
		token, useVoice, waiting,
		container.NewHBox(continueBtn, cancelBtn),
	)

	d = dialog.NewCustomWithoutButtons("Captcha needed", body, s.win)
	d.SetOnClosed(func() { s.captchaPending = nil })
	d.Resize(fyne.NewSize(560, 500))
	d.Show()
}

// showVerify completes registration with the code Signal sent.
func (s *Station) showVerify(id string) {
	acct := s.store.Account(id)
	if acct == nil {
		return
	}

	// We are past the captcha. Make sure no captcha state remains armed, so an
	// incoming or buffered token cannot reopen the captcha dialog over this one
	// and restart registration.
	s.captchaAcct = ""
	s.captchaPending = nil
	s.bufferedCaptcha = ""

	intro := widget.NewLabel("Enter the code Signal sent to " + acct.Number + ".")
	intro.Wrapping = fyne.TextWrapWord

	code := widget.NewEntry()
	code.SetPlaceHolder("123456")

	pin := widget.NewPasswordEntry()
	pin.SetPlaceHolder("Only if this number has a registration lock")

	resend := widget.NewButton("Send the code again", func() { s.register(id, "", false) })
	resend.Importance = widget.LowImportance

	callInstead := widget.NewButton("Call me with the code instead", func() { s.register(id, "", true) })
	callInstead.Importance = widget.LowImportance

	form := widget.NewForm(
		widget.NewFormItem("Verification code", code),
		widget.NewFormItem("Registration PIN", pin),
	)

	body := container.NewVBox(intro, form, widget.NewSeparator(),
		container.NewHBox(resend, callInstead))

	d := dialog.NewCustomConfirm("Verify "+acct.Label, "Verify", "Later", body, func(ok bool) {
		if !ok {
			return
		}
		s.async("Verifying…", func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			return s.cli.Verify(ctx, acct.Number, code.Text, pin.Text)
		}, func(err error) {
			if err != nil {
				s.showError(err)
				return
			}
			_ = s.store.UpdateAccount(id, func(a *Account) { a.Stage = StageRegistered })
			s.refreshAll()
			s.offerLink(id)
		})
	}, s.win)
	d.Resize(fyne.NewSize(520, 400))
	d.Show()
}

func (s *Station) offerLink(id string) {
	acct := s.store.Account(id)
	if acct == nil {
		return
	}
	dialog.ShowConfirm("Registered",
		acct.Label+" is registered. Link a Signal Desktop profile to it now?",
		func(yes bool) {
			if yes {
				s.linkDesktop(id)
			}
		}, s.win)
}
