// Package core tracks threads and boards and saves their files.
package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"synochan/internal/config"
	"synochan/internal/fetch"
	"synochan/internal/fourchan"
	"synochan/internal/queue"
)

// PeriodicSaveInterval is how often changed state is written while running, so little is lost on a crash.
const PeriodicSaveInterval = time.Minute

// removalWait is how long a removal waits for the thread's downloads in flight before renaming its folder.
const removalWait = 15 * time.Second

// Options configure a Manager.
type Options struct {
	Config    *config.Store
	Client    *fetch.Client
	Endpoints fourchan.Endpoints
	StatePath string // Where tracked lists are kept. Empty disables persistence.
	Logger    *slog.Logger
}

// Manager owns the tracked threads and boards and the work queue.
type Manager struct {
	ctx       context.Context
	cancel    context.CancelFunc
	cfg       *config.Store
	client    *fetch.Client
	endpoints fourchan.Endpoints
	statePath string
	log       *slog.Logger
	q         *queue.Queue

	mu          sync.Mutex // Guards the lists below. Lock order: mu, then a tracker's own lock.
	threads     []*Thread
	threadByKey map[string]*Thread
	boards      []*Board
	boardByKey  map[string]*Board

	rev    atomic.Uint64
	dirty  atomic.Bool
	saveMu sync.Mutex
	loops  sync.WaitGroup
}

// New creates a manager. Call Start to load saved lists and begin processing.
func New(opts Options) *Manager {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{
		ctx:         ctx,
		cancel:      cancel,
		cfg:         opts.Config,
		client:      opts.Client,
		endpoints:   opts.Endpoints,
		statePath:   opts.StatePath,
		log:         opts.Logger,
		threadByKey: make(map[string]*Thread),
		boardByKey:  make(map[string]*Board),
	}
	m.q = queue.New(queue.Options{
		Limits: func() (int, bool) {
			s := m.cfg.Get()
			return s.MaxConcurrentDownloads, s.Max1RequestPerSecond
		},
		AddTrackers: m.addTrackers,
		Logger:      opts.Logger,
	})
	return m
}

// Start loads saved lists (when enabled) and starts processing.
func (m *Manager) Start() error {
	s := m.cfg.Get()
	m.q.SetPaused(s.StartPaused)

	var loadErr error
	if s.KeepLists && m.statePath != "" {
		st, err := readState(m.statePath)
		if err != nil {
			loadErr = err
			m.log.Error("could not load saved lists", "err", err)
		}
		m.mu.Lock()
		for _, bs := range st.Boards {
			if _, dup := m.boardByKey[boardKey(bs.Site, bs.Code)]; dup || bs.Code == "" {
				continue
			}
			b := m.boardFromState(bs)
			m.boards = append(m.boards, b)
			m.boardByKey[b.Key()] = b
			m.q.Enqueue(b)
		}
		for _, ts := range st.Threads {
			if _, dup := m.threadByKey[threadKey(ts.Site, ts.Board, ts.ID)]; dup || ts.Board == "" || ts.ID <= 0 {
				continue
			}
			t := m.threadFromState(ts)
			m.threads = append(m.threads, t)
			m.threadByKey[t.Key()] = t
			m.q.Enqueue(t)
		}
		m.mu.Unlock()
		m.log.Info("loaded saved lists", "threads", len(st.Threads), "boards", len(st.Boards))
	}
	m.touch()
	m.dirty.Store(false)

	m.loops.Add(2)
	go func() {
		defer m.loops.Done()
		m.q.Run(m.ctx)
	}()
	go func() {
		defer m.loops.Done()
		m.periodicSave()
	}()
	return loadErr
}

// Shutdown stops processing and saves the lists.
func (m *Manager) Shutdown(timeout time.Duration) {
	m.cancel()
	done := make(chan struct{})
	go func() {
		m.loops.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		m.log.Warn("timed out waiting for running work to stop")
	}
	if err := m.SaveNow(); err != nil {
		m.log.Error("could not save lists", "err", err)
	}
}

// Revision changes whenever anything shown in the UI changes.
func (m *Manager) Revision() uint64 { return m.rev.Load() }

func (m *Manager) touch() {
	m.rev.Add(1)
	m.dirty.Store(true)
}

func (m *Manager) periodicSave() {
	ticker := time.NewTicker(PeriodicSaveInterval)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			if m.dirty.Load() {
				if err := m.SaveNow(); err != nil {
					m.log.Error("periodic save failed", "err", err)
				}
			}
		}
	}
}

// SaveNow writes the tracked lists, when keeping lists is enabled.
func (m *Manager) SaveNow() error {
	if m.statePath == "" || !m.cfg.Get().KeepLists {
		return nil
	}
	// Held across collecting and writing, so a save that collected before a removal can't be written after
	// the removal's own save and bring the removed tracker back.
	m.saveMu.Lock()
	defer m.saveMu.Unlock()

	m.dirty.Store(false)
	m.mu.Lock()
	st := stateFile{
		Threads: make([]threadState, 0, len(m.threads)),
		Boards:  make([]boardState, 0, len(m.boards)),
	}
	for _, b := range m.boards {
		st.Boards = append(st.Boards, b.state())
	}
	for _, t := range m.threads {
		st.Threads = append(st.Threads, t.state())
	}
	m.mu.Unlock()
	return writeState(m.statePath, st)
}

func (m *Manager) saveLogged() {
	if err := m.SaveNow(); err != nil {
		m.log.Error("could not save lists", "err", err)
	}
}

// AddResult reports what happened to one entered URL.
type AddResult struct {
	Input     string `json:"input"`
	Status    string `json:"status"` // "added", "duplicate" or "invalid"
	Kind      string `json:"kind,omitempty"`
	Key       string `json:"key,omitempty"`
	SaveTo    string `json:"save_to,omitempty"`
	SharePath string `json:"share_path,omitempty"`
}

// AddURLs adds every thread and board URL found in text. Entries may be separated by commas, spaces or new lines.
func (m *Manager) AddURLs(text string) []AddResult {
	s := m.cfg.Get()
	var results []AddResult
	var toQueue []queue.Item

	m.mu.Lock()
	for _, input := range fourchan.SplitURLs(text) {
		if ref, ok := fourchan.ParseThreadURL(input); ok {
			key := threadKey(fourchan.SiteName, ref.Board, ref.ID)
			if existing, dup := m.threadByKey[key]; dup {
				results = append(results, AddResult{Input: input, Status: "duplicate", Kind: "thread", Key: key, SaveTo: existing.SaveTo, SharePath: SharePath(existing.SaveTo)})
				continue
			}
			t := m.newThread(ref.Board, ref.ID, threadSaveDir(s.SavePath, ref.Board, ref.ID))
			t.priority = queue.High
			if ref.Subject != "" {
				t.subject, t.subjectSet = ref.Subject, true
			}
			m.threads = append(m.threads, t)
			m.threadByKey[key] = t
			toQueue = append(toQueue, t)
			results = append(results, AddResult{Input: input, Status: "added", Kind: "thread", Key: key, SaveTo: t.SaveTo, SharePath: SharePath(t.SaveTo)})
			continue
		}
		if code, ok := fourchan.ParseBoardURL(input); ok {
			key := boardKey(fourchan.SiteName, code)
			if existing, dup := m.boardByKey[key]; dup {
				results = append(results, AddResult{Input: input, Status: "duplicate", Kind: "board", Key: key, SaveTo: existing.SaveTo, SharePath: SharePath(existing.SaveTo)})
				continue
			}
			b := m.newBoard(code, boardSaveDir(s.SavePath, code))
			m.boards = append(m.boards, b)
			m.boardByKey[key] = b
			toQueue = append(toQueue, b)
			results = append(results, AddResult{Input: input, Status: "added", Kind: "board", Key: key, SaveTo: b.SaveTo, SharePath: SharePath(b.SaveTo)})
			continue
		}
		results = append(results, AddResult{Input: input, Status: "invalid"})
	}
	m.mu.Unlock()

	for _, item := range toQueue {
		m.q.Enqueue(item)
	}
	if len(toQueue) > 0 {
		m.touch()
		// Saved straight away, so trackers the user adds survive the process being killed.
		m.saveLogged()
	}
	return results
}

// addTrackers is the queue callback for threads found on a board.
func (m *Manager) addTrackers(source queue.Item, items []queue.Item, onAdded func()) bool {
	var added []*Thread
	m.mu.Lock()
	// Checked under the lock: a board removed meanwhile must not add its results afterwards.
	if !source.ShouldProcess() {
		m.mu.Unlock()
		return false
	}
	for _, item := range items {
		t, ok := item.(*Thread)
		if !ok {
			continue
		}
		if _, dup := m.threadByKey[t.Key()]; dup {
			t.cancel()
			continue
		}
		m.threads = append(m.threads, t)
		m.threadByKey[t.Key()] = t
		added = append(added, t)
	}
	if onAdded != nil {
		onAdded()
	}
	m.mu.Unlock()

	for _, t := range added {
		m.q.Enqueue(t)
	}
	if len(added) > 0 {
		m.log.Info("added threads from board", "source", source.String(), "count", len(added))
	}
	m.touch()
	return true
}

// ErrNotFound is returned for unknown keys.
var ErrNotFound = errors.New("not tracked")

// RemoveThread stops tracking a thread and, when enabled, renames its folder to include the subject.
func (m *Manager) RemoveThread(key string) error {
	m.mu.Lock()
	t := m.threadByKey[key]
	m.mu.Unlock()
	if t == nil {
		return ErrNotFound
	}
	return m.removeThread(t, true)
}

func (m *Manager) removeThread(t *Thread, manual bool) error {
	t.cancel()

	m.mu.Lock()
	if m.threadByKey[t.Key()] == t {
		delete(m.threadByKey, t.Key())
		for i, x := range m.threads {
			if x == t {
				m.threads = append(m.threads[:i], m.threads[i+1:]...)
				break
			}
		}
	}
	m.mu.Unlock()
	m.q.Purge()
	m.touch()
	m.saveLogged()

	s := m.cfg.Get()
	if !s.AddThreadSubjectToFolder {
		return nil
	}

	// Let downloads in flight stop before the folder is moved, so none lands in the old place.
	done := make(chan struct{})
	go func() {
		t.active.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(removalWait):
		m.log.Warn("downloads still running, renaming folder anyway", "thread", t.Key())
	}

	newPath, err := moveThreadDir(t.SaveTo, t.DisplaySubject(), t.ID, s.ThreadFolderNameFormat)
	if err != nil {
		m.log.Error("could not rename thread folder", "thread", t.Key(), "manual", manual, "err", err)
		return fmt.Errorf("thread removed, but its folder could not be renamed: %w", err)
	}
	if newPath != "" {
		m.log.Info("renamed thread folder", "thread", t.Key(), "to", newPath)
	}
	return nil
}

// ClearThreads removes all threads. Folder rename errors are collected, removal continues.
func (m *Manager) ClearThreads() []error {
	m.mu.Lock()
	all := append([]*Thread(nil), m.threads...)
	m.mu.Unlock()

	var (
		wg   sync.WaitGroup
		emu  sync.Mutex
		errs []error
	)
	sem := make(chan struct{}, 8)
	for _, t := range all {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if err := m.removeThread(t, true); err != nil {
				emu.Lock()
				errs = append(errs, fmt.Errorf("%s: %w", t.Key(), err))
				emu.Unlock()
			}
		}()
	}
	wg.Wait()
	return errs
}

// RemoveBoard stops tracking a board. Threads it added stay tracked.
func (m *Manager) RemoveBoard(key string) error {
	m.mu.Lock()
	b := m.boardByKey[key]
	if b == nil {
		m.mu.Unlock()
		return ErrNotFound
	}
	b.cancel()
	delete(m.boardByKey, key)
	for i, x := range m.boards {
		if x == b {
			m.boards = append(m.boards[:i], m.boards[i+1:]...)
			break
		}
	}
	m.mu.Unlock()
	m.q.Purge()
	m.touch()
	m.saveLogged()
	return nil
}

// ClearBoards removes all boards.
func (m *Manager) ClearBoards() {
	m.mu.Lock()
	for _, b := range m.boards {
		b.cancel()
	}
	m.boards = nil
	m.boardByKey = make(map[string]*Board)
	m.mu.Unlock()
	m.q.Purge()
	m.touch()
	m.saveLogged()
}

// RenameThread sets a custom subject. An empty subject shows as NoSubject.
func (m *Manager) RenameThread(key, subject string) error {
	m.mu.Lock()
	t := m.threadByKey[key]
	m.mu.Unlock()
	if t == nil {
		return ErrNotFound
	}
	t.SetSubject(subject)
	m.saveLogged()
	return nil
}

// SetPaused pauses or resumes all scraping and downloading.
func (m *Manager) SetPaused(paused bool) {
	m.q.SetPaused(paused)
	m.touch()
}

// Settings returns the current settings.
func (m *Manager) Settings() config.Settings { return m.cfg.Get() }

// UpdateSettings validates and applies new settings. A new save folder applies to trackers added afterwards.
func (m *Manager) UpdateSettings(s config.Settings) error {
	s.SavePath = strings.TrimRight(strings.TrimSpace(s.SavePath), "/")
	s.UserAgent = strings.TrimSpace(s.UserAgent)
	if err := s.Validate(); err != nil {
		return err
	}
	if err := CheckWritable(s.SavePath); err != nil {
		return err
	}
	old := m.cfg.Get()
	if err := m.cfg.Set(s); err != nil {
		return err
	}
	m.q.Purge() // Thumbnails stop being wanted when they are turned off.
	m.touch()
	if s.KeepLists && !old.KeepLists {
		// Save everything now, so trackers added while saving was off are kept too.
		m.saveLogged()
	}
	return nil
}

// Snapshot is everything the UI shows.
type Snapshot struct {
	Rev     uint64       `json:"rev"`
	Queue   queue.Status `json:"queue"`
	Threads []ThreadView `json:"threads"`
	Boards  []BoardView  `json:"boards"`
}

// Snapshot returns the current state for display.
func (m *Manager) Snapshot() Snapshot {
	interval := time.Duration(m.cfg.Get().MinSecondsBetweenScrapes) * time.Second
	rev := m.rev.Load()
	m.mu.Lock()
	threads := append([]*Thread(nil), m.threads...)
	boards := append([]*Board(nil), m.boards...)
	m.mu.Unlock()

	s := Snapshot{
		Rev:     rev,
		Queue:   m.q.Status(),
		Threads: make([]ThreadView, 0, len(threads)),
		Boards:  make([]BoardView, 0, len(boards)),
	}
	for _, t := range threads {
		s.Threads = append(s.Threads, t.view(interval))
	}
	for _, b := range boards {
		s.Boards = append(s.Boards, b.view(interval))
	}
	return s
}

// QueueStatus returns the queue state alone (cheap, for polling).
func (m *Manager) QueueStatus() queue.Status { return m.q.Status() }

// URLs returns the public URLs of all tracked threads or boards, for copying.
func (m *Manager) URLs(kind string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	if kind == "boards" {
		for _, b := range m.boards {
			out = append(out, b.URL())
		}
	} else {
		for _, t := range m.threads {
			out = append(out, t.URL())
		}
	}
	return out
}
