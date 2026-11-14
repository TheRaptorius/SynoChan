package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"synochan/internal/config"
	"synochan/internal/core"
	"synochan/internal/fetch"
	"synochan/internal/fourchan"
	"synochan/internal/tokens"
)

func newAPI(t *testing.T) (http.Handler, string) {
	t.Helper()
	s := config.Defaults(t.TempDir())
	s.StartPaused = true // Nothing is fetched in these tests.
	m := core.New(core.Options{
		Config:    config.NewMemory(s),
		Client:    fetch.New(func() string { return "test" }),
		Endpoints: fourchan.Endpoints{API: "http://127.0.0.1:1", Images: "http://127.0.0.1:1", Thumbs: "http://127.0.0.1:1", Boards: "http://127.0.0.1:1"},
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	m.Start()
	t.Cleanup(func() { m.Shutdown(time.Second) })
	logPath := filepath.Join(t.TempDir(), "log")
	os.WriteFile(logPath, []byte("one\ntwo\nthree\n"), 0o644)
	return New(m, tokens.NewMemory(), Info{Version: "1.2.3", RepoURL: "https://example.org/repo", Author: "someone", LogPath: logPath}), logPath
}

func call(t *testing.T, h http.Handler, method, path, body string) (int, map[string]any) {
	t.Helper()
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	r.Header.Set(UserHeader, "jiri")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	out := map[string]any{}
	json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func TestAPIFlow(t *testing.T) {
	h, _ := newAPI(t)

	code, out := call(t, h, "POST", "/api/add", `{"text":"https://boards.4chan.org/g/thread/1, https://boards.4chan.org/a/, nope"}`)
	results := out["results"].([]any)
	if code != 200 || len(results) != 3 {
		t.Fatalf("add: %d %v", code, out)
	}

	_, state := call(t, h, "GET", "/api/state", "")
	rev := state["rev"].(float64)
	if len(state["threads"].([]any)) != 1 || len(state["boards"].([]any)) != 1 {
		t.Fatalf("state: %v", state)
	}
	_, same := call(t, h, "GET", "/api/state?rev="+jsonNumber(rev), "")
	if same["unchanged"] != true {
		t.Fatalf("expected unchanged: %v", same)
	}

	if code, _ := call(t, h, "POST", "/api/threads/rename", `{"key":"4chan/g/1","subject":"Hello"}`); code != 200 {
		t.Fatalf("rename: %d", code)
	}
	_, state = call(t, h, "GET", "/api/state", "")
	if subj := state["threads"].([]any)[0].(map[string]any)["subject"]; subj != "Hello" {
		t.Fatalf("subject %v", subj)
	}

	if code, _ := call(t, h, "POST", "/api/threads/prioritize", `{"keys":["4chan/g/1"]}`); code != 200 {
		t.Fatalf("prioritize: %d", code)
	}
	_, state = call(t, h, "GET", "/api/state", "")
	if p := state["threads"].([]any)[0].(map[string]any)["priority"]; p != float64(1) {
		t.Fatalf("priority %v", p)
	}
	call(t, h, "POST", "/api/threads/unprioritize", `{"keys":["4chan/g/1"]}`)
	if code, _ := call(t, h, "POST", "/api/threads/prioritize", `{"keys":["nope"]}`); code != 404 {
		t.Fatalf("unknown thread: %d", code)
	}
	if state["save_path"] == "" {
		t.Fatal("snapshot lacks the save path")
	}

	_, urls := call(t, h, "GET", "/api/urls?kind=boards", "")
	if u := urls["urls"].([]any); len(u) != 1 || u[0] != "https://boards.4chan.org/a/catalog" {
		t.Fatalf("urls %v", urls)
	}

	call(t, h, "POST", "/api/threads/remove", `{"keys":["4chan/g/1"]}`)
	call(t, h, "POST", "/api/boards/clear", "")
	_, state = call(t, h, "GET", "/api/state", "")
	if len(state["threads"].([]any)) != 0 || len(state["boards"].([]any)) != 0 {
		t.Fatalf("not removed: %v", state)
	}

	_, q := call(t, h, "POST", "/api/pause", `{"paused":false}`)
	if q["paused"] != false {
		t.Fatalf("pause: %v", q)
	}

	_, info := call(t, h, "GET", "/api/info", "")
	if info["version"] != "1.2.3" || info["user"] != "jiri" || info["repo_url"] != "https://example.org/repo" || info["author"] != "someone" {
		t.Fatalf("info: %v", info)
	}
	_, logs := call(t, h, "GET", "/api/logs?lines=2", "")
	if l := logs["lines"].([]any); len(l) != 2 || l[1] != "three" {
		t.Fatalf("logs: %v", logs)
	}
}

func TestAPISettings(t *testing.T) {
	h, _ := newAPI(t)
	code, out := call(t, h, "POST", "/api/settings", `{"min_seconds_between_scrapes":5}`)
	if code != 400 || !strings.Contains(out["error"].(string), "at least 10") {
		t.Fatalf("invalid settings: %d %v", code, out)
	}
	code, out = call(t, h, "POST", "/api/settings", `{"save_html":true,"max_concurrent_downloads":2,"theme":"dark"}`)
	if code != 200 || out["save_html"] != true || out["max_concurrent_downloads"].(float64) != 2 || out["keep_lists"] != true || out["theme"] != "dark" {
		t.Fatalf("partial update: %d %v", code, out)
	}
	if code, _ := call(t, h, "GET", "/api/settings", ""); code != 200 {
		t.Fatal("get settings")
	}
	if code, _ := call(t, h, "POST", "/api/settings", `{"theme":"purple"}`); code != 400 {
		t.Fatal("unknown theme accepted")
	}
	if code, _ := call(t, h, "POST", "/api/settings", `not json`); code != 400 {
		t.Fatal("bad body accepted")
	}
}

func jsonNumber(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}

func callWithToken(t *testing.T, h http.Handler, method, path, body, token string) (int, map[string]any) {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		r.Header.Set(TokenHeader, token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	out := map[string]any{}
	json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func TestExtensionTokens(t *testing.T) {
	h, _ := newAPI(t)
	if code, _ := callWithToken(t, h, "GET", "/api/ext/ping", "", ""); code != 401 {
		t.Fatalf("ping without token: %d", code)
	}
	_, created := call(t, h, "POST", "/api/tokens/create", `{"name":"Chrome"}`)
	secret := created["secret"].(string)
	id := created["token"].(map[string]any)["id"].(string)
	if code, out := callWithToken(t, h, "GET", "/api/ext/ping", "", secret); code != 200 || out["version"] != "1.2.3" {
		t.Fatalf("ping: %d %v", code, out)
	}
	code, out := callWithToken(t, h, "POST", "/api/ext/add", `{"urls":["https://boards.4chan.org/g/thread/77","nope"]}`, secret)
	if code != 200 || len(out["results"].([]any)) != 2 {
		t.Fatalf("add: %d %v", code, out)
	}
	_, list := call(t, h, "GET", "/api/tokens", "")
	toks := list["tokens"].([]any)
	if len(toks) != 1 || toks[0].(map[string]any)["hash"] != nil {
		t.Fatalf("list leaks or misses tokens: %v", toks)
	}
	call(t, h, "POST", "/api/tokens/revoke", `{"id":"`+id+`"}`)
	if code, _ := callWithToken(t, h, "POST", "/api/ext/add", `{"urls":[]}`, secret); code != 401 {
		t.Fatalf("revoked token accepted: %d", code)
	}
}
