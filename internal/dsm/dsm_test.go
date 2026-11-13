package dsm

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"synochan/internal/server"
)

func TestInGroup(t *testing.T) {
	f := filepath.Join(t.TempDir(), "group")
	os.WriteFile(f, []byte("users:x:100:\nadministrators:x:101:admin,Jiri\nhttp:x:1023:\n"), 0o644)
	for user, want := range map[string]bool{"admin": true, "jiri": true, "guest": false} {
		got, err := InGroup(f, "administrators", user)
		if err != nil || got != want {
			t.Errorf("InGroup(%q) = %v, %v", user, got, err)
		}
	}
}

func TestSessionAuthenticator(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "authenticate.cgi")
	// Imitates DSM: prints the user only for the right token and cookie.
	os.WriteFile(script, []byte(`#!/bin/sh
case "$QUERY_STRING" in
  "SynoToken=good") ;;
  *) exit 1 ;;
esac
case "$HTTP_COOKIE" in
  *id=session*) echo "jiri" ;;
  *) exit 1 ;;
esac
`), 0o755)
	group := filepath.Join(dir, "group")
	os.WriteFile(group, []byte("administrators:x:101:admin,jiri\n"), 0o644)

	oldCGI, oldGroup := AuthenticateCGI, GroupFile
	AuthenticateCGI, GroupFile = script, group
	t.Cleanup(func() { AuthenticateCGI, GroupFile = oldCGI, oldGroup })

	t.Setenv("HTTP_COOKIE", "id=session")
	r := httptest.NewRequest("GET", "/api.cgi?path=state", nil)
	r.Header.Set("X-Syno-Token", "good")
	if user, err := SessionAuthenticator(true)(r); err != nil || user != "jiri" {
		t.Fatalf("valid session: %q %v", user, err)
	}

	r.Header.Set("X-Syno-Token", "bad")
	if _, err := SessionAuthenticator(true)(r); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("bad token: %v", err)
	}

	os.WriteFile(group, []byte("administrators:x:101:admin\n"), 0o644)
	r.Header.Set("X-Syno-Token", "good")
	if _, err := SessionAuthenticator(true)(r); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non admin: %v", err)
	}
	if _, err := SessionAuthenticator(false)(r); err != nil {
		t.Fatalf("non admin allowed when not admins only: %v", err)
	}
}

func startDaemon(t *testing.T, h http.Handler) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return sock
}

func TestFrontendForwardsAuthenticatedCalls(t *testing.T) {
	var seen *http.Request
	sock := startDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}))
	f := NewFrontend(sock, func(*http.Request) (string, error) { return "jiri", nil })

	r := httptest.NewRequest("POST", "/webman/3rdparty/synochan/api.cgi?path=threads/remove&SynoToken=x&lines=5", strings.NewReader(`{"keys":["a"]}`))
	r.Header.Set("Cookie", "id=secret")
	w := httptest.NewRecorder()
	f.ServeHTTP(w, r)

	if w.Code != 200 || !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatalf("response %d %s", w.Code, w.Body)
	}
	if seen.URL.Path != "/api/threads/remove" || seen.URL.RawQuery != "lines=5" || seen.Method != "POST" {
		t.Fatalf("forwarded %s %s?%s", seen.Method, seen.URL.Path, seen.URL.RawQuery)
	}
	if seen.Header.Get("Cookie") != "" || seen.Header.Get(server.UserHeader) != "jiri" {
		t.Fatalf("headers: %v", seen.Header)
	}
}

func TestFrontendRejects(t *testing.T) {
	sock := startDaemon(t, http.NotFoundHandler())
	cases := []struct {
		name string
		auth Authenticator
		url  string
		code int
	}{
		{"no session", func(*http.Request) (string, error) { return "", ErrUnauthenticated }, "/x?path=state", 401},
		{"not admin", func(*http.Request) (string, error) { return "bob", ErrForbidden }, "/x?path=state", 403},
		{"bad path", func(*http.Request) (string, error) { return "jiri", nil }, "/x?path=../etc", 404},
		{"missing path", func(*http.Request) (string, error) { return "jiri", nil }, "/x", 404},
	}
	for _, c := range cases {
		w := httptest.NewRecorder()
		NewFrontend(sock, c.auth).ServeHTTP(w, httptest.NewRequest("GET", c.url, nil))
		var body map[string]string
		json.Unmarshal(w.Body.Bytes(), &body)
		if w.Code != c.code || body["error"] == "" {
			t.Errorf("%s: %d %s", c.name, w.Code, w.Body)
		}
	}
}

func TestFrontendDaemonDown(t *testing.T) {
	f := NewFrontend(filepath.Join(t.TempDir(), "missing.sock"), func(*http.Request) (string, error) { return "jiri", nil })
	w := httptest.NewRecorder()
	f.ServeHTTP(w, httptest.NewRequest("GET", "/x?path=state", nil))
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "Package Center") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}
