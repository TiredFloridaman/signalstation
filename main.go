package main

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"os"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

const appVersion = "1.0.0"

// Station holds everything the UI needs. There is exactly one, created in main.
type Station struct {
	fyneApp fyne.App
	win     fyne.Window
	store   *Store
	cli     *CLI

	selected string // id of the account shown in the detail pane

	listBox   *fyne.Container
	detailBox *fyne.Container
	statusBar *widget.Label
	banner    *fyne.Container

	keepCancel    context.CancelFunc
	captchaCancel context.CancelFunc

	// captchaPending, when set, is a registration waiting for a captcha token.
	// A token arriving from the browser (or a manual paste) is routed here so
	// the flow continues without the user copying anything.
	captchaPending func(uri string)

	// captchaAcct is the account whose registration triggered the current
	// captcha requirement. If a token arrives when no dialog is open, it lets
	// deliverCaptcha reopen the captcha dialog for the right account rather than
	// leaving the user to find it.
	captchaAcct string

	// bufferedCaptcha holds a token that arrived before any registration was
	// waiting for it, so the next captcha dialog can consume it immediately.
	bufferedCaptcha string
}

func main() {
	launchURI := captchaURIFromArgs(os.Args[1:])

	// If launched by the browser to handle a signalcaptcha:// link, hand the
	// token off. handleProtocolLaunch always writes the token to disk; it
	// returns true when an already-running instance will consume it, in which
	// case this process exits without opening a second window.
	if launchURI != "" {
		if handleProtocolLaunch(launchURI) {
			return
		}
		// No instance was running, so this process continues and will consume
		// its own stashed token once its watcher starts.
	} else if instanceIsRunning() {
		// A plain launch while another instance is alive. Two instances would
		// each run a token watcher and race to consume browser-delivered captcha
		// tokens, so a token could be swallowed by whichever instance is not
		// showing the captcha dialog — the "consumed but nothing happens"
		// symptom. Refuse to start a second one.
		log.Println("another Signal Station instance is already running; exiting")
		return
	}

	a := app.NewWithID("org.signalstation.app")
	a.Settings().SetTheme(newStationTheme())

	w := a.NewWindow("Signal Station")
	w.Resize(fyne.NewSize(940, 620))
	w.CenterOnScreen()

	store, err := LoadStore()
	if err != nil {
		// Without a store there is no app, so say what broke and stop.
		log.Println("load store:", err)
		w.SetContent(widget.NewLabel("Could not open the Signal Station data folder:\n\n" + err.Error()))
		w.ShowAndRun()
		return
	}

	s := &Station{fyneApp: a, win: w, store: store}
	s.cli = NewCLI(store.Config())
	s.buildUI()
	s.refreshAll()
	s.startKeepOnline()

	// Register as the signalcaptcha:// handler (idempotent), and start watching
	// for tokens delivered by browser launches.
	if err := registerCaptchaHandler(); err != nil {
		log.Println("register captcha handler:", err)
	}
	s.startCaptchaWatch()

	w.SetCloseIntercept(func() {
		if s.keepCancel != nil {
			s.keepCancel()
		}
		s.stopCaptchaWatch()
		w.Close()
	})

	w.ShowAndRun()
}

// async runs work off the UI goroutine and hands the result back on it.
// Every Fyne call from a background goroutine has to go through fyne.Do.
func (s *Station) async(status string, work func() error, done func(error)) {
	s.setStatus(status)
	go func() {
		err := work()
		fyne.Do(func() {
			s.setStatus("")
			if done != nil {
				done(err)
			}
		})
	}()
}

func (s *Station) setStatus(msg string) {
	if s.statusBar == nil {
		return
	}
	if msg == "" {
		msg = s.idleStatus()
	}
	s.statusBar.SetText(msg)
}

func (s *Station) idleStatus() string {
	n := len(s.store.Config().Accounts)
	switch n {
	case 0:
		return "No accounts yet"
	case 1:
		return "1 account"
	default:
		return fmt.Sprintf("%d accounts", n)
	}
}

func (s *Station) showError(err error) {
	if err == nil {
		return
	}
	dialog.ShowError(err, s.win)
}

func (s *Station) openURL(raw string) {
	u, err := url.Parse(raw)
	if err != nil {
		s.showError(err)
		return
	}
	if err := s.fyneApp.OpenURL(u); err != nil {
		s.showError(err)
	}
}

// startKeepOnline periodically drains messages for every registered account.
//
// Signal expects a primary device to come online regularly. Because signal-cli
// is the primary here and it only runs when invoked, nothing would ever fetch
// messages between manual actions, and delivery to the linked Signal Desktop
// profiles degrades. A slow poll keeps each account healthy without hammering
// the service.
func (s *Station) startKeepOnline() {
	if s.keepCancel != nil {
		s.keepCancel()
		s.keepCancel = nil
	}
	if !s.store.Config().KeepOnline {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.keepCancel = cancel

	go func() {
		ticker := time.NewTicker(4 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			for _, acct := range s.store.Accounts() {
				if acct.Stage != StageRegistered && acct.Stage != StageLinked {
					continue
				}
				runCtx, cancelRun := context.WithTimeout(ctx, 45*time.Second)
				_ = s.cli.Receive(runCtx, acct.Number, 8*time.Second)
				cancelRun()
			}
		}
	}()
}
