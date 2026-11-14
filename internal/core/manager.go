// Package core tracks threads and boards and saves their files.
package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"synochan/internal/config"
	"synochan/internal/fetch"
	"synochan/internal/filter"
	"synochan/internal/fourchan"
	"synochan/internal/queue"
)

// PeriodicSaveInterval is how often changed state is written while running, so little is lost on a crash.
const PeriodicSaveInterval = time.Minute

// Options configure a Manager.
type Options struct {
	Config    *config.Store
	Client    *fetch.Client
	Endpoints fourchan.Endpoints
	StatePath string // Where tracked lists are kept. Empty disables persistence.
	Logger    *slog.Logger
	// TrackerSpacing overrides the one second between API requests (tests only).
	TrackerSpacing time.Duration
	// LogLevel is switched between info and debug by the "detailed logging" setting.
	LogLevel *slog.LevelVar
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
	logLevel  *slog.LevelVar
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
		logLevel:    opts.LogLevel,
		threadByKey: make(map[string]*Thread),
		boardByKey:  make(map[string]*Board),
	}
	m.q = queue.New(queue.Options{
		Limits: func() queue.Limits {
			s := m.cfg.Get()
			return queue.Limits{MaxDownloads: s.MaxConcurrentDownloads, SpaceDownloads: s.SpaceDownloads}
		},
		AddTrackers:    m.addTrackers,
		TrackerSpacing: opts.TrackerSpacing,
		Logger:         opts.Logger,
	})
	return m
}

// Start loads saved lists (when enabled) and starts processing.
func (m *Manager) Start() error {
	s := m.cfg.Get()
	m.q.SetPaused(s.StartPaused)
	m.applyLogLevel(s)

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
	Input        string `json:"input"`
	Status       string `json:"status"` // "added", "duplicate" or "invalid"
	Kind         string `json:"kind,omitempty"`
	Key          string `json:"key,omitempty"`
	SaveTo       string `json:"save_to,omitempty"`
	SharePath    string `json:"share_path,omitempty"`
	FolderExists bool   `json:"folder_exists,omitempty"`
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
				v := existing.view(0)
				results = append(results, AddResult{Input: input, Status: "duplicate", Kind: "thread", Key: key, SaveTo: v.SaveTo, SharePath: v.SharePath, FolderExists: v.FolderExists})
				continue
			}
			t := m.newThread(ref.Board, ref.ID, s.SavePath)
			t.lane = queue.High
			if ref.Subject != "" {
				t.subject, t.subjectSet = ref.Subject, true
			}
			m.threads = append(m.threads, t)
			m.threadByKey[key] = t
			toQueue = append(toQueue, t)
			results = append(results, AddResult{Input: input, Status: "added", Kind: "thread", Key: key})
			continue
		}
		if code, ok := fourchan.ParseBoardURL(input); ok {
			key := boardKey(fourchan.SiteName, code)
			if existing, dup := m.boardByKey[key]; dup {
				v := existing.view(0)
				results = append(results, AddResult{Input: input, Status: "duplicate", Kind: "board", Key: key, SaveTo: v.SaveTo, SharePath: v.SharePath, FolderExists: v.FolderExists})
				continue
			}
			b := m.newBoard(code, boardSaveDir(s.SavePath, code))
			m.boards = append(m.boards, b)
			m.boardByKey[key] = b
			toQueue = append(toQueue, b)
			results = append(results, AddResult{Input: input, Status: "added", Kind: "board", Key: key})
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

func (m *Manager) applyLogLevel(s config.Settings) {
	if m.logLevel == nil {
		return
	}
	if s.DebugLogging {
		m.logLevel.Set(slog.LevelDebug)
	} else {
		m.logLevel.Set(slog.LevelInfo)
	}
}

// RemoveThread stops tracking a thread. With deleteFiles, its folder is deleted too; otherwise it stays.
func (m *Manager) RemoveThread(key string, deleteFiles bool) error {
	m.mu.Lock()
	t := m.threadByKey[key]
	m.mu.Unlock()
	if t == nil {
		return ErrNotFound
	}
	m.removeThread(t)
	if deleteFiles {
		return m.deleteThreadDir(t)
	}
	return nil
}

// deleteThreadDir deletes a removed thread's folder, once writes in flight have stopped. Only a folder
// directly inside <root>/4chan/<board> is ever deleted.
func (m *Manager) deleteThreadDir(t *Thread) error {
	done := make(chan struct{})
	go func() {
		t.active.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		return fmt.Errorf("files of %s are still being written, folder not deleted", t.Key())
	}
	t.mu.Lock()
	dir := t.dir
	t.mu.Unlock()
	if dir == "" {
		return nil // Nothing was saved yet.
	}
	base := boardSaveDir(t.Root, t.Board)
	rel, err := filepath.Rel(base, dir)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || strings.ContainsRune(rel, filepath.Separator) {
		return fmt.Errorf("refusing to delete %s: not a thread folder inside %s", dir, base)
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("delete %s: %w", dir, err)
	}
	m.log.Info("deleted thread folder", "thread", t.Key(), "folder", dir)
	return nil
}

func (m *Manager) removeThread(t *Thread) {
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
	m.renumberPriorities()
	m.q.Purge()
	m.touch()
	m.saveLogged()
}

// ClearThreads removes all threads, with deleteFiles also their folders. Folder errors are returned,
// removal goes on regardless.
func (m *Manager) ClearThreads(deleteFiles bool) []error {
	m.mu.Lock()
	all := m.threads
	for _, t := range all {
		t.cancel()
	}
	m.threads = nil
	m.threadByKey = make(map[string]*Thread)
	m.mu.Unlock()
	m.q.Purge()
	m.touch()
	m.saveLogged()
	var errs []error
	if deleteFiles {
		for _, t := range all {
			if err := m.deleteThreadDir(t); err != nil {
				errs = append(errs, err)
			}
		}
	}
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
	err := t.SetSubject(subject)
	m.saveLogged()
	return err
}

// Prioritize gives threads a download priority, in the order given. With first, they go before all
// prioritised threads, otherwise after them. Already prioritised threads are moved.
func (m *Manager) Prioritize(keys []string, first bool) error {
	m.mu.Lock()
	var picked []*Thread
	seen := map[*Thread]bool{}
	for _, k := range keys {
		if t := m.threadByKey[k]; t != nil && !seen[t] {
			picked = append(picked, t)
			seen[t] = true
		}
	}
	if len(picked) == 0 {
		m.mu.Unlock()
		return ErrNotFound
	}
	var others []*Thread
	for _, t := range m.prioritizedLocked() {
		if !seen[t] {
			others = append(others, t)
		}
	}
	ordered := append(others, picked...)
	if first {
		ordered = append(picked, others...)
	}
	for i, t := range ordered {
		t.mu.Lock()
		t.prio = i + 1
		if seen[t] {
			// Check right away, so the priority covers the thread's current files.
			t.prioChecked = false
			t.lastAttempt = time.Time{}
		}
		t.mu.Unlock()
	}
	m.mu.Unlock()
	for _, t := range picked {
		m.q.Enqueue(t)
	}
	m.q.Wake()
	m.touch()
	m.saveLogged()
	return nil
}

// Unprioritize removes threads' download priority.
func (m *Manager) Unprioritize(keys []string) {
	m.mu.Lock()
	for _, k := range keys {
		if t := m.threadByKey[k]; t != nil {
			t.mu.Lock()
			t.prio = 0
			t.mu.Unlock()
		}
	}
	m.mu.Unlock()
	m.renumberPriorities()
	m.q.Wake()
	m.touch()
	m.saveLogged()
}

// prioritizedLocked returns the prioritised threads by priority.
func (m *Manager) prioritizedLocked() []*Thread {
	var out []*Thread
	for _, t := range m.threads {
		t.mu.Lock()
		p := t.prio
		t.mu.Unlock()
		if p > 0 {
			out = append(out, t)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		out[i].mu.Lock()
		a := out[i].prio
		out[i].mu.Unlock()
		out[j].mu.Lock()
		b := out[j].prio
		out[j].mu.Unlock()
		return a < b
	})
	return out
}

// renumberPriorities closes gaps, so priorities stay 1, 2, 3, ….
func (m *Manager) renumberPriorities() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, t := range m.prioritizedLocked() {
		t.mu.Lock()
		t.prio = i + 1
		t.mu.Unlock()
	}
}

// checkPriorityDone ends a thread's priority once a check finished after it was prioritised and every file
// found so far is saved (or given up on). Files found by later checks before that keep the priority going.
func (m *Manager) checkPriorityDone(t *Thread) {
	t.mu.Lock()
	done := t.prio > 0 && t.prioChecked && t.fileCountKnown && t.pendingLocked() == 0
	if done {
		t.prio = 0
	}
	t.mu.Unlock()
	if done {
		m.log.Info("priority download finished", "thread", t.Key())
		m.renumberPriorities()
		m.touch()
		m.saveLogged()
	}
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
	m.applyLogLevel(s)
	m.q.Purge() // Thumbnails stop being wanted when they are turned off.
	m.q.Wake()  // Download limits may have changed.
	m.touch()
	if s.KeepLists && !old.KeepLists {
		// Save everything now, so trackers added while saving was off are kept too.
		m.saveLogged()
	}
	return nil
}

// Snapshot is everything the UI shows.
type Snapshot struct {
	Rev   uint64       `json:"rev"`
	Queue queue.Status `json:"queue"`
	// Save folder for newly added threads and boards.
	SavePath      string       `json:"save_path"`
	SaveSharePath string       `json:"save_share_path,omitempty"`
	SaveExists    bool         `json:"save_exists"`
	Threads       []ThreadView `json:"threads"`
	Boards        []BoardView  `json:"boards"`
}

// Snapshot returns the current state for display.
func (m *Manager) Snapshot() Snapshot {
	interval := time.Duration(m.cfg.Get().MinSecondsBetweenScrapes) * time.Second
	rev := m.rev.Load()
	m.mu.Lock()
	threads := append([]*Thread(nil), m.threads...)
	boards := append([]*Board(nil), m.boards...)
	m.mu.Unlock()

	savePath := m.cfg.Get().SavePath
	s := Snapshot{
		Rev:           rev,
		Queue:         m.q.Status(),
		SavePath:      savePath,
		SaveSharePath: SharePath(savePath),
		Threads:       make([]ThreadView, 0, len(threads)),
		Boards:        make([]BoardView, 0, len(boards)),
	}
	for _, t := range threads {
		s.Threads = append(s.Threads, t.view(interval))
	}
	tracked := m.trackedIDs()
	for _, b := range boards {
		v := b.view(interval)
		v.Filtered = len(m.filteredOn(b, tracked))
		s.Boards = append(s.Boards, v)
	}
	if info, err := os.Stat(savePath); err == nil && info.IsDir() {
		s.SaveExists = true
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

// boardRules returns the filters that apply to a board: the global ones plus the board's own.
// Entries were validated when saved, so compile errors are only logged.
func (m *Manager) boardRules(b *Board) filter.Rules {
	s := m.cfg.Get()
	var own config.BoardFilters
	if b != nil {
		b.mu.Lock()
		own = b.filters
		b.mu.Unlock()
	}
	rules, err := filter.NewRules(s.Filters.Skip, s.Filters.Only, own.Skip, own.Only, s.SkipSticky)
	if err != nil {
		m.log.Warn("invalid filter entries ignored", "err", err)
	}
	return rules
}

// SetBoardFilters sets a board's own filters. They add to the global skip list; a non-empty only list
// replaces the global one for this board.
func (m *Manager) SetBoardFilters(key string, f config.BoardFilters) error {
	var err error
	if f.Skip, err = filter.CleanList(f.Skip); err != nil {
		return fmt.Errorf("skip filter: %w", err)
	}
	if f.Only, err = filter.CleanList(f.Only); err != nil {
		return fmt.Errorf("only filter: %w", err)
	}
	m.mu.Lock()
	b := m.boardByKey[key]
	m.mu.Unlock()
	if b == nil {
		return ErrNotFound
	}
	b.mu.Lock()
	b.filters = f
	b.mu.Unlock()
	m.touch()
	m.saveLogged()
	return nil
}

func (m *Manager) trackedIDs() map[string]bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make(map[string]bool, len(m.threads))
	for k := range m.threadByKey {
		ids[k] = true
	}
	return ids
}

// filteredOn lists live threads of a board's last catalog that its filters keep out and that are not tracked.
func (m *Manager) filteredOn(b *Board, tracked map[string]bool) []FilteredThread {
	rules := m.boardRules(b)
	b.mu.Lock()
	catalog := b.catalog
	b.mu.Unlock()
	out := []FilteredThread{}
	for _, e := range catalog {
		if tracked[threadKey(b.Site, b.Code, e.ID)] {
			continue
		}
		if v := rules.Check(e.FilterS, e.Text, e.Sticky); !v.Allowed {
			subject := e.Subject
			if subject == "" {
				subject = NoSubject
			}
			out = append(out, FilteredThread{ID: e.ID, Subject: subject, Reason: v.Reason, URL: fourchan.ThreadURL(b.Code, e.ID)})
		}
	}
	return out
}

// BoardFiltered lists the live threads a board's filters keep out.
func (m *Manager) BoardFiltered(key string) ([]FilteredThread, error) {
	m.mu.Lock()
	b := m.boardByKey[key]
	m.mu.Unlock()
	if b == nil {
		return nil, ErrNotFound
	}
	return m.filteredOn(b, m.trackedIDs()), nil
}

// FilterMatch is a tracked thread that the current filters would have kept out.
type FilterMatch struct {
	Key     string `json:"key"`
	Subject string `json:"subject"`
	Reason  string `json:"reason"`
}

// FilterMatches lists tracked threads, added by a board (boardKey, or any board when empty), that the
// current filters would keep out. Threads the user added are never listed.
func (m *Manager) FilterMatches(boardKey string) []FilterMatch {
	m.mu.Lock()
	threads := append([]*Thread(nil), m.threads...)
	boards := make(map[string]*Board, len(m.boardByKey))
	for k, b := range m.boardByKey {
		boards[k] = b
	}
	m.mu.Unlock()

	rulesFor := map[string]filter.Rules{}
	out := []FilterMatch{}
	for _, t := range threads {
		if t.FromBoard == "" || (boardKey != "" && t.FromBoard != boardKey) {
			continue
		}
		rules, ok := rulesFor[t.FromBoard]
		if !ok {
			rules = m.boardRules(boards[t.FromBoard]) // A removed board leaves the global filters.
			rulesFor[t.FromBoard] = rules
		}
		t.mu.Lock()
		subject, text, display := t.matchSubject, t.matchText, t.displaySubjectLocked()
		t.mu.Unlock()
		// Stickies are judged at catalog time only; a tracked thread's sticky state is not known.
		if v := rules.Check(subject, text, false); !v.Allowed {
			out = append(out, FilterMatch{Key: t.Key(), Subject: display, Reason: v.Reason})
		}
	}
	return out
}
