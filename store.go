package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Stage tracks how far an account has got through setup, so the UI can show the
// one action that makes sense next instead of a wall of buttons.
type Stage string

const (
	StagePending    Stage = "pending"    // number entered, registration not started
	StageAwaitCode  Stage = "await_code" // register sent, waiting on SMS/voice code
	StageRegistered Stage = "registered" // signal-cli is primary for this number
	StageLinked     Stage = "linked"     // Signal Desktop profile linked and usable
)

func (s Stage) Label() string {
	switch s {
	case StagePending:
		return "Not registered"
	case StageAwaitCode:
		return "Waiting for code"
	case StageRegistered:
		return "Desktop not linked"
	case StageLinked:
		return "Ready"
	default:
		return string(s)
	}
}

type Account struct {
	ID         string    `json:"id"`
	Label      string    `json:"label"`
	Number     string    `json:"number"`
	ProfileDir string    `json:"profile_dir"`
	Stage      Stage     `json:"stage"`
	Created    time.Time `json:"created"`
	LastOpened time.Time `json:"last_opened,omitempty"`
}

type Config struct {
	SignalCLIPath string `json:"signal_cli_path"`
	DesktopPath   string `json:"desktop_path"`
	JavaHome      string `json:"java_home,omitempty"`
	KeepOnline    bool   `json:"keep_online"`
	// DisableScreenSecurity, when true (the default), writes contentProtection
	// off in each profile so the linking QR is screen-readable on Windows 11.
	DisableScreenSecurity *bool      `json:"disable_screen_security,omitempty"`
	Accounts              []*Account `json:"accounts"`
}

type Store struct {
	mu   sync.RWMutex
	cfg  Config
	path string
}

func LoadStore() (*Store, error) {
	if err := ensureDirs(); err != nil {
		return nil, fmt.Errorf("create app directories: %w", err)
	}
	s := &Store{path: configFilePath()}

	data, err := os.ReadFile(s.path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		s.cfg = Config{Accounts: []*Account{}}
	case err != nil:
		return nil, fmt.Errorf("read %s: %w", s.path, err)
	default:
		if err := json.Unmarshal(data, &s.cfg); err != nil {
			return nil, fmt.Errorf("parse %s: %w", s.path, err)
		}
	}
	if s.cfg.Accounts == nil {
		s.cfg.Accounts = []*Account{}
	}

	// Fill in tool paths on first run, or if a previously saved path has since
	// disappeared (app uninstalled, Homebrew cleanup, and so on).
	if !fileExists(s.cfg.SignalCLIPath) {
		s.cfg.SignalCLIPath = DetectSignalCLI()
	}
	if !fileExists(s.cfg.DesktopPath) {
		s.cfg.DesktopPath = DetectSignalDesktop()
	}
	return s, s.save()
}

func (s *Store) save() error {
	data, err := json.MarshalIndent(s.cfg, "", "  ")
	if err != nil {
		return err
	}
	// Write to a temp file in the same directory, then rename, so a crash
	// mid-write can never leave a truncated account list behind.
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.save()
}

func (s *Store) Config() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// ScreenSecurityDisabled reports the effective setting, defaulting to true
// (protection off, QR readable) when the user has never set it.
func (s *Store) ScreenSecurityDisabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.cfg.DisableScreenSecurity == nil {
		return true
	}
	return *s.cfg.DisableScreenSecurity
}

func (s *Store) SetScreenSecurityDisabled(v bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.DisableScreenSecurity = &v
	return s.save()
}

func (s *Store) SetPaths(signalCLI, desktop, javaHome string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.SignalCLIPath = signalCLI
	s.cfg.DesktopPath = desktop
	s.cfg.JavaHome = javaHome
	return s.save()
}

func (s *Store) SetKeepOnline(v bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.KeepOnline = v
	return s.save()
}

// Accounts returns a copy of the account list, newest activity first.
func (s *Store) Accounts() []*Account {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Account, len(s.cfg.Accounts))
	copy(out, s.cfg.Accounts)
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Created.Before(out[j].Created)
	})
	return out
}

func (s *Store) Account(id string) *Account {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, a := range s.cfg.Accounts {
		if a.ID == id {
			return a
		}
	}
	return nil
}

// NumberInUse guards against two accounts pointing at the same phone number,
// which would give them the same signal-cli identity.
func (s *Store) NumberInUse(number string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, a := range s.cfg.Accounts {
		if strings.EqualFold(a.Number, number) {
			return true
		}
	}
	return false
}

func (s *Store) AddAccount(label, number string) (*Account, error) {
	number = strings.TrimSpace(number)
	label = strings.TrimSpace(label)
	if label == "" {
		label = number
	}
	if err := validateNumber(number); err != nil {
		return nil, err
	}
	if s.NumberInUse(number) {
		return nil, fmt.Errorf("%s is already set up in Signal Station", number)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	id := fmt.Sprintf("%s-%d", slugify(label), time.Now().UnixNano()%100000)
	acct := &Account{
		ID:         id,
		Label:      label,
		Number:     number,
		ProfileDir: filepath.Join(profilesDir(), slugify(label)+"-"+slugify(number)),
		Stage:      StagePending,
		Created:    time.Now(),
	}
	if err := os.MkdirAll(acct.ProfileDir, 0o700); err != nil {
		return nil, fmt.Errorf("create profile folder: %w", err)
	}
	s.cfg.Accounts = append(s.cfg.Accounts, acct)
	return acct, s.save()
}

func (s *Store) UpdateAccount(id string, fn func(*Account)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.cfg.Accounts {
		if a.ID == id {
			fn(a)
			return s.save()
		}
	}
	return fmt.Errorf("account %s not found", id)
}

// RemoveAccount drops the account from the list. deleteData also erases the
// Signal Desktop profile on disk; the signal-cli registration is left alone
// because unregistering is a separate, destructive server-side action.
func (s *Store) RemoveAccount(id string, deleteData bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, a := range s.cfg.Accounts {
		if a.ID != id {
			continue
		}
		if deleteData && a.ProfileDir != "" && strings.HasPrefix(a.ProfileDir, profilesDir()) {
			_ = os.RemoveAll(a.ProfileDir)
		}
		s.cfg.Accounts = append(s.cfg.Accounts[:i], s.cfg.Accounts[i+1:]...)
		return s.save()
	}
	return fmt.Errorf("account %s not found", id)
}

// validateNumber checks the E.164 shape signal-cli expects: a leading +, then
// 7 to 15 digits. It deliberately does not try to validate country codes.
func validateNumber(number string) error {
	if !strings.HasPrefix(number, "+") {
		return errors.New("phone number must start with a country code, like +14155550123")
	}
	digits := number[1:]
	if len(digits) < 7 || len(digits) > 15 {
		return errors.New("phone number should be 7 to 15 digits after the +")
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return errors.New("phone number may contain only digits after the +")
		}
	}
	return nil
}
