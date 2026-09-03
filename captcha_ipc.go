package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fyne.io/fyne/v2"
)

// The signalcaptcha:// handler and the main app are separate processes: the
// browser launches a fresh Signal Station to handle the link, but the user's
// real window is a different, already-running process. They communicate through
// a small token file in the app data directory.
//
// A file drop is used rather than a socket because it needs no port, no
// permission prompt, and no platform-specific IPC. The design is deliberately
// dumb so it cannot fail in subtle ways: the handler process ALWAYS writes the
// token and exits; the running instance ALWAYS polls for it. There is no
// "is another instance alive" guess in the delivery path, because that guess was
// a source of silent failures.

func captchaInboxPath() string {
	return filepath.Join(appDataDir(), "captcha-inbox.token")
}

// protocolLogPath records every protocol-handler launch, so a failing handoff
// can be diagnosed from disk rather than guessed at.
func protocolLogPath() string {
	return filepath.Join(logDirPath(), "protocol.log")
}

func logProtocol(format string, args ...any) {
	_ = ensureDirs()
	f, err := os.OpenFile(protocolLogPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s "+format+"\n",
		append([]any{time.Now().Format(time.RFC3339)}, args...)...)
}

// stashCaptchaToken writes a token for the running instance to pick up, written
// atomically so the watcher never reads a half-written file.
func stashCaptchaToken(uri string) error {
	if err := ensureDirs(); err != nil {
		return err
	}
	path := captchaInboxPath()
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := os.WriteFile(tmp, []byte(uri), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// handleProtocolLaunch is called from main when this process was started to
// handle a signalcaptcha:// link. It always writes the token, and returns
// whether the process should exit now (because another instance is already
// running to consume it) or continue starting up (to become that instance).
func handleProtocolLaunch(uri string) (shouldExit bool) {
	logProtocol("handler launched with uri (len=%d)", len(uri))

	if err := stashCaptchaToken(uri); err != nil {
		logProtocol("stash failed: %v", err)
		// Could not write the token; the only hope is to start up and show the
		// dialog so the user can paste. Do not exit.
		return false
	}
	logProtocol("token stashed at %s", captchaInboxPath())

	if instanceIsRunning() {
		logProtocol("running instance detected; exiting, it will consume the token")
		return true
	}
	logProtocol("no running instance; this process will start and consume the token")
	return false
}

// --- single-instance liveness -----------------------------------------------

func livenessPath() string {
	return filepath.Join(appDataDir(), "instance.alive")
}

// livenessTTL is how recently the heartbeat must have been touched for the
// instance to count as alive.
const livenessTTL = 10 * time.Second

func instanceIsRunning() bool {
	info, err := os.Stat(livenessPath())
	if err != nil {
		return false
	}
	return time.Since(info.ModTime()) < livenessTTL
}

func touchLiveness() {
	_ = ensureDirs()
	path := livenessPath()
	now := time.Now()
	if err := os.Chtimes(path, now, now); err != nil {
		if f, cerr := os.Create(path); cerr == nil {
			_ = f.Close()
		}
	}
}

// --- watcher -----------------------------------------------------------------

// startCaptchaWatch refreshes this instance's liveness heartbeat and polls for
// captcha tokens delivered by browser launches.
func (s *Station) startCaptchaWatch() {
	if s.captchaCancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.captchaCancel = cancel

	touchLiveness()
	logProtocol("watcher started (pid=%d); polling for tokens", os.Getpid())

	// Consume anything already waiting: this instance may itself be the one the
	// browser launched, having stashed a token before the UI came up.
	if uri := consumeCaptchaToken(); uri != "" {
		s.deliverCaptcha(uri)
	}

	go func() {
		heartbeat := time.NewTicker(3 * time.Second)
		poll := time.NewTicker(500 * time.Millisecond)
		defer heartbeat.Stop()
		defer poll.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-heartbeat.C:
				touchLiveness()
			case <-poll.C:
				if uri := consumeCaptchaToken(); uri != "" {
					u := uri
					fyne.Do(func() { s.deliverCaptcha(u) })
				}
			}
		}
	}()
}

func (s *Station) stopCaptchaWatch() {
	if s.captchaCancel != nil {
		s.captchaCancel()
		s.captchaCancel = nil
	}
	_ = os.Remove(livenessPath())
}

// consumeCaptchaToken atomically claims a pending token, returning "" if none.
// Renaming before reading means two rapid deliveries cannot be read twice.
func consumeCaptchaToken() string {
	path := captchaInboxPath()
	claim := fmt.Sprintf("%s.%d.claimed", path, os.Getpid())
	if err := os.Rename(path, claim); err != nil {
		return "" // nothing waiting
	}
	data, err := os.ReadFile(claim)
	_ = os.Remove(claim)
	if err != nil {
		return ""
	}
	uri := strings.TrimSpace(string(data))
	if !strings.HasPrefix(uri, CaptchaScheme+"://") {
		return ""
	}
	logProtocol("token consumed by running instance (len=%d)", len(uri))
	return uri
}

// deliverCaptcha routes an incoming token to a waiting registration. If no
// registration is waiting, it remembers the token and prompts the user to start
// one, then feeds it in when they do.
func (s *Station) deliverCaptcha(uri string) {
	token := normalizeCaptcha(uri)
	if token == "" {
		logProtocol("deliverCaptcha: token empty after normalize; ignoring")
		return
	}
	s.bringToFront()

	if s.captchaPending != nil {
		logProtocol("deliverCaptcha: dialog waiting; delivering to captchaPending")
		cb := s.captchaPending
		s.captchaPending = nil
		cb(uri)
		return
	}

	acct := s.store.Account(s.captchaAcct)
	stage := "<none>"
	if acct != nil {
		stage = string(acct.Stage)
	}
	logProtocol("deliverCaptcha: no dialog waiting; captchaAcct=%q stage=%s", s.captchaAcct, stage)

	// No dialog is open. Reopening the captcha dialog is valid when an account is
	// still stuck before a code has been requested. StageAwaitCode is included
	// because a captcha can be demanded on a resend after the first code request,
	// but StageRegistered and StageLinked never need a captcha.
	if s.captchaAcct != "" && acct != nil &&
		(acct.Stage == StagePending || acct.Stage == StageAwaitCode) {
		logProtocol("deliverCaptcha: reopening captcha dialog for %s", s.captchaAcct)
		s.bufferedCaptcha = uri
		id := s.captchaAcct
		s.setStatus("Captcha received — continuing…")
		s.showCaptcha(id, false)
		return
	}

	// Otherwise the token is not actionable right now. Hold it for the next
	// registration attempt and say so.
	logProtocol("deliverCaptcha: not actionable now; buffering token")
	s.bufferedCaptcha = uri
	s.setStatus("Captcha received. Start registering an account to use it.")
}

// takeBufferedCaptcha returns and clears any token that arrived before a
// registration was waiting for it, so showCaptcha can pick it up immediately.
func (s *Station) takeBufferedCaptcha() string {
	uri := s.bufferedCaptcha
	s.bufferedCaptcha = ""
	return uri
}

// bringToFront raises the window when a captcha arrives from the browser.
func (s *Station) bringToFront() {
	if s.win != nil {
		s.win.Show()
		s.win.RequestFocus()
	}
}
