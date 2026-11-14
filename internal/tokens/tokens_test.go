package tokens

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateCheckRevokePersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	tok, secret, err := s.Create(" Chrome ")
	if err != nil || !strings.HasPrefix(secret, Prefix) || tok.Name != "Chrome" || tok.Hash != "" {
		t.Fatalf("create: %+v %q %v", tok, secret, err)
	}
	if _, ok := s.Check(secret); !ok {
		t.Fatal("valid token rejected")
	}
	for _, bad := range []string{"", "sc_nope", secret + "x", strings.TrimPrefix(secret, Prefix)} {
		if _, ok := s.Check(bad); ok {
			t.Fatalf("accepted %q", bad)
		}
	}
	again, _ := Open(path)
	if _, ok := again.Check(secret); !ok || len(again.List()) != 1 || again.List()[0].LastUsed.IsZero() {
		t.Fatalf("not persisted: %+v", again.List())
	}
	if err := again.Revoke(tok.ID); err != nil {
		t.Fatal(err)
	}
	third, _ := Open(path)
	if _, ok := third.Check(secret); ok {
		t.Fatal("revoked token still valid")
	}
}
