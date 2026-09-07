// Package store persists login credentials and long-poll/context-token state on disk,
// so the CLI and the HTTP API server can share the same login session and context tokens.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Credential is the login session persisted after a successful QR login.
type Credential struct {
	Token   string `json:"token"`
	BaseURL string `json:"base_url"`
	BotID   string `json:"bot_id"`
	UserID  string `json:"user_id"`
}

// Store manages the on-disk state directory (default ~/.ilinkgo).
type Store struct {
	dir string
	mu  sync.Mutex
}

// New creates the data directory (0700) and returns a Store.
func New(dir string) (*Store, error) {
	if dir == "" {
		return nil, errors.New("store: empty data dir")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("store: create dir %s: %w", dir, err)
	}
	return &Store{dir: dir}, nil
}

// Dir returns the data directory path.
func (s *Store) Dir() string { return s.dir }

// LoadCredential reads the saved login session. Returns (nil, nil) if none exists yet.
func (s *Store) LoadCredential() (*Credential, error) {
	var c Credential
	ok, err := s.readJSON("credentials.json", &c)
	if err != nil || !ok || c.Token == "" {
		return nil, err
	}
	return &c, nil
}

// SaveCredential writes the login session (0600, contains the bot token).
func (s *Store) SaveCredential(c *Credential) error {
	if c == nil {
		return errors.New("store: nil credential")
	}
	return s.writeJSON("credentials.json", c)
}

// ClearCredential removes the saved session (used by `ilinkgo logout`).
func (s *Store) ClearCredential() error {
	if err := os.Remove(s.path("credentials.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("store: remove credentials: %w", err)
	}
	return nil
}

// LoadSyncBuf returns the saved long-poll cursor, or "" if none exists (start from now).
func (s *Store) LoadSyncBuf() string {
	b, err := os.ReadFile(s.path("sync_buf.dat"))
	if err != nil {
		return ""
	}
	return string(b)
}

// SaveSyncBuf persists the long-poll cursor so `serve` can resume after a restart.
func (s *Store) SaveSyncBuf(buf string) error {
	if err := os.WriteFile(s.path("sync_buf.dat"), []byte(buf), 0o600); err != nil {
		return fmt.Errorf("store: write sync buf: %w", err)
	}
	return nil
}

// LoadContextToken returns the cached context token for a user, persisted by `serve`
// while monitoring inbound messages. Needed so `send`/the API can push a message to a
// user without an explicit --context-token even when `serve` isn't currently running.
func (s *Store) LoadContextToken(userID string) (string, bool) {
	m, err := s.loadContextTokens()
	if err != nil {
		return "", false
	}
	t, ok := m[userID]
	return t, ok && t != ""
}

// SaveContextToken persists a context token for a user.
func (s *Store) SaveContextToken(userID, token string) error {
	if userID == "" || token == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.loadContextTokens()
	if err != nil {
		return err
	}
	m[userID] = token
	return s.writeJSON("context_tokens.json", m)
}

func (s *Store) loadContextTokens() (map[string]string, error) {
	m := map[string]string{}
	ok, err := s.readJSON("context_tokens.json", &m)
	if err != nil {
		return nil, err
	}
	if !ok {
		return map[string]string{}, nil
	}
	return m, nil
}

// LoadDefaultUser returns the persisted default recipient: the first user that ever
// messaged the bot. Returns "" when none has been captured yet.
func (s *Store) LoadDefaultUser() string {
	b, err := os.ReadFile(s.path("default_user"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// SaveDefaultUser persists the default recipient so --to can be omitted later.
func (s *Store) SaveDefaultUser(userID string) error {
	if userID == "" {
		return nil
	}
	if err := os.WriteFile(s.path("default_user"), []byte(userID), 0o600); err != nil {
		return fmt.Errorf("store: write default user: %w", err)
	}
	return nil
}

// LoadAPIToken returns the persisted API bearer token, if one was previously generated.
func (s *Store) LoadAPIToken() (string, bool) {
	b, err := os.ReadFile(s.path("api_token.txt"))
	if err != nil {
		return "", false
	}
	return string(b), true
}

// SaveAPIToken persists the API bearer token (0600) for reuse across `serve` restarts.
func (s *Store) SaveAPIToken(token string) error {
	if err := os.WriteFile(s.path("api_token.txt"), []byte(token), 0o600); err != nil {
		return fmt.Errorf("store: write api token: %w", err)
	}
	return nil
}

func (s *Store) path(name string) string { return filepath.Join(s.dir, name) }

func (s *Store) readJSON(name string, out any) (bool, error) {
	b, err := os.ReadFile(s.path(name))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("store: read %s: %w", name, err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		return false, fmt.Errorf("store: parse %s: %w", name, err)
	}
	return true, nil
}

func (s *Store) writeJSON(name string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("store: marshal %s: %w", name, err)
	}
	if err := os.WriteFile(s.path(name), b, 0o600); err != nil {
		return fmt.Errorf("store: write %s: %w", name, err)
	}
	return nil
}
