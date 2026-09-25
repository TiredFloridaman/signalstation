package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// Persistent per-account signal-cli daemons.
//
// While Signal Station is open, each registered/linked account runs its own
// long-lived `signal-cli -a NUMBER jsonRpc` process. That process connects to
// Signal once and streams received messages in real time as JSON-RPC
// notifications, instead of the app polling every few minutes. Messages are
// journaled for the export as they arrive, and group names are refreshed by
// sending listGroups requests over the same process's stdin (signal-cli locks
// the account, so a separate listGroups process cannot run alongside it).

// daemonHandle is the supervisor's control over one account's daemon goroutine.
type daemonHandle struct {
	cancel context.CancelFunc
}

// reconcileDaemons starts a daemon for every registered/linked account that does
// not have one, and stops daemons for accounts that are gone or no longer
// eligible. It runs at startup, on a slow timer, and after settings/account
// changes. Daemons run only when background connection is enabled (keep-online
// or the export), so the feature has an off switch.
func (s *Station) reconcileDaemons() {
	if s.daemonRoot == nil {
		return
	}
	s.daemonMu.Lock()
	defer s.daemonMu.Unlock()
	if s.daemons == nil {
		s.daemons = map[string]*daemonHandle{}
	}

	cfg := s.store.Config()
	enabled := cfg.KeepOnline || cfg.ExportEnabled

	want := map[string]bool{}
	if enabled && s.cli.Available() {
		for _, a := range s.store.Accounts() {
			if a.Stage == StageRegistered || a.Stage == StageLinked {
				want[a.Number] = true
			}
		}
	}

	// Stop daemons that are no longer wanted.
	for num, h := range s.daemons {
		if !want[num] {
			h.cancel()
			delete(s.daemons, num)
		}
	}
	// Start daemons that are wanted but not running.
	for num := range want {
		if _, ok := s.daemons[num]; ok {
			continue
		}
		ctx, cancel := context.WithCancel(s.daemonRoot)
		s.daemons[num] = &daemonHandle{cancel: cancel}
		go s.runDaemon(ctx, num)
	}
}

// runDaemon keeps one account's jsonRpc process alive until its context is
// cancelled, restarting it with capped backoff if it exits or crashes.
func (s *Station) runDaemon(ctx context.Context, number string) {
	backoff := 2 * time.Second
	const maxBackoff = 60 * time.Second

	for {
		if ctx.Err() != nil {
			return
		}
		started := time.Now()
		s.runDaemonOnce(ctx, number)

		if ctx.Err() != nil {
			return
		}
		// If it ran a healthy while before dying, reset backoff; otherwise grow
		// it so a persistently failing account does not spin.
		if time.Since(started) > 2*time.Minute {
			backoff = 2 * time.Second
		}
		logProtocol("daemon %s exited; restarting in %s", shortAcct(number), backoff)
		if !sleepCtx(ctx, backoff) {
			return
		}
		backoff = minDur(backoff*2, maxBackoff)
	}
}

// runDaemonOnce launches the process, streams it until it ends, and returns.
func (s *Station) runDaemonOnce(ctx context.Context, number string) {
	cmd, err := s.cli.jsonRPCCommand(ctx, number)
	if err != nil {
		logProtocol("daemon %s: %v", shortAcct(number), err)
		return
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		logProtocol("daemon %s stdout pipe: %v", shortAcct(number), err)
		return
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		logProtocol("daemon %s stdin pipe: %v", shortAcct(number), err)
		return
	}
	// Keep only the tail of stderr so a long-running process cannot grow it
	// without bound; it is logged if the process dies.
	stderr := &tailWriter{max: 4096}
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		logProtocol("daemon %s start failed: %v", shortAcct(number), err)
		return
	}
	logProtocol("daemon started for %s (pid=%d)", shortAcct(number), cmd.Process.Pid)

	// Periodically ask this process for its groups so group folders get real
	// names. The response arrives on the same stdout stream and is handled by
	// processDaemonLine.
	grpCtx, grpCancel := context.WithCancel(ctx)
	go s.groupRefreshLoop(grpCtx, number, stdin)

	// Read the stream until the process closes it.
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		processDaemonLine(number, sc.Bytes(), s.store.Config().ExportEnabled)
	}

	grpCancel()
	_ = cmd.Wait()
	if tail := stderr.String(); tail != "" {
		logProtocol("daemon %s stderr tail: %s", shortAcct(number), lastMeaningfulLine(tail))
	}
}

// groupRefreshLoop sends a listGroups request shortly after start and then
// periodically, so newly created or renamed groups get their real names.
func (s *Station) groupRefreshLoop(ctx context.Context, number string, stdin io.Writer) {
	if !sleepCtx(ctx, 5*time.Second) {
		return
	}
	for {
		id := atomic.AddInt64(&s.daemonReq, 1)
		req := fmt.Sprintf(`{"jsonrpc":"2.0","method":"listGroups","id":%d}`+"\n", id)
		if _, err := io.WriteString(stdin, req); err != nil {
			return // process is going away; the reader will notice too
		}
		if !sleepCtx(ctx, 15*time.Minute) {
			return
		}
	}
}

// --- small helpers ----------------------------------------------------------

// tailWriter keeps only the last max bytes written, so capturing a long-running
// process's stderr cannot grow without bound.
type tailWriter struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (w *tailWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	if len(w.buf) > w.max {
		w.buf = w.buf[len(w.buf)-w.max:]
	}
	return len(p), nil
}

func (w *tailWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(w.buf)
}

// sleepCtx sleeps for d, returning false if the context is cancelled first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func minDur(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// shortAcct trims an account number for logs (the full number is sensitive and
// already elsewhere); keeps the last 4 digits.
func shortAcct(a string) string {
	if len(a) <= 4 {
		return a
	}
	return "…" + a[len(a)-4:]
}
