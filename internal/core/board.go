package core

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

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
}

func (m *Manager) newBoard(code, saveTo string) *Board {
	ctx, cancel := context.WithCancel(m.ctx)
	return &Board{
		m:       m,
		Site:    fourchan.SiteName,
		Code:    code,
		SaveTo:  saveTo,
		AddedAt: time.Now(),
		ctx:     ctx,
		cancel:  cancel,
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

	total := 0
	var newThreads []queue.Item
	var greatestNew int64
	saveRoot := b.m.cfg.Get().SavePath
	for _, page := range pages {
		for i := range page.Threads {
			p := &page.Threads[i]
			total++
			if p.No <= greatest {
				continue
			}
			t := b.m.newThread(b.Code, p.No, saveRoot)
			if subject := fourchan.ExtractSubject(p); subject != "" {
				t.subject, t.subjectSet = subject, true
			}
			// The catalog does not count the opening post's file.
			t.fileCount, t.fileCountKnown = p.Images+1, true
			newThreads = append(newThreads, t)
			greatestNew = max(greatestNew, p.No)
		}
	}

	if len(newThreads) == 0 {
		finish(res.LastModified, total)
		return queue.Result{}, nil
	}

	b.mu.Lock()
	b.threadCount = total
	b.mu.Unlock()

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
	}
	if info, err := os.Stat(b.SaveTo); err == nil && info.IsDir() {
		v.FolderExists = true
	}
	if !b.lastAttempt.IsZero() {
		v.NextCheck = b.lastAttempt.Add(interval)
	}
	return v
}
