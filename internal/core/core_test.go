package core

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"synochan/internal/config"
	"synochan/internal/fetch"
	"synochan/internal/fourchan"
)

// fakeBoard imitates the image board API, CDN and HTML pages for one board ("g").
type fakeBoard struct {
	mu           sync.Mutex
	threads      map[int64][]map[string]any // thread id -> posts
	modified     map[int64]time.Time
	catalog      []map[string]any
	catalogMod   time.Time
	gone         map[int64]bool
	requests     map[string]int
	notModified  int
	imageRequest int
	downloads    []string
}

func newFakeBoard() *fakeBoard {
	return &fakeBoard{
		threads:  map[int64][]map[string]any{},
		modified: map[int64]time.Time{},
		gone:     map[int64]bool{},
		requests: map[string]int{},
	}
}

func post(no int64, tim int64, filename, ext string, extra map[string]any) map[string]any {
	p := map[string]any{"no": no}
	if tim != 0 {
		p["tim"], p["filename"], p["ext"] = tim, filename, ext
	}
	for k, v := range extra {
		p[k] = v
	}
	return p
}

func (f *fakeBoard) setThread(id int64, posts ...map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.threads[id] = posts
	// Whole seconds, as HTTP dates have no sub second precision; always move forward.
	next := time.Now().Truncate(time.Second).Add(time.Second)
	if prev := f.modified[id]; !next.After(prev) {
		next = prev.Add(time.Second)
	}
	f.modified[id] = next
}

func (f *fakeBoard) setCatalog(threads ...map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.catalog = threads
	next := time.Now().Truncate(time.Second).Add(time.Second)
	if !next.After(f.catalogMod) {
		next = f.catalogMod.Add(time.Second)
	}
	f.catalogMod = next
}

func (f *fakeBoard) count(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests[path]
}

func (f *fakeBoard) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests[r.URL.Path]++

	notModified := func(mod time.Time) bool {
		if ims, err := http.ParseTime(r.Header.Get("If-Modified-Since")); err == nil && !mod.After(ims) {
			f.notModified++
			w.WriteHeader(http.StatusNotModified)
			return true
		}
		w.Header().Set("Last-Modified", mod.UTC().Format(http.TimeFormat))
		return false
	}

	var id int64
	switch {
	case r.URL.Path == "/g/catalog.json":
		if notModified(f.catalogMod) {
			return
		}
		json.NewEncoder(w).Encode([]map[string]any{{"page": 1, "threads": f.catalog}})
	case sscan(r.URL.Path, "/g/thread/%d.json", &id):
		if f.gone[id] || f.threads[id] == nil {
			http.NotFound(w, r)
			return
		}
		if notModified(f.modified[id]) {
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"posts": f.threads[id]})
	case sscan(r.URL.Path, "/g/thread/%d", &id):
		var b strings.Builder
		b.WriteString(`<html><head><script src="//s.4cdn.org/js/core.js"></script></head><body>`)
		for _, p := range f.threads[id] {
			if tim, ok := p["tim"]; ok {
				fmt.Fprintf(&b, `<a href="//i.4cdn.org/g/%v%s"><img src="//i.4cdn.org/g/%vs.jpg"></a>`, tim, p["ext"], tim)
			}
		}
		b.WriteString(`</body></html>`)
		w.Write([]byte(b.String()))
	case strings.HasPrefix(r.URL.Path, "/g/"):
		f.imageRequest++
		f.downloads = append(f.downloads, r.URL.Path)
		w.Write([]byte("file:" + r.URL.Path))
	default:
		http.NotFound(w, r)
	}
}

func sscan(path, format string, id *int64) bool {
	var rest string
	n, _ := fmt.Sscanf(path, format+"%s", id, &rest)
	if n == 1 {
		// Make sure the pattern consumed everything (Sscanf stops at the first mismatch).
		return fmt.Sprintf(format, *id) == path
	}
	return false
}

type harness struct {
	t     *testing.T
	board *fakeBoard
	srv   *httptest.Server
	cfg   *config.Store
	m     *Manager
	root  string
	data  string
}

func newHarness(t *testing.T, mutate func(*config.Settings)) *harness {
	t.Helper()
	h := &harness{t: t, board: newFakeBoard(), root: t.TempDir(), data: t.TempDir()}
	h.srv = httptest.NewServer(h.board)
	t.Cleanup(h.srv.Close)
	s := config.Defaults(h.root)
	s.MaxConcurrentDownloads = 4
	s.MinSecondsBetweenScrapes = 10
	if mutate != nil {
		mutate(&s)
	}
	h.cfg = config.NewMemory(s)
	h.start()
	return h
}

func (h *harness) start() {
	h.m = New(Options{
		Config:         h.cfg,
		Client:         fetch.New(func() string { return "test" }),
		Endpoints:      fourchan.Endpoints{API: h.srv.URL, Images: h.srv.URL, Thumbs: h.srv.URL, Boards: h.srv.URL},
		StatePath:      filepath.Join(h.data, "state.json"),
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		TrackerSpacing: time.Millisecond,
	})
	if err := h.m.Start(); err != nil {
		h.t.Fatal(err)
	}
	m := h.m
	h.t.Cleanup(func() { m.Shutdown(5 * time.Second) })
}

func (h *harness) waitFor(what string, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.t.Fatalf("timed out waiting for %s", what)
}

func (h *harness) thread(key string) *ThreadView {
	for _, t := range h.m.Snapshot().Threads {
		if t.Key == key {
			return &t
		}
	}
	return nil
}

// recheck makes a tracker due immediately instead of waiting for the interval.
func (h *harness) recheck(key string) {
	h.m.mu.Lock()
	if t := h.m.threadByKey[key]; t != nil {
		t.mu.Lock()
		t.lastAttempt = time.Now().Add(-time.Hour)
		t.mu.Unlock()
		h.m.mu.Unlock()
		h.m.q.Enqueue(t)
		return
	}
	b := h.m.boardByKey[key]
	h.m.mu.Unlock()
	b.mu.Lock()
	b.lastAttempt = time.Now().Add(-time.Hour)
	b.mu.Unlock()
	h.m.q.Enqueue(b)
}

func files(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			out = append(out, rel)
		}
		return nil
	})
	sort.Strings(out)
	return out
}

func TestThreadDownloadsFilesAndRechecks(t *testing.T) {
	h := newHarness(t, func(s *config.Settings) { s.SaveHTML = true })
	h.board.setThread(100,
		post(100, 1700000000001, "first", ".jpg", map[string]any{"sub": "Wallpapers"}),
		post(101, 0, "", "", map[string]any{"com": "no file"}),
		post(102, 1700000000002, "second", ".png", nil),
	)

	res := h.m.AddURLs("https://boards.4chan.org/g/thread/100#p101")
	if len(res) != 1 || res[0].Status != "added" || res[0].Key != "4chan/g/100" {
		t.Fatalf("add: %+v", res)
	}
	dir := filepath.Join(h.root, "4chan", "g", "100 - Wallpapers")
	h.waitFor("two files", func() bool {
		v := h.thread("4chan/g/100")
		return v != nil && v.Saved == 2
	})
	v := h.thread("4chan/g/100")
	if v.Subject != "Wallpapers" || *v.FileCount != 2 || *v.Pending != 0 || v.LastError != "" || !v.FolderExists || v.SaveTo != dir {
		t.Fatalf("view: %+v", v)
	}
	if got := files(t, dir); strings.Join(got, ",") != "100.jpg,102.png,Thread.html" {
		t.Fatalf("files: %v", got)
	}
	page, _ := os.ReadFile(filepath.Join(dir, "Thread.html"))
	if !strings.Contains(string(page), `href="100.jpg"`) || !strings.Contains(string(page), `src="https://s.4cdn.org/js/core.js"`) {
		t.Fatalf("page links not rewritten: %s", page)
	}

	// Unchanged thread: a conditional request answered with 304, nothing downloaded again.
	images := h.board.imageRequest
	h.recheck("4chan/g/100")
	h.waitFor("304", func() bool { h.board.mu.Lock(); defer h.board.mu.Unlock(); return h.board.notModified >= 1 })
	if h.board.imageRequest != images {
		t.Fatal("files downloaded again for an unchanged thread")
	}

	// A new post: only its file is downloaded.
	h.board.setThread(100,
		post(100, 1700000000001, "first", ".jpg", map[string]any{"sub": "Wallpapers"}),
		post(102, 1700000000002, "second", ".png", nil),
		post(103, 1700000000003, "third", ".gif", nil),
	)
	h.recheck("4chan/g/100")
	h.waitFor("third file", func() bool { v := h.thread("4chan/g/100"); return v != nil && v.Saved == 3 })
	if h.board.count("/g/1700000000001.jpg") != 1 {
		t.Fatal("old file downloaded twice")
	}
}

func TestFilenameFormats(t *testing.T) {
	cases := map[config.ImageFilenameFormat]string{
		config.FilenameID:            "100.jpg",
		config.FilenameOriginal:      "Little Saint.jpg",
		config.FilenameIDAndOriginal: "100 - Little Saint.jpg",
		config.FilenameOriginalAndID: "Little Saint - 100.jpg",
	}
	for format, want := range cases {
		u := fourchan.UploadInfo{Tim: "1700", UploadedFilename: "Little Saint", Ext: ".jpg", No: 100}
		if got := uploadFilename(u, format); got != want {
			t.Errorf("format %d: got %q, want %q", format, got, want)
		}
	}
}

func TestGoneThreadIsRemovedAndFolderKept(t *testing.T) {
	h := newHarness(t, nil)
	h.board.setThread(200, post(200, 1700000000010, "a", ".jpg", map[string]any{"sub": "Old: thread?"}))
	h.m.AddURLs("https://boards.4chan.org/g/thread/200")
	h.waitFor("download", func() bool { v := h.thread("4chan/g/200"); return v != nil && v.Saved == 1 })

	h.board.mu.Lock()
	h.board.gone[200] = true
	h.board.mu.Unlock()
	h.recheck("4chan/g/200")
	h.waitFor("removal", func() bool { return h.thread("4chan/g/200") == nil })
	if _, err := os.Stat(filepath.Join(h.root, "4chan", "g", "200 - Old thread", "200.jpg")); err != nil {
		t.Fatal(err)
	}
}

func TestFolderNamesAndRenaming(t *testing.T) {
	h := newHarness(t, func(s *config.Settings) { s.FolderName = config.FolderSubjectNumber })
	h.board.setThread(300, post(300, 1700000000020, "a", ".jpg", nil)) // No subject: named by number.
	h.m.AddURLs("https://boards.4chan.org/g/thread/300")
	h.waitFor("download", func() bool { v := h.thread("4chan/g/300"); return v != nil && v.Saved == 1 })
	base := filepath.Join(h.root, "4chan", "g")
	if _, err := os.Stat(filepath.Join(base, "300", "300.jpg")); err != nil {
		t.Fatal(err)
	}

	// Renaming the thread renames its folder; a taken name gets a suffix.
	os.MkdirAll(filepath.Join(base, "My Name - 300"), 0o755)
	if err := h.m.RenameThread("4chan/g/300", "My Name"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(base, "My Name - 300 (1)", "300.jpg")); err != nil {
		t.Fatal(err)
	}
	if v := h.thread("4chan/g/300"); v.SaveTo != filepath.Join(base, "My Name - 300 (1)") {
		t.Fatalf("save_to %q", v.SaveTo)
	}

	// Removal leaves the folder as it is.
	if err := h.m.RemoveThread("4chan/g/300", false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(base, "My Name - 300 (1)")); err != nil {
		t.Fatal(err)
	}
	if err := h.m.RemoveThread("4chan/g/300", false); err != ErrNotFound {
		t.Fatalf("second removal: %v", err)
	}
}

func TestPriorityDownloadsFirstAndEnds(t *testing.T) {
	h := newHarness(t, func(s *config.Settings) {
		s.MaxConcurrentDownloads = 1
		s.StartPaused = true
	})
	var postsA, postsB []map[string]any
	for i := int64(0); i < 5; i++ {
		postsA = append(postsA, post(700+i, 1700000007000+i, "a", ".jpg", nil))
		postsB = append(postsB, post(800+i, 1700000008000+i, "b", ".jpg", nil))
	}
	h.board.setThread(700, postsA...)
	h.board.setThread(800, postsB...)
	h.m.AddURLs("https://boards.4chan.org/g/thread/700 https://boards.4chan.org/g/thread/800")
	if err := h.m.Prioritize([]string{"4chan/g/800"}, false); err != nil {
		t.Fatal(err)
	}
	if v := h.thread("4chan/g/800"); v.Priority != 1 {
		t.Fatalf("priority %d", v.Priority)
	}
	h.m.SetPaused(false)
	h.waitFor("all files", func() bool {
		a, b := h.thread("4chan/g/700"), h.thread("4chan/g/800")
		return a.Saved == 5 && b.Saved == 5
	})
	// Every file of the prioritised thread came before any of the other thread's.
	h.board.mu.Lock()
	order := append([]string(nil), h.board.downloads...)
	h.board.mu.Unlock()
	for i, p := range order {
		if i < 5 && !strings.Contains(p, "170000000800") {
			t.Fatalf("download order %v", order)
		}
	}
	h.waitFor("priority ended", func() bool { return h.thread("4chan/g/800").Priority == 0 })
}

func TestPriorityRenumbering(t *testing.T) {
	h := newHarness(t, func(s *config.Settings) { s.StartPaused = true })
	h.m.AddURLs("https://boards.4chan.org/g/thread/1 https://boards.4chan.org/g/thread/2 https://boards.4chan.org/g/thread/3")
	h.m.Prioritize([]string{"4chan/g/1", "4chan/g/2"}, false)
	h.m.Prioritize([]string{"4chan/g/3"}, true)
	prio := func(k string) int { return h.thread(k).Priority }
	if prio("4chan/g/3") != 1 || prio("4chan/g/1") != 2 || prio("4chan/g/2") != 3 {
		t.Fatalf("priorities %d %d %d", prio("4chan/g/1"), prio("4chan/g/2"), prio("4chan/g/3"))
	}
	h.m.Unprioritize([]string{"4chan/g/3"})
	if prio("4chan/g/3") != 0 || prio("4chan/g/1") != 1 || prio("4chan/g/2") != 2 {
		t.Fatalf("after unprioritize %d %d %d", prio("4chan/g/1"), prio("4chan/g/2"), prio("4chan/g/3"))
	}
	h.m.RemoveThread("4chan/g/1", false)
	if prio("4chan/g/2") != 1 {
		t.Fatalf("after removal %d", prio("4chan/g/2"))
	}
}

func TestBoardAddsOnlyNewThreads(t *testing.T) {
	h := newHarness(t, nil)
	h.board.setThread(400, post(400, 1700000000030, "a", ".jpg", nil))
	h.board.setThread(401, post(401, 0, "", "", map[string]any{"com": "text only"}))
	h.board.setCatalog(
		map[string]any{"no": 400, "sub": "From catalog", "images": 0},
		map[string]any{"no": 401, "name": "Anonymous", "com": "short op", "images": 2},
	)
	res := h.m.AddURLs("https://boards.4chan.org/g/catalog")
	if res[0].Status != "added" || res[0].Kind != "board" {
		t.Fatalf("add: %+v", res)
	}
	h.waitFor("threads from board", func() bool { return len(h.m.Snapshot().Threads) == 2 })
	v := h.thread("4chan/g/401")
	if v.Subject != "short op" || v.FileCount == nil {
		t.Fatalf("catalog thread view: %+v", v)
	}
	h.waitFor("catalog thread download", func() bool { v := h.thread("4chan/g/400"); return v != nil && v.Saved == 1 })

	// The user removes a thread: it must not come back on the next board scrape.
	h.m.RemoveThread("4chan/g/400", false)
	h.board.setThread(402, post(402, 0, "", "", nil))
	h.board.setCatalog(
		map[string]any{"no": 400, "images": 0},
		map[string]any{"no": 401, "images": 2},
		map[string]any{"no": 402, "images": 0},
	)
	h.recheck("4chan/g")
	h.waitFor("new thread", func() bool { return h.thread("4chan/g/402") != nil })
	time.Sleep(100 * time.Millisecond)
	if h.thread("4chan/g/400") != nil {
		t.Fatal("removed thread came back")
	}
	if bv := h.m.Snapshot().Boards[0]; bv.ThreadCount != 3 {
		t.Fatalf("board view: %+v", bv)
	}

	// Removing the board keeps its threads.
	h.m.ClearBoards()
	if s := h.m.Snapshot(); len(s.Boards) != 0 || len(s.Threads) != 2 {
		t.Fatalf("after clearing boards: %d boards, %d threads", len(s.Boards), len(s.Threads))
	}
}

func TestDuplicateAndInvalidInput(t *testing.T) {
	h := newHarness(t, func(s *config.Settings) { s.StartPaused = true })
	res := h.m.AddURLs("https://boards.4chan.org/g/thread/1, https://boards.4chan.org/g/thread/1#p5\nnot-a-link https://boards.4chan.org/g/")
	statuses := []string{}
	for _, r := range res {
		statuses = append(statuses, r.Status)
	}
	if strings.Join(statuses, ",") != "added,duplicate,invalid,added" {
		t.Fatalf("statuses: %v", statuses)
	}
	if res[1].SaveTo != filepath.Join(h.root, "4chan", "g", "1") {
		t.Fatalf("duplicate save path: %q", res[1].SaveTo)
	}
	if v := h.thread("4chan/g/1"); v.Subject != "Loading…" || !v.Loading {
		t.Fatalf("unscraped view: %+v", v)
	}
	if q := h.m.QueueStatus(); !q.Paused || q.Queued != 2 {
		t.Fatalf("queue: %+v", q)
	}
}

func TestPersistenceAcrossRestart(t *testing.T) {
	h := newHarness(t, nil)
	h.board.setThread(500,
		post(500, 1700000000040, "a", ".jpg", map[string]any{"sub": "Keep me"}),
		post(501, 1700000000041, "b", ".jpg", nil),
	)
	h.board.setCatalog(map[string]any{"no": 500, "images": 1})
	h.m.AddURLs("https://boards.4chan.org/g/thread/500 https://boards.4chan.org/g/")
	h.waitFor("downloads", func() bool { v := h.thread("4chan/g/500"); return v != nil && v.Saved == 2 })
	h.waitFor("board scraped", func() bool { s := h.m.Snapshot(); return len(s.Boards) == 1 && !s.Boards[0].LastCheck.IsZero() })
	h.m.RenameThread("4chan/g/500", "Renamed")
	h.m.Shutdown(5 * time.Second)
	images := h.board.imageRequest

	h.start()
	v := h.thread("4chan/g/500")
	if v == nil || v.Subject != "Renamed" || v.Saved != 2 || *v.FileCount != 2 {
		t.Fatalf("restored view: %+v", v)
	}
	if len(h.m.Snapshot().Boards) != 1 {
		t.Fatal("board not restored")
	}
	h.waitFor("first scrape after restart", func() bool {
		v := h.thread("4chan/g/500")
		return v != nil && !v.LastCheck.IsZero()
	})
	time.Sleep(100 * time.Millisecond)
	if h.board.imageRequest != images {
		t.Fatal("saved files downloaded again after restart")
	}
	h.board.mu.Lock()
	nm := h.board.notModified
	h.board.mu.Unlock()
	if nm < 1 {
		t.Fatal("restored thread should send If-Modified-Since when all files were saved")
	}
}

func TestRestartRefetchesWhenFilesWereMissing(t *testing.T) {
	data := t.TempDir()
	lm := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	fc := 2
	st := stateFile{Threads: []threadState{{
		Site: "4chan", Board: "g", ID: 600, FileCount: &fc, LastModified: lm,
		SaveTo: filepath.Join(t.TempDir(), "600"), SavedAssets: []string{"Upload:600.1700000000050"},
	}}}
	if err := writeState(filepath.Join(data, "state.json"), st); err != nil {
		t.Fatal(err)
	}
	m := New(Options{Config: config.NewMemory(config.Defaults(t.TempDir())), StatePath: filepath.Join(data, "state.json")})
	ts, _ := readState(filepath.Join(data, "state.json"))
	thr := m.threadFromState(ts.Threads[0])
	if !thr.lastModified.IsZero() {
		t.Fatal("If-Modified-Since must not be used while files are missing")
	}
	if _, ok := thr.seen[AssetID{Type: AssetUpload, ID: "600.1700000000050"}]; !ok {
		t.Fatal("saved asset not marked seen")
	}
}

func TestUpdateSettingsValidates(t *testing.T) {
	h := newHarness(t, nil)
	s := h.m.Settings()

	bad := s
	bad.MinSecondsBetweenScrapes = 2
	if err := h.m.UpdateSettings(bad); err == nil {
		t.Fatal("accepted a 2 second interval")
	}

	notDir := filepath.Join(t.TempDir(), "file")
	os.WriteFile(notDir, []byte("x"), 0o644)
	bad = s
	bad.SavePath = notDir
	if err := h.m.UpdateSettings(bad); err == nil {
		t.Fatal("accepted a file as save folder")
	}

	good := s
	good.SavePath = filepath.Join(t.TempDir(), "new", "place") + "/"
	good.SaveHTML = true
	if err := h.m.UpdateSettings(good); err != nil {
		t.Fatal(err)
	}
	if got := h.m.Settings(); !got.SaveHTML || strings.HasSuffix(got.SavePath, "/") {
		t.Fatalf("settings not applied: %+v", got)
	}
}

func TestSharePath(t *testing.T) {
	cases := map[string]string{
		"/volume1/downloads/4chan/g/1": "/downloads/4chan/g/1",
		"/volumeUSB1/usbshare/x":       "/usbshare/x",
		"/var/packages/synochan/var":   "",
	}
	for in, want := range cases {
		if got := SharePath(in); got != want {
			t.Errorf("SharePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRemoveWithFilesDeletesOnlyTheThreadFolder(t *testing.T) {
	h := newHarness(t, nil)
	h.board.setThread(900, post(900, 1700000009000, "a", ".jpg", map[string]any{"sub": "Delete me"}))
	h.board.setThread(901, post(901, 1700000009010, "b", ".jpg", map[string]any{"sub": "Keep me"}))
	h.board.setThread(902, post(902, 0, "", "", map[string]any{"sub": "No files"}))
	h.m.AddURLs("https://boards.4chan.org/g/thread/900 https://boards.4chan.org/g/thread/901 https://boards.4chan.org/g/thread/902")
	h.waitFor("downloads", func() bool {
		a, b := h.thread("4chan/g/900"), h.thread("4chan/g/901")
		return a != nil && b != nil && a.Saved == 1 && b.Saved == 1
	})
	base := filepath.Join(h.root, "4chan", "g")
	if err := h.m.RemoveThread("4chan/g/900", true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(base, "900 - Delete me")); !os.IsNotExist(err) {
		t.Fatalf("folder not deleted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "901 - Keep me", "901.jpg")); err != nil {
		t.Fatal("other thread's files touched")
	}
	// A thread without a folder yet removes cleanly.
	if err := h.m.RemoveThread("4chan/g/902", true); err != nil {
		t.Fatal(err)
	}
	// Clearing with files deletes the rest; the board folder itself stays.
	if errs := h.m.ClearThreads(true); len(errs) != 0 {
		t.Fatal(errs)
	}
	entries, _ := os.ReadDir(base)
	if len(entries) != 0 {
		t.Fatalf("left over: %v", entries)
	}
	if _, err := os.Stat(base); err != nil {
		t.Fatal("board folder deleted")
	}
}

func TestDeleteRefusesFoldersOutsideTheBoard(t *testing.T) {
	h := newHarness(t, func(s *config.Settings) { s.StartPaused = true })
	h.m.AddURLs("https://boards.4chan.org/g/thread/950")
	h.m.mu.Lock()
	th := h.m.threadByKey["4chan/g/950"]
	h.m.mu.Unlock()
	th.mu.Lock()
	th.dir = h.root // Pretend something pointed the thread at the save root.
	th.mu.Unlock()
	if err := h.m.RemoveThread("4chan/g/950", true); err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("expected refusal, got %v", err)
	}
	if _, err := os.Stat(h.root); err != nil {
		t.Fatal("save root deleted")
	}
}

func TestBoardFilters(t *testing.T) {
	h := newHarness(t, func(s *config.Settings) {
		s.StartPaused = true
		s.Filters.Skip = []string{"spoiler*", "general thread"}
	})
	h.board.setCatalog(
		map[string]any{"no": 1000, "sub": "Rules", "sticky": 1},
		map[string]any{"no": 1001, "sub": "Spoilers inside"},
		map[string]any{"no": 1002, "sub": "Daily", "com": "the GENERAL thread for cats"},
		map[string]any{"no": 1003, "sub": "Café wallpapers"},
		map[string]any{"no": 1004, "com": "<span class=\"quote\">&gt;nothing special</span>"},
	)
	for id := int64(1000); id <= 1004; id++ {
		h.board.setThread(id, post(id, 0, "", "", nil))
	}
	h.m.AddURLs("https://boards.4chan.org/g/catalog https://boards.4chan.org/g/thread/1001")
	h.m.SetPaused(false)
	h.waitFor("board scanned", func() bool { s := h.m.Snapshot(); return len(s.Boards) == 1 && !s.Boards[0].LastCheck.IsZero() })
	h.m.SetPaused(true)

	keys := map[int64]bool{}
	for _, v := range h.m.Snapshot().Threads {
		keys[v.ID] = true
	}
	// 1001 was added by hand and stays; 1003 and 1004 pass; the sticky and 1002 are filtered.
	if !keys[1001] || !keys[1003] || !keys[1004] || keys[1000] || keys[1002] || len(keys) != 3 {
		t.Fatalf("tracked: %v", keys)
	}
	filtered, err := h.m.BoardFiltered("4chan/g")
	if err != nil || len(filtered) != 2 || filtered[0].Reason != "sticky thread" || filtered[1].Reason != `contains "general thread"` {
		t.Fatalf("filtered: %+v %v", filtered, err)
	}
	if n := h.m.Snapshot().Boards[0].Filtered; n != 2 {
		t.Fatalf("filtered count %d", n)
	}

	// A board's own only-list: tracked board threads that no longer fit are offered for removal,
	// the thread added by hand never is.
	if err := h.m.SetBoardFilters("4chan/g", config.BoardFilters{Only: []string{"cafe"}}); err != nil {
		t.Fatal(err)
	}
	matches := h.m.FilterMatches("4chan/g")
	if len(matches) != 1 || matches[0].Key != "4chan/g/1004" {
		t.Fatalf("matches: %+v", matches)
	}
	if err := h.m.SetBoardFilters("4chan/g", config.BoardFilters{Skip: []string{"*"}}); err == nil {
		t.Fatal("a match-everything filter was accepted")
	}
	if err := h.m.SetBoardFilters("4chan/nope", config.BoardFilters{}); err != ErrNotFound {
		t.Fatalf("unknown board: %v", err)
	}

	// Board filters and origins survive a restart.
	h.m.Shutdown(5 * time.Second)
	h.start()
	if v := h.m.Snapshot().Boards[0]; len(v.Filters.Only) != 1 || v.Filters.Only[0] != "cafe" {
		t.Fatalf("board filters not restored: %+v", v.Filters)
	}
	if matches := h.m.FilterMatches(""); len(matches) != 1 || matches[0].Key != "4chan/g/1004" {
		t.Fatalf("origins not restored: %+v", matches)
	}
}

func TestRememberedBoardFilters(t *testing.T) {
	h := newHarness(t, nil)
	h.board.setCatalog(map[string]any{"no": 1100, "sub": "Spoiler talk"}, map[string]any{"no": 1101, "sub": "Fine"})
	h.board.setThread(1100, post(1100, 0, "", "", nil))
	h.board.setThread(1101, post(1101, 0, "", "", nil))
	h.m.AddURLs("https://boards.4chan.org/g/")
	if err := h.m.SetBoardFilters("4chan/g", config.BoardFilters{Skip: []string{"spoiler"}}); err != nil {
		t.Fatal(err)
	}
	h.waitFor("first scan", func() bool { return h.board.count("/g/catalog.json") >= 1 })
	h.m.RemoveBoard("4chan/g")
	h.m.ClearThreads(false)

	// Added back in the app: filters applied, board held until the user decides.
	scans := h.board.count("/g/catalog.json")
	res := h.m.AddURLsWith("https://boards.4chan.org/g/", AddOptions{AskAboutRememberedFilters: true})
	if !res[0].Pending || res[0].RememberedFilters == nil || res[0].RememberedFilters.Skip[0] != "spoiler" {
		t.Fatalf("add result: %+v", res[0])
	}
	time.Sleep(200 * time.Millisecond)
	if h.board.count("/g/catalog.json") != scans || !h.m.Snapshot().Boards[0].Pending {
		t.Fatal("a pending board was checked before the decision")
	}
	// Dropping the filters forgets them and starts the board.
	if err := h.m.ConfirmBoard("4chan/g", false); err != nil {
		t.Fatal(err)
	}
	h.waitFor("both threads", func() bool { return h.thread("4chan/g/1100") != nil && h.thread("4chan/g/1101") != nil })
	if err := h.m.ConfirmBoard("4chan/g", true); err != ErrNotPending {
		t.Fatalf("second decision: %v", err)
	}
	h.m.RemoveBoard("4chan/g")
	if res := h.m.AddURLs("https://boards.4chan.org/g/"); res[0].RememberedFilters != nil || res[0].Pending {
		t.Fatalf("forgotten filters came back: %+v", res[0])
	}

	// From the extension (no question): remembered filters are applied right away.
	h.m.SetBoardFilters("4chan/g", config.BoardFilters{Only: []string{"fine"}})
	h.m.RemoveBoard("4chan/g")
	res = h.m.AddURLs("https://boards.4chan.org/g/")
	if res[0].Pending || res[0].RememberedFilters == nil || h.m.Snapshot().Boards[0].Filters.Only[0] != "fine" {
		t.Fatalf("extension add: %+v", res[0])
	}

	// Nobody decides: the board starts with its filters after the timeout. The memory survives a restart.
	h.m.RemoveBoard("4chan/g")
	old := PendingDecisionTimeout
	PendingDecisionTimeout = 100 * time.Millisecond
	t.Cleanup(func() { PendingDecisionTimeout = old })
	h.m.AddURLsWith("https://boards.4chan.org/g/", AddOptions{AskAboutRememberedFilters: true})
	h.waitFor("timeout start", func() bool { b := h.m.Snapshot().Boards; return len(b) == 1 && !b[0].Pending })
	h.m.RemoveBoard("4chan/g")
	h.m.Shutdown(5 * time.Second)
	h.start()
	if res := h.m.AddURLs("https://boards.4chan.org/g/"); res[0].RememberedFilters == nil || res[0].RememberedFilters.Only[0] != "fine" {
		t.Fatalf("memory lost on restart: %+v", res[0])
	}
}
