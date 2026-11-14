package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"synochan/internal/config"
	"synochan/internal/fetch"
	"synochan/internal/fourchan"
	"synochan/internal/queue"
)

// NoSubject is shown for threads without any usable subject.
const NoSubject = "No Subject"

// apiSpacing keeps two requests of one check a second apart, as the image board's API rules require.
const apiSpacing = time.Second

// Thread tracks one thread: it is rechecked periodically and queues downloads for new files.
type Thread struct {
	m       *Manager
	Site    string
	Board   string
	ID      int64
	Root    string // Save folder at the time the thread was added; its folder is created below it.
	AddedAt time.Time

	ctx    context.Context
	cancel context.CancelFunc

	mu             sync.Mutex
	dir            string // The thread's folder once created, "" before the first file.
	subject        string
	subjectSet     bool // Subject known (scraped, from the catalog or set by the user), possibly empty.
	fileCount      int
	fileCountKnown bool
	lastModified   time.Time // Server time of the last successful check, sent as If-Modified-Since.
	lastAttempt    time.Time // Local time the last check started, for spacing checks out.
	lastCheck      time.Time // Local time of the last successful check, for display.
	lastError      string
	seen           map[AssetID]struct{} // Assets that were queued and need no further processing.
	saved          map[AssetID]struct{} // Assets that were saved to disk.
	failed         map[AssetID]struct{} // Assets the queue gave up on.
	lane           queue.Lane
	prio           int  // User priority: 1 is downloaded first, 0 means none.
	prioChecked    bool // A check finished since the thread was prioritised.
}

func (m *Manager) newThread(board string, id int64, root string) *Thread {
	ctx, cancel := context.WithCancel(m.ctx)
	return &Thread{
		m:       m,
		Site:    fourchan.SiteName,
		Board:   board,
		ID:      id,
		Root:    root,
		AddedAt: time.Now(),
		ctx:     ctx,
		cancel:  cancel,
		seen:    make(map[AssetID]struct{}),
		saved:   make(map[AssetID]struct{}),
		failed:  make(map[AssetID]struct{}),
		lane:    queue.Default,
	}
}

// Key identifies the thread, e.g. "4chan/g/123456".
func (t *Thread) Key() string { return threadKey(t.Site, t.Board, t.ID) }

func threadKey(site, board string, id int64) string { return fmt.Sprintf("%s/%s/%d", site, board, id) }

// URL is the public address of the thread.
func (t *Thread) URL() string { return fourchan.ThreadURL(t.Board, t.ID) }

func (t *Thread) String() string { return "Thread " + t.Key() }

// Context implements queue.Item.
func (t *Thread) Context() context.Context { return t.ctx }

// ShouldProcess implements queue.Item.
func (t *Thread) ShouldProcess() bool { return t.ctx.Err() == nil }

// IsAsset implements queue.Item.
func (t *Thread) IsAsset() bool { return false }

// Order implements queue.Item. Prioritised threads are checked first, new threads before rechecks.
func (t *Thread) Order() queue.Order {
	t.mu.Lock()
	defer t.mu.Unlock()
	return queue.Order{Rank: t.rankLocked(), Lane: t.lane}
}

func (t *Thread) rankLocked() int {
	if t.prio > 0 {
		return t.prio
	}
	return queue.NoRank
}

func (t *Thread) assetOrder(lane queue.Lane) queue.Order {
	t.mu.Lock()
	defer t.mu.Unlock()
	return queue.Order{Rank: t.rankLocked(), Lane: lane}
}

// ReadyAt implements queue.Item.
func (t *Thread) ReadyAt() time.Time {
	t.mu.Lock()
	last := t.lastAttempt
	t.mu.Unlock()
	if last.IsZero() {
		return time.Time{}
	}
	return last.Add(time.Duration(t.m.cfg.Get().MinSecondsBetweenScrapes) * time.Second)
}

func (t *Thread) displaySubjectLocked() string {
	if !t.subjectSet && !t.fileCountKnown {
		return "Loading…"
	}
	if t.subject == "" {
		return NoSubject
	}
	return t.subject
}

// plannedDirLocked is the folder the thread is (or will be) saved in, by the folder name setting.
func (t *Thread) plannedDirLocked() string {
	return threadDir(t.Root, t.Board, t.ID, t.subject, t.m.cfg.Get().FolderName)
}

// ensureDir creates the thread's folder on first use and returns it.
func (t *Thread) ensureDir() (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.dir != "" {
		if _, err := os.Stat(t.dir); err == nil {
			return t.dir, nil
		}
	}
	dir := t.plannedDirLocked()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", friendlyPermissionError(dir, err)
	}
	t.dir = dir
	return dir, nil
}

// SetSubject renames the thread and, when its folder name includes the subject, its folder.
func (t *Thread) SetSubject(subject string) error {
	t.mu.Lock()
	t.subject = fourchan.SanitiseSubject(subject)
	t.subjectSet = true
	var renameErr error
	if t.dir != "" {
		if want := t.plannedDirLocked(); want != t.dir {
			// A download writing into the old folder fails and is retried into the new one.
			newDir, err := renameDir(t.dir, want)
			if err != nil {
				renameErr = fmt.Errorf("thread renamed, but its folder could not be renamed: %w", err)
			} else {
				t.dir = newDir
			}
		}
	}
	t.mu.Unlock()
	t.m.touch()
	return renameErr
}

func (t *Thread) noteError(err error) {
	if err == nil || errors.Is(err, context.Canceled) {
		return
	}
	t.mu.Lock()
	t.lastError = err.Error()
	t.mu.Unlock()
	t.m.touch()
}

func (t *Thread) markSaved(id AssetID) {
	t.mu.Lock()
	t.saved[id] = struct{}{}
	t.mu.Unlock()
	t.m.touch()
	t.m.checkPriorityDone(t)
}

func (t *Thread) markFailed(id AssetID) {
	t.mu.Lock()
	t.failed[id] = struct{}{}
	t.mu.Unlock()
	t.m.touch()
	t.m.checkPriorityDone(t)
}

// pendingLocked counts uploads that were found but are neither saved nor given up on.
func (t *Thread) pendingLocked() int {
	pending := 0
	for id := range t.seen {
		if id.Type != AssetUpload {
			continue
		}
		_, saved := t.saved[id]
		_, failed := t.failed[id]
		if !saved && !failed {
			pending++
		}
	}
	return pending
}

// Process implements queue.Item: checks the thread and queues downloads for files not seen before.
func (t *Thread) Process(ctx context.Context) (queue.Result, error) {
	if !t.ShouldProcess() {
		return queue.Result{Remove: true}, nil
	}
	s := t.m.cfg.Get()
	ep := t.m.endpoints

	t.mu.Lock()
	t.lastAttempt = time.Now()
	since := t.lastModified
	needSubject := !t.subjectSet
	t.mu.Unlock()
	t.m.touch()

	res, err := t.m.client.GetIfModified(ctx, ep.ThreadJSONURL(t.Board, t.ID), since)
	if err != nil {
		if fetch.IsGone(err) {
			t.m.log.Info("thread is gone, removing", "thread", t.Key())
			go t.m.removeThread(t)
			return queue.Result{Remove: true}, nil
		}
		t.noteError(err)
		return queue.Result{}, err
	}

	if res.NotModified {
		t.mu.Lock()
		t.lastModified = res.LastModified
		t.lastCheck = time.Now()
		t.lastError = ""
		t.prioChecked = true
		t.mu.Unlock()
		t.m.touch()
		t.m.checkPriorityDone(t)
		return queue.Result{}, nil
	}

	var thread fourchan.ThreadResponse
	if err := json.Unmarshal(res.Body, &thread); err != nil {
		err = fmt.Errorf("parse thread JSON: %w", err)
		t.noteError(err)
		return queue.Result{}, err
	}
	if len(thread.Posts) == 0 {
		err := errors.New("thread JSON has no posts")
		t.noteError(err)
		return queue.Result{}, err
	}

	// The subject is recorded before anything is saved, so the folder is created with it.
	if needSubject {
		subject := fourchan.ExtractSubject(&thread.Posts[0])
		t.mu.Lock()
		if !t.subjectSet {
			t.subject, t.subjectSet = subject, true
		}
		t.mu.Unlock()
	}

	uploads := fourchan.Uploads(ep, t.Board, thread.Posts)
	var thumbs []fourchan.ThumbInfo

	if s.SaveHTML {
		// A second request in one check: keep it a second apart from the first.
		select {
		case <-time.After(apiSpacing):
		case <-ctx.Done():
			return queue.Result{}, ctx.Err()
		}
		page, err := t.m.client.GetString(ctx, ep.ThreadHTMLURL(t.Board, t.ID))
		if err != nil {
			t.noteError(err)
			return queue.Result{}, err
		}
		local := make([]fourchan.LocalUpload, len(uploads))
		for i, u := range uploads {
			local[i] = fourchan.LocalUpload{Tim: u.Tim, Ext: u.Ext, Filename: uploadFilename(u, s.ImageFilenameFormat)}
		}
		page = fourchan.FixThreadHTMLLinks(fourchan.PrepareThreadHTML(page), t.Board, local, s.SaveThumbnails)
		dir, err := t.ensureDir()
		if err != nil {
			t.noteError(err)
			return queue.Result{}, err
		}
		if err := config.WriteFileAtomic(filepath.Join(dir, "Thread.html"), []byte(page), 0o644); err != nil {
			t.noteError(err)
			return queue.Result{}, err
		}
		if s.SaveThumbnails {
			thumbs = fourchan.Thumbnails(ep, t.Board, thread.Posts)
		}
	}

	var newItems []queue.Item
	t.mu.Lock()
	for _, u := range uploads {
		id := uploadID(t.ID, u.Tim)
		if _, ok := t.seen[id]; ok {
			continue
		}
		t.seen[id] = struct{}{}
		newItems = append(newItems, &uploadItem{t: t, info: u, id: id})
	}
	for _, th := range thumbs {
		id := thumbnailID(t.Site, t.ID, th.No)
		if _, ok := t.seen[id]; ok {
			continue
		}
		t.seen[id] = struct{}{}
		newItems = append(newItems, &thumbnailItem{t: t, url: th.URL, id: id})
	}
	t.fileCount = len(uploads)
	t.fileCountKnown = true
	t.lane = queue.Default
	// Recorded only now that the results are handled, so a failure above fetches them again next time.
	t.lastModified = res.LastModified
	t.lastCheck = time.Now()
	t.lastError = ""
	t.prioChecked = true
	t.mu.Unlock()
	t.m.touch()
	t.m.checkPriorityDone(t)

	return queue.Result{Enqueue: newItems}, nil
}

// ThreadView is the thread as shown in the UI.
type ThreadView struct {
	Key          string    `json:"key"`
	Site         string    `json:"site"`
	Board        string    `json:"board"`
	ID           int64     `json:"id"`
	URL          string    `json:"url"`
	Subject      string    `json:"subject"`
	Loading      bool      `json:"loading"`
	FileCount    *int      `json:"file_count"`
	Saved        int       `json:"saved"`
	Pending      *int      `json:"pending"`
	Failed       int       `json:"failed,omitempty"`
	Priority     int       `json:"priority,omitempty"`
	LastCheck    time.Time `json:"last_check,omitzero"`
	NextCheck    time.Time `json:"next_check,omitzero"`
	LastError    string    `json:"last_error,omitempty"`
	SaveTo       string    `json:"save_to"`
	SharePath    string    `json:"share_path,omitempty"`
	FolderExists bool      `json:"folder_exists"`
	AddedAt      time.Time `json:"added_at"`
}

func (t *Thread) view(interval time.Duration) ThreadView {
	t.mu.Lock()
	defer t.mu.Unlock()
	saveTo := t.dir
	if saveTo == "" {
		saveTo = t.plannedDirLocked()
	}
	v := ThreadView{
		Key:          t.Key(),
		Site:         t.Site,
		Board:        t.Board,
		ID:           t.ID,
		URL:          t.URL(),
		Subject:      t.displaySubjectLocked(),
		Loading:      !t.subjectSet && !t.fileCountKnown,
		Priority:     t.prio,
		LastCheck:    t.lastCheck,
		LastError:    t.lastError,
		SaveTo:       saveTo,
		SharePath:    SharePath(saveTo),
		FolderExists: t.dir != "",
		AddedAt:      t.AddedAt,
	}
	if !t.lastAttempt.IsZero() {
		v.NextCheck = t.lastAttempt.Add(interval)
	}
	for id := range t.saved {
		if id.Type == AssetUpload {
			v.Saved++
		}
	}
	for id := range t.failed {
		if id.Type == AssetUpload {
			v.Failed++
		}
	}
	if t.fileCountKnown {
		fc := t.fileCount
		v.FileCount = &fc
		pending := t.pendingLocked()
		v.Pending = &pending
	}
	return v
}
