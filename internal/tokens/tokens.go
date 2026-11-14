// Package tokens manages API tokens for the browser extension. Only a hash of each token is stored; the
// token itself is shown once, when it is created.
package tokens

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"time"

	"synochan/internal/config"
)

// Prefix marks SynoChan tokens, so they are recognisable when pasted somewhere by mistake.
const Prefix = "sc_"

// Token is a stored token, without its secret.
type Token struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Hash     string    `json:"hash,omitempty"`
	Created  time.Time `json:"created"`
	LastUsed time.Time `json:"last_used,omitzero"`
}

// Store keeps the tokens in a JSON file.
type Store struct {
	path string
	mu   sync.Mutex
	list []Token
}

// Open loads the tokens from path. A missing file means no tokens.
func Open(path string) (*Store, error) {
	s := &Store{path: path}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &s.list); err != nil {
		return nil, err
	}
	return s, nil
}

// NewMemory returns a store that never touches the disk.
func NewMemory() *Store { return &Store{} }

func hash(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func randomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Create makes a new token and returns it with its secret, which is not stored and cannot be shown again.
func (s *Store) Create(name string) (Token, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Browser extension"
	}
	if len(name) > 80 {
		name = name[:80]
	}
	id, err := randomString(6)
	if err != nil {
		return Token{}, "", err
	}
	secret, err := randomString(32)
	if err != nil {
		return Token{}, "", err
	}
	secret = Prefix + secret
	t := Token{ID: id, Name: name, Hash: hash(secret), Created: time.Now()}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.list = append(s.list, t)
	if err := s.saveLocked(); err != nil {
		s.list = s.list[:len(s.list)-1]
		return Token{}, "", err
	}
	t.Hash = ""
	return t, secret, nil
}

// Revoke deletes a token. Unknown ids are ignored.
func (s *Store) Revoke(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, t := range s.list {
		if t.ID == id {
			s.list = append(s.list[:i], s.list[i+1:]...)
			return s.saveLocked()
		}
	}
	return nil
}

// List returns the tokens without their hashes.
func (s *Store) List() []Token {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Token, len(s.list))
	for i, t := range s.list {
		t.Hash = ""
		out[i] = t
	}
	return out
}

// Check reports whether secret is a valid token, and records its use.
func (s *Store) Check(secret string) (Token, bool) {
	if !strings.HasPrefix(secret, Prefix) {
		return Token{}, false
	}
	h := hash(secret)
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.list {
		if s.list[i].Hash == h {
			// Saved at most once a minute, so a busy extension does not rewrite the file on every call.
			if time.Since(s.list[i].LastUsed) > time.Minute {
				s.list[i].LastUsed = time.Now()
				s.saveLocked()
			}
			t := s.list[i]
			t.Hash = ""
			return t, true
		}
	}
	return Token{}, false
}

func (s *Store) saveLocked() error {
	if s.path == "" {
		return nil
	}
	data, err := json.MarshalIndent(s.list, "", "  ")
	if err != nil {
		return err
	}
	return config.WriteFileAtomic(s.path, data, 0o600)
}
