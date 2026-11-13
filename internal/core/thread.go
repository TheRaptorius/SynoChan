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

// Thread tracks one thread: it is rescraped periodically and queues downloads for new files.
type Thread struct {
	m       *Manager
	Site    string
	Board   string
	ID      int64
	SaveTo  string
	AddedAt time.Time

	ctx    context.Context
	cancel context.CancelFunc
	active sync.WaitGroup // Downloads of this thread in flight.

	mu             sync.Mutex
	subject        string
	subjectSet     bool // Subject known (scraped, from the catalog or set by the user), possibly empty.
	fileCount      int
	fileCountKnown bool
	lastModified   time.Time // Server time of the last successful scrape, sent as If-Modified-Since.
	lastAttempt    time.Time // Local time the last scrape started, for spacing scrapes out.
	lastCheck      time.Time // Local time of the last successful scrape, for display.
	lastError      string
	seen           map[AssetID]struct{} // Assets that were queued and need no further processing.
	saved          map[AssetID]struct{} // Assets that were saved to disk.
	priority       queue.Priority
}

func (m *Manager) newThread(board string, id int64, saveTo string) *Thread {
	ctx, cancel := context.WithCancel(m.ctx)
	return &Thread{
		m:        m,
		Site:     fourchan.SiteName,
		Board:    board,
		ID:       id,
		SaveTo:   saveTo,
		AddedAt:  time.Now(),
		ctx:      ctx,
		cancel:   cancel,
		seen:     make(map[AssetID]struct{}),
		saved:    make(map[AssetID]struct{}),
		priority: queue.Default,
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

// Priority implements queue.Item. New threads go first so their subject and file count show up quickly.
func (t *Thread) Priority() queue.Priority {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.priority
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

// DisplaySubject is "Loading…" until anything is known, then the subject or NoSubject.
func (t *Thread) DisplaySubject() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.displaySubjectLocked()
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

// SetSubject renames the thread.
func (t *Thread) SetSubject(subject string) {
	t.mu.Lock()
	t.subject = fourchan.SanitiseSubject(subject)
	t.subjectSet = true
	t.mu.Unlock()
	t.m.touch()
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
}

// Process implements queue.Item: scrapes the thread and queues downloads for files not seen before.
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
			go t.m.removeThread(t, false)
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
		t.mu.Unlock()
		t.m.touch()
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

	subject := ""
	if needSubject {
		subject = fourchan.ExtractSubject(&thread.Posts[0])
	}

	uploads := fourchan.Uploads(ep, t.Board, thread.Posts)
	var thumbs []fourchan.ThumbInfo

	if s.SaveHTML {
		if s.Max1RequestPerSecond {
			// Two requests in one item: keep them a second apart as well.
			select {
			case <-time.After(time.Second):
			case <-ctx.Done():
				return queue.Result{}, ctx.Err()
			}
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
		if err := os.MkdirAll(t.SaveTo, 0o755); err != nil {
			t.noteError(err)
			return queue.Result{}, err
		}
		if err := config.WriteFileAtomic(filepath.Join(t.SaveTo, "Thread.html"), []byte(page), 0o644); err != nil {
			t.noteError(err)
			return queue.Result{}, err
		}
		if s.SaveThumbnails {
			thumbs = fourchan.Thumbnails(ep, t.Board, thread.Posts)
		}
	}

	var newItems []queue.Item
	t.mu.Lock()
	if needSubject && !t.subjectSet {
		t.subject = subject
		t.subjectSet = true
	}
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
	t.priority = queue.Default
	// Recorded only now that the results are handled, so a failure above fetches them again next time.
	t.lastModified = res.LastModified
	t.lastCheck = time.Now()
	t.lastError = ""
	t.mu.Unlock()
	t.m.touch()

	return queue.Result{Enqueue: newItems}, nil
}

// ThreadView is the thread as shown in the UI.
type ThreadView struct {
	Key       string    `json:"key"`
	Site      string    `json:"site"`
	Board     string    `json:"board"`
	ID        int64     `json:"id"`
	URL       string    `json:"url"`
	Subject   string    `json:"subject"`
	Loading   bool      `json:"loading"`
	FileCount *int      `json:"file_count"`
	Saved     int       `json:"saved"`
	Pending   *int      `json:"pending"`
	LastCheck time.Time `json:"last_check,omitzero"`
	NextCheck time.Time `json:"next_check,omitzero"`
	LastError string    `json:"last_error,omitempty"`
	SaveTo    string    `json:"save_to"`
	SharePath string    `json:"share_path,omitempty"`
	AddedAt   time.Time `json:"added_at"`
}

func (t *Thread) view(interval time.Duration) ThreadView {
	t.mu.Lock()
	defer t.mu.Unlock()
	v := ThreadView{
		Key:       t.Key(),
		Site:      t.Site,
		Board:     t.Board,
		ID:        t.ID,
		URL:       t.URL(),
		Subject:   t.displaySubjectLocked(),
		Loading:   !t.subjectSet && !t.fileCountKnown,
		LastCheck: t.lastCheck,
		LastError: t.lastError,
		SaveTo:    t.SaveTo,
		SharePath: SharePath(t.SaveTo),
		AddedAt:   t.AddedAt,
	}
	if !t.lastAttempt.IsZero() {
		v.NextCheck = t.lastAttempt.Add(interval)
	}
	savedUploads, seenUploads := 0, 0
	for id := range t.saved {
		if id.Type == AssetUpload {
			savedUploads++
		}
	}
	for id := range t.seen {
		if id.Type == AssetUpload {
			seenUploads++
		}
	}
	v.Saved = savedUploads
	if t.fileCountKnown {
		fc := t.fileCount
		v.FileCount = &fc
		pending := max(0, seenUploads-savedUploads)
		v.Pending = &pending
	}
	return v
}
