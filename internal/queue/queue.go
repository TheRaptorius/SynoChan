// Package queue schedules scraping and downloading work.
//
// Two kinds of work share the queue. Trackers (thread and board checks) talk to the image board's API: they
// run one at a time, at most one per second, and always go before downloads, so checks are never stuck
// behind a long download backlog. Assets (file downloads) run in parallel up to a configurable limit,
// optionally spaced one second apart, in order of their thread's priority and then first come, first served.
package queue

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"

	"synochan/internal/fetch"
)

// Lane orders items of the same kind and rank. Lower values go first.
type Lane int

const (
	High Lane = iota
	Default
	Low
)

// NoRank is the rank of items without a priority. Ranked items (1, 2, 3, …) go before them.
const NoRank = math.MaxInt32

// Order decides which of several ready items of the same kind runs first: lower Rank, then lower Lane,
// then the one queued earliest.
type Order struct {
	Rank int
	Lane Lane
}

// Item is a unit of work: a thread or board check, or a file download.
type Item interface {
	// Context is cancelled when the item is no longer wanted (e.g. its thread was removed).
	Context() context.Context
	// ShouldProcess reports whether the item still wants processing. Once false it never becomes true again.
	ShouldProcess() bool
	// ReadyAt is the earliest time the item may be processed. Zero means any time.
	ReadyAt() time.Time
	// Order is read whenever the next item is picked, so it may change while the item waits.
	Order() Order
	// IsAsset reports whether this is a file download. Assets are dropped after repeated failures,
	// trackers are retried indefinitely.
	IsAsset() bool
	Process(ctx context.Context) (Result, error)
	String() string
}

// GiveUpNotifier is implemented by items that want to know when the queue stopped retrying them.
type GiveUpNotifier interface {
	GaveUp()
}

// Result is what processing an item produced.
type Result struct {
	// Remove takes the item off the queue. Otherwise it is requeued and runs again when ready.
	Remove bool
	// Enqueue are new items (downloads) to queue.
	Enqueue []Item
	// Trackers are new trackers (threads found on a board), handed to the AddTrackers callback.
	Trackers []Item
	// OnTrackersAdded runs once Trackers were added, to record state that must not be saved before they are.
	OnTrackersAdded func()
}

// Limits are the user configurable limits, read before every scheduling decision.
type Limits struct {
	MaxDownloads   int  // Parallel file downloads.
	SpaceDownloads bool // Start file downloads at most one per second.
}

// Options configure a queue.
type Options struct {
	Limits func() Limits
	// AddTrackers adds new trackers found by source and enqueues them, then runs onAdded, in one step.
	// Returns false if nothing was done (source was removed meanwhile).
	AddTrackers func(source Item, trackers []Item, onAdded func()) bool
	// TrackerSpacing is the minimum time between the starts of two trackers. Defaults to one second.
	TrackerSpacing time.Duration
	Logger         *slog.Logger
}

const (
	maxAssetAttempts  = 5
	initialRetryDelay = 5 * time.Second
	maxRetryDelay     = 10 * time.Minute
	idleWait          = 15 * time.Second
	tooManyDefault    = time.Minute
	downloadSpacing   = time.Second
)

type retryState struct {
	failures int
	at       time.Time
}

type entry struct {
	item Item
	seq  uint64
}

// Status is a snapshot for display.
type Status struct {
	Paused       bool      `json:"paused"`
	Active       int       `json:"active"`
	Queued       int       `json:"queued"`
	BackoffUntil time.Time `json:"backoff_until,omitzero"`
}

// Queue is a distinct work queue with separate limits for trackers and downloads.
type Queue struct {
	opts Options
	log  *slog.Logger

	mu               sync.Mutex
	entries          []entry
	queued           map[Item]struct{}
	nextSeq          uint64
	retries          map[Item]retryState
	runningTrackers  int
	runningAssets    int
	lastTrackerStart time.Time
	lastAssetStart   time.Time
	paused           bool
	tooMany          time.Duration
	backoffUntil     time.Time

	wake chan struct{}
	done chan struct{}
	idle chan struct{} // Closed and replaced when nothing runs any more; used by WaitIdle.
}

// New creates a queue. Call Run to start processing.
func New(opts Options) *Queue {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.TrackerSpacing <= 0 {
		opts.TrackerSpacing = time.Second
	}
	return &Queue{
		opts:    opts,
		log:     opts.Logger,
		queued:  make(map[Item]struct{}),
		retries: make(map[Item]retryState),
		wake:    make(chan struct{}, 1),
		done:    make(chan struct{}),
		idle:    make(chan struct{}),
	}
}

func (q *Queue) signal() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// Enqueue adds an item unless it is already queued.
func (q *Queue) Enqueue(item Item) {
	q.mu.Lock()
	q.enqueueLocked(item)
	q.mu.Unlock()
	q.signal()
}

// Wake makes the queue re-evaluate what to run next, e.g. after priorities changed.
func (q *Queue) Wake() { q.signal() }

func (q *Queue) enqueueLocked(item Item) {
	if _, ok := q.queued[item]; ok {
		return
	}
	q.queued[item] = struct{}{}
	q.nextSeq++
	q.entries = append(q.entries, entry{item: item, seq: q.nextSeq})
}

// SetPaused stops or resumes starting new work. Running items finish.
func (q *Queue) SetPaused(paused bool) {
	q.mu.Lock()
	q.paused = paused
	q.mu.Unlock()
	q.signal()
}

// Status returns a snapshot for display.
func (q *Queue) Status() Status {
	q.mu.Lock()
	defer q.mu.Unlock()
	s := Status{Paused: q.paused, Active: q.runningTrackers + q.runningAssets, Queued: len(q.entries)}
	if time.Now().Before(q.backoffUntil) {
		s.BackoffUntil = q.backoffUntil
	}
	return s
}

// Purge drops queued items that no longer want processing, so counts stay accurate after removals.
func (q *Queue) Purge() {
	q.mu.Lock()
	defer q.mu.Unlock()
	kept := q.entries[:0]
	for _, e := range q.entries {
		if e.item.ShouldProcess() {
			kept = append(kept, e)
		} else {
			delete(q.queued, e.item)
			delete(q.retries, e.item)
		}
	}
	clear(q.entries[len(kept):])
	q.entries = kept
}

// Done is closed when Run has returned.
func (q *Queue) Done() <-chan struct{} { return q.done }

// WaitIdle blocks until no items are running or ctx ends.
func (q *Queue) WaitIdle(ctx context.Context) error {
	for {
		q.mu.Lock()
		if q.runningTrackers+q.runningAssets == 0 {
			q.mu.Unlock()
			return nil
		}
		ch := q.idle
		q.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Run processes items until ctx is cancelled. Running items are cancelled with it.
func (q *Queue) Run(ctx context.Context) {
	defer close(q.done)
	for ctx.Err() == nil {
		q.step(ctx)
	}
	q.log.Debug("process queue stopped")
}

func (q *Queue) wait(ctx context.Context, d time.Duration) {
	if d <= 0 {
		select {
		case <-q.wake:
		case <-ctx.Done():
		}
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-q.wake:
	case <-t.C:
	case <-ctx.Done():
	}
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

func (q *Queue) step(ctx context.Context) {
	lim := Limits{MaxDownloads: 1}
	if q.opts.Limits != nil {
		lim = q.opts.Limits()
	}
	lim.MaxDownloads = max(1, lim.MaxDownloads)

	q.mu.Lock()
	if q.paused {
		q.mu.Unlock()
		q.wait(ctx, 0)
		return
	}
	if q.tooMany > 0 {
		if q.runningTrackers+q.runningAssets > 0 {
			// Let requests already in flight finish first. Any 429s they get merge into this one wait.
			q.mu.Unlock()
			q.wait(ctx, 0)
			return
		}
		delay := q.tooMany + time.Second
		q.tooMany = 0
		q.backoffUntil = time.Now().Add(delay)
		q.mu.Unlock()
		q.log.Warn("backing off after Too Many Requests", "delay", delay)
		sleep(ctx, delay)
		return
	}

	now := time.Now()
	item, wakeAt := q.pickLocked(now, lim)
	if item == nil {
		q.mu.Unlock()
		wait := time.Duration(0) // Only a wake signal (a finished item, a new item) can change anything.
		if !wakeAt.IsZero() {
			wait = max(time.Millisecond, min(idleWait, time.Until(wakeAt)))
		}
		q.wait(ctx, wait)
		return
	}
	if item.IsAsset() {
		q.runningAssets++
		q.lastAssetStart = now
	} else {
		q.runningTrackers++
		q.lastTrackerStart = now
	}
	q.mu.Unlock()

	q.log.Debug("starting", "item", item)
	go q.run(ctx, item)
}

// pickLocked takes the best ready item that has a free slot off the queue. When there is none, it returns
// the time something becomes possible without outside events (an item getting ready or a spacing expiring),
// or zero if only a finished or new item can change anything.
func (q *Queue) pickLocked(now time.Time, lim Limits) (Item, time.Time) {
	trackerFreeAt := q.lastTrackerStart.Add(q.opts.TrackerSpacing)
	trackerSlot := q.runningTrackers == 0 && !trackerFreeAt.After(now)

	assetFreeAt := time.Time{}
	if lim.SpaceDownloads {
		assetFreeAt = q.lastAssetStart.Add(downloadSpacing)
	}
	assetSlot := q.runningAssets < lim.MaxDownloads && !assetFreeAt.After(now)

	best := -1
	var bestOrder Order
	var wakeAt time.Time
	earliest := func(t time.Time) {
		if !t.IsZero() && (wakeAt.IsZero() || t.Before(wakeAt)) {
			wakeAt = t
		}
	}

	kept := q.entries[:0]
	for _, e := range q.entries {
		if !e.item.ShouldProcess() {
			delete(q.queued, e.item)
			delete(q.retries, e.item)
			continue
		}
		kept = append(kept, e)
		idx := len(kept) - 1

		readyAt := e.item.ReadyAt()
		if r, ok := q.retries[e.item]; ok && r.at.After(readyAt) {
			readyAt = r.at
		}
		if !readyAt.IsZero() && readyAt.After(now) {
			earliest(readyAt)
			continue
		}

		asset := e.item.IsAsset()
		if asset && !assetSlot {
			if q.runningAssets < lim.MaxDownloads {
				earliest(assetFreeAt)
			}
			continue
		}
		if !asset && !trackerSlot {
			if q.runningTrackers == 0 {
				earliest(trackerFreeAt)
			}
			continue
		}

		order := e.item.Order()
		if best < 0 || better(asset, order, e.seq, kept[best].item.IsAsset(), bestOrder, kept[best].seq) {
			best, bestOrder = idx, order
		}
	}
	clear(q.entries[len(kept):])
	q.entries = kept

	if best < 0 {
		return nil, wakeAt
	}
	item := q.entries[best].item
	q.entries = append(q.entries[:best], q.entries[best+1:]...)
	delete(q.queued, item)
	return item, time.Time{}
}

// better reports whether item a goes before item b: trackers before assets, then lower rank, lane and sequence.
func better(aAsset bool, a Order, aSeq uint64, bAsset bool, b Order, bSeq uint64) bool {
	if aAsset != bAsset {
		return !aAsset
	}
	if a.Rank != b.Rank {
		return a.Rank < b.Rank
	}
	if a.Lane != b.Lane {
		return a.Lane < b.Lane
	}
	return aSeq < bSeq
}

func (q *Queue) run(ctx context.Context, item Item) {
	itemCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(item.Context(), cancel)

	res, err := safeProcess(itemCtx, item)

	stop()
	cancel()

	// Handled before the slot is freed, so state set here (e.g. a backoff) is seen before another item starts.
	q.handle(ctx, item, res, err)

	q.mu.Lock()
	if item.IsAsset() {
		q.runningAssets--
	} else {
		q.runningTrackers--
	}
	if q.runningTrackers+q.runningAssets == 0 {
		close(q.idle)
		q.idle = make(chan struct{})
	}
	q.mu.Unlock()
	q.signal()
}

func safeProcess(ctx context.Context, item Item) (res Result, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return item.Process(ctx)
}

func (q *Queue) handle(ctx context.Context, item Item, res Result, err error) {
	var tooMany *fetch.TooManyRequestsError

	switch {
	case err == nil:
		q.mu.Lock()
		delete(q.retries, item)
		for _, n := range res.Enqueue {
			q.enqueueLocked(n)
		}
		q.mu.Unlock()

		if len(res.Trackers) > 0 || res.OnTrackersAdded != nil {
			if q.opts.AddTrackers == nil || !q.opts.AddTrackers(item, res.Trackers, res.OnTrackersAdded) {
				q.log.Debug("results not added, source was removed", "item", item)
			}
		}
		if !res.Remove {
			q.Enqueue(item)
		}
		q.signal()

	case errors.Is(err, context.Canceled) && (ctx.Err() != nil || item.Context().Err() != nil):
		// Removed or shutting down. The item is already off the queue, so it is discarded.
		q.forget(item)
		q.log.Debug("cancelled", "item", item)

	case fetch.IsGone(err):
		q.forget(item)
		q.log.Debug("gone", "item", item, "err", err)
		if n, ok := item.(GiveUpNotifier); ok {
			n.GaveUp()
		}

	case errors.As(err, &tooMany):
		delay := tooMany.RetryAfter
		if delay <= 0 {
			delay = tooManyDefault
		}
		q.log.Warn("Too Many Requests", "item", item, "backoff", delay)
		q.mu.Lock()
		q.tooMany = max(q.tooMany, delay)
		q.enqueueLocked(item)
		q.mu.Unlock()

	default:
		q.retryLater(item, err)
	}
}

func (q *Queue) forget(item Item) {
	q.mu.Lock()
	delete(q.retries, item)
	q.mu.Unlock()
}

// retryLater requeues a failed item with exponential backoff, or gives up on an asset that keeps failing.
func (q *Queue) retryLater(item Item, err error) {
	q.mu.Lock()
	failures := q.retries[item].failures + 1
	if item.IsAsset() && failures >= maxAssetAttempts {
		delete(q.retries, item)
		q.mu.Unlock()
		q.log.Error("giving up", "item", item, "attempts", failures, "err", err)
		if n, ok := item.(GiveUpNotifier); ok {
			n.GaveUp()
		}
		return
	}
	delay := min(initialRetryDelay<<min(failures-1, 20), maxRetryDelay)
	q.retries[item] = retryState{failures: failures, at: time.Now().Add(delay)}
	q.enqueueLocked(item)
	q.mu.Unlock()
	q.log.Warn("failed, retrying later", "item", item, "attempt", failures, "retry_in", delay, "err", err)
}
