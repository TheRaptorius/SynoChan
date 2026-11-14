package core

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"synochan/internal/config"
	"synochan/internal/fetch"
	"synochan/internal/fourchan"
	"synochan/internal/queue"
)

// Board tracks a board: new threads that appear on it are added to the thread list.
type Board struct {
	m       *Manager
	Site    string
	Code    string
	SaveTo  string
	AddedAt time.Time

	ctx    context.Context
	cancel context.CancelFunc

	mu               sync.Mutex
	greatestThreadID int64 // Threads up to this id were already added; older ones are ignored.
	threadCount      int
	lastModified     time.Time
	lastAttempt      time.Time
	lastCheck        time.Time
	lastError        string
	filters          config.BoardFilters // The board's own filters, on top of the global ones.
	pending          bool                // Added back with remembered filters; waits for the user's decision.
	// dismissed are threads of this board the user removed; a board never adds them again. Only ids still in
	// the catalog are kept, so the set stays small.
	dismissed map[int64]bool
	rescan    bool // Read the catalog in full at the next check, as the filters changed.
	// seedDismissed: the board comes from a version that did not record removals. At its first check, threads
	// it saw before that are allowed but not tracked must have been removed by the user; they are recorded so.
	seedDismissed bool
	pendingTimer  *time.Timer
	catalog       []catalogEntry // Threads of the last catalog read, for listing filtered ones.
}

// catalogEntry is what filters need to know about a thread on a board.
type catalogEntry struct {
	ID      int64
	Subject string // As shown in the UI.
	FilterS string // Subject and name, searched by filters.
	Text    string // Opening post text, searched by filters.
	Sticky  bool
}

// FilteredThread is a thread on a board that the board's filters keep out.
type FilteredThread struct {
	ID      int64  `json:"id"`
	Subject string `json:"subject"`
	Reason  string `json:"reason"`
	URL     string `json:"url"`
}

func (m *Manager) newBoard(code, saveTo string) *Board {
	ctx, cancel := context.WithCancel(m.ctx)
	return &Board{
		m:         m,
		Site:      fourchan.SiteName,
		Code:      code,
		SaveTo:    saveTo,
		AddedAt:   time.Now(),
		ctx:       ctx,
		cancel:    cancel,
		dismissed: make(map[int64]bool),
	}
}

// Key identifies the board, e.g. "4chan/g".
func (b *Board) Key() string { return boardKey(b.Site, b.Code) }

func boardKey(site, code string) string { return site + "/" + code }

// URL is the public address of the board.
func (b *Board) URL() string { return fourchan.BoardURL(b.Code) }

func (b *Board) String() string { return "Board " + b.Key() }

func (b *Board) Context() context.Context { return b.ctx }
func (b *Board) ShouldProcess() bool      { return b.ctx.Err() == nil }
func (b *Board) IsAsset() bool            { return false }
func (b *Board) Order() queue.Order       { return queue.Order{Rank: queue.NoRank, Lane: queue.Default} }

func (b *Board) ReadyAt() time.Time {
	b.mu.Lock()
	last := b.lastAttempt
	b.mu.Unlock()
	if last.IsZero() {
		return time.Time{}
	}
	return last.Add(time.Duration(b.m.cfg.Get().MinSecondsBetweenScrapes) * time.Second)
}

func (b *Board) noteError(err error) {
	b.mu.Lock()
	b.lastError = err.Error()
	b.mu.Unlock()
	b.m.touch()
}

// Process implements queue.Item: reads the catalog and hands threads newer than any seen before to the manager.
func (b *Board) Process(ctx context.Context) (queue.Result, error) {
	if !b.ShouldProcess() {
		return queue.Result{Remove: true}, nil
	}
	b.mu.Lock()
	b.lastAttempt = time.Now()
	since := b.lastModified
	if b.rescan {
		since = time.Time{} // Filters changed: read the whole catalog even if the board did not change.
		b.rescan = false
	}
	greatest := b.greatestThreadID
	b.mu.Unlock()
	b.m.touch()

	res, err := b.m.client.GetIfModified(ctx, b.m.endpoints.CatalogURL(b.Code), since)
	if err != nil {
		if fetch.IsGone(err) {
			// Keep the board and keep retrying (with backoff); it may be a temporary error or a typo the user fixes.
			err = fmt.Errorf("board /%s/ was not found", b.Code)
		}
		b.noteError(err)
		return queue.Result{}, err
	}

	finish := func(lastModified time.Time, threadCount int) {
		b.mu.Lock()
		b.lastModified = lastModified
		if threadCount >= 0 {
			b.threadCount = threadCount
		}
		b.lastCheck = time.Now()
		b.lastError = ""
		b.mu.Unlock()
		b.m.touch()
	}

	if res.NotModified {
		finish(res.LastModified, -1)
		return queue.Result{}, nil
	}

	var pages []fourchan.CatalogPage
	if err := json.Unmarshal(res.Body, &pages); err != nil {
		err = fmt.Errorf("parse catalog JSON: %w", err)
		b.noteError(err)
		return queue.Result{}, err
	}

	rules := b.m.boardRules(b)
	tracked := b.m.trackedIDs()
	b.mu.Lock()
	seed := b.seedDismissed
	dismissed := make(map[int64]bool, len(b.dismissed))
	for id := range b.dismissed {
		dismissed[id] = true
	}
	b.mu.Unlock()
	live := map[int64]bool{}
	total := 0
	var catalog []catalogEntry
	var newThreads []queue.Item
	var greatestNew int64
	saveRoot := b.m.cfg.Get().SavePath
	for _, page := range pages {
		for i := range page.Threads {
			p := &page.Threads[i]
			total++
			filterSubject, text := fourchan.FilterText(p)
			entry := catalogEntry{ID: p.No, Subject: fourchan.ExtractSubject(p), FilterS: filterSubject, Text: text, Sticky: p.Sticky != 0}
			catalog = append(catalog, entry)
			live[p.No] = true
			verdict := rules.Check(entry.FilterS, entry.Text, entry.Sticky)
			if p.No <= greatest {
				// Seen before. Add it now only if it was filtered then and the filters let it in now: it is not
				// tracked, and the user did not remove it.
				if !verdict.Allowed || tracked[threadKey(b.Site, b.Code, p.No)] || dismissed[p.No] {
					continue
				}
				if seed {
					dismissed[p.No] = true
					continue
				}
				b.m.log.Info("thread let in by changed filters", "board", b.Code, "thread", p.No, "subject", entry.Subject)
			} else {
				// Filtered threads count as seen too, so they are judged (and logged) once.
				greatestNew = max(greatestNew, p.No)
				if !verdict.Allowed {
					b.m.log.Info("thread filtered", "board", b.Code, "thread", p.No, "reason", verdict.Reason, "subject", entry.Subject)
					continue
				}
			}
			t := b.m.newThread(b.Code, p.No, saveRoot)
			t.FromBoard = b.Key()
			t.matchSubject, t.matchText = entry.FilterS, entry.Text
			if entry.Subject != "" {
				t.subject, t.subjectSet = entry.Subject, true
			}
			// The catalog does not count the opening post's file.
			t.fileCount, t.fileCountKnown = p.Images+1, true
			newThreads = append(newThreads, t)
		}
	}

	b.mu.Lock()
	b.catalog = catalog
	if seed {
		b.seedDismissed = false
		for id := range dismissed {
			b.dismissed[id] = true
		}
	}
	for id := range b.dismissed {
		if !live[id] {
			delete(b.dismissed, id) // Gone from the board: nothing left to keep out.
		}
	}
	b.threadCount = total
	if len(newThreads) == 0 {
		b.greatestThreadID = max(b.greatestThreadID, greatestNew)
	}
	b.mu.Unlock()

	if len(newThreads) == 0 {
		finish(res.LastModified, total)
		return queue.Result{}, nil
	}

	lastModified := res.LastModified
	return queue.Result{
		Trackers: newThreads,
		// Recorded only once the threads are in the list, so a save never stores the new position without them,
		// and if adding fails the next scrape finds them again instead of skipping them or getting a 304.
		OnTrackersAdded: func() {
			b.mu.Lock()
			b.greatestThreadID = max(b.greatestThreadID, greatestNew)
			b.mu.Unlock()
			finish(lastModified, total)
		},
	}, nil
}

// BoardView is the board as shown in the UI.
type BoardView struct {
	Key          string    `json:"key"`
	Site         string    `json:"site"`
	Code         string    `json:"code"`
	URL          string    `json:"url"`
	ThreadCount  int       `json:"thread_count"`
	LastCheck    time.Time `json:"last_check,omitzero"`
	NextCheck    time.Time `json:"next_check,omitzero"`
	LastError    string    `json:"last_error,omitempty"`
	SaveTo       string    `json:"save_to"`
	SharePath    string    `json:"share_path,omitempty"`
	FolderExists bool      `json:"folder_exists"`
	AddedAt      time.Time `json:"added_at"`
	// Filters are the board's own filters; Filtered counts live threads the filters keep out.
	Filters  config.BoardFilters `json:"filters"`
	Filtered int                 `json:"filtered"`
	// Pending boards wait for the user to decide about remembered filters before their first check.
	Pending bool `json:"pending,omitempty"`
}

func (b *Board) view(interval time.Duration) BoardView {
	b.mu.Lock()
	defer b.mu.Unlock()
	v := BoardView{
		Key:         b.Key(),
		Site:        b.Site,
		Code:        b.Code,
		URL:         b.URL(),
		ThreadCount: b.threadCount,
		LastCheck:   b.lastCheck,
		LastError:   b.lastError,
		SaveTo:      b.SaveTo,
		SharePath:   SharePath(b.SaveTo),
		AddedAt:     b.AddedAt,
		Filters:     b.filters,
		Pending:     b.pending,
	}
	if info, err := os.Stat(b.SaveTo); err == nil && info.IsDir() {
		v.FolderExists = true
	}
	if !b.lastAttempt.IsZero() {
		v.NextCheck = b.lastAttempt.Add(interval)
	}
	return v
}
