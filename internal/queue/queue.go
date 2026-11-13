// Package queue schedules scraping and downloading work.
//
// Items are only taken off the queue when there is capacity, so priority order, request spacing and
// "Too Many Requests" backoff apply to every request.
package queue

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"synochan/internal/fetch"
)

// Priority decides which lane an item waits in. Lower values are processed first.
type Priority int

const (
	High Priority = iota
	Default
	Low
	laneCount
)

// Item is a unit of work: a thread or board scrape, or a file download.
type Item interface {
	// Context is cancelled when the item is no longer wanted (e.g. its thread was removed).
	Context() context.Context
	// ShouldProcess reports whether the item still wants processing. Once false it never becomes true again.
	ShouldProcess() bool
	// ReadyAt is the earliest time the item may be processed. Zero means any time.
	ReadyAt() time.Time
	Priority() Priority
	// IsAsset reports whether this is a file download. Assets are dropped after repeated failures,
	// trackers are retried indefinitely.
	IsAsset() bool
	Process(ctx context.Context) (Result, error)
	String() string
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

// Options configure a queue.
type Options struct {
	// Limits returns the current concurrency limit and whether requests are spaced one second apart.
	Limits func() (maxConcurrent int, oneRequestPerSecond bool)
	// AddTrackers adds new trackers found by source and enqueues them, then runs onAdded, in one step.
	// Returns false if nothing was done (source was removed meanwhile).
	AddTrackers func(source Item, trackers []Item, onAdded func()) bool
	Logger      *slog.Logger
}

const (
	maxAssetAttempts  = 5
	initialRetryDelay = 5 * time.Second
	maxRetryDelay     = 10 * time.Minute
	idleWait          = 15 * time.Second
	tooManyDefault    = time.Minute
)

type retryState struct {
	failures int
	at       time.Time
}

// Status is a snapshot for display.
type Status struct {
	Paused       bool      `json:"paused"`
	Active       int       `json:"active"`
	Queued       int       `json:"queued"`
	BackoffUntil time.Time `json:"backoff_until,omitzero"`
}

// Queue is a distinct, prioritised work queue with a bounded number of running items.
type Queue struct {
	opts Options
	log  *slog.Logger

	mu           sync.Mutex
	lanes        [laneCount][]Item
	queued       map[Item]struct{}
	retries      map[Item]retryState
	running      int
	paused       bool
	tooMany      time.Duration
	backoffUntil time.Time

	wake chan struct{}
	done chan struct{}
	idle chan struct{} // Closed and replaced when running drops to zero; used by WaitIdle.
}

// New creates a queue. Call Run to start processing.
func New(opts Options) *Queue {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
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

func (q *Queue) enqueueLocked(item Item) {
	if _, ok := q.queued[item]; ok {
		return
	}
	q.queued[item] = struct{}{}
	p := item.Priority()
	if p < High || p >= laneCount {
		p = Default
	}
	q.lanes[p] = append(q.lanes[p], item)
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
	s := Status{Paused: q.paused, Active: q.running, Queued: len(q.queued)}
	if time.Now().Before(q.backoffUntil) {
		s.BackoffUntil = q.backoffUntil
	}
	return s
}

// Purge drops queued items that no longer want processing, so counts stay accurate after removals.
func (q *Queue) Purge() {
	q.mu.Lock()
	defer q.mu.Unlock()
	for p := range q.lanes {
		kept := q.lanes[p][:0]
		for _, item := range q.lanes[p] {
			if item.ShouldProcess() {
				kept = append(kept, item)
			} else {
				delete(q.queued, item)
				delete(q.retries, item)
			}
		}
		clear(q.lanes[p][len(kept):])
		q.lanes[p] = kept
	}
}

// Done is closed when Run has returned.
func (q *Queue) Done() <-chan struct{} { return q.done }

// WaitIdle blocks until no items are running or ctx ends.
func (q *Queue) WaitIdle(ctx context.Context) error {
	for {
		q.mu.Lock()
		if q.running == 0 {
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
	maxConcurrent, oneRPS := q.opts.Limits()
	maxConcurrent = max(1, maxConcurrent)

	q.mu.Lock()
	if q.paused || q.running >= maxConcurrent {
		q.mu.Unlock()
		q.wait(ctx, 0)
		return
	}
	if q.tooMany > 0 {
		if q.running > 0 {
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

	item, nextReadyAt := q.dequeueReadyLocked(time.Now())
	if item == nil {
		q.mu.Unlock()
		wait := idleWait
		if !nextReadyAt.IsZero() {
			wait = min(wait, time.Until(nextReadyAt))
		}
		if wait > 0 {
			q.wait(ctx, wait)
		}
		return
	}
	q.running++
	q.mu.Unlock()

	q.log.Debug("starting", "item", item)
	go q.run(ctx, item)

	if oneRPS {
		sleep(ctx, time.Second)
	}
}

// dequeueReadyLocked takes items off the lanes until one is ready, putting deferred ones back.
// Returns nil and the earliest ready time of deferred items when none is ready.
func (q *Queue) dequeueReadyLocked(now time.Time) (Item, time.Time) {
	var deferred []Item
	var nextReadyAt time.Time
	defer func() {
		for _, d := range deferred {
			q.enqueueLocked(d)
		}
	}()

	for p := range q.lanes {
		for len(q.lanes[p]) > 0 {
			item := q.lanes[p][0]
			q.lanes[p][0] = nil
			q.lanes[p] = q.lanes[p][1:]
			delete(q.queued, item)

			if !item.ShouldProcess() {
				delete(q.retries, item)
				continue
			}
			readyAt := item.ReadyAt()
			if r, ok := q.retries[item]; ok && r.at.After(readyAt) {
				readyAt = r.at
			}
			if readyAt.IsZero() || !readyAt.After(now) {
				return item, time.Time{}
			}
			deferred = append(deferred, item)
			if nextReadyAt.IsZero() || readyAt.Before(nextReadyAt) {
				nextReadyAt = readyAt
			}
		}
	}
	return nil, nextReadyAt
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
	q.running--
	if q.running == 0 {
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
	defer q.mu.Unlock()

	failures := q.retries[item].failures + 1
	if item.IsAsset() && failures >= maxAssetAttempts {
		delete(q.retries, item)
		q.log.Error("giving up", "item", item, "attempts", failures, "err", err)
		return
	}
	delay := min(initialRetryDelay<<min(failures-1, 20), maxRetryDelay)
	q.retries[item] = retryState{failures: failures, at: time.Now().Add(delay)}
	q.log.Warn("failed, retrying later", "item", item, "attempt", failures, "retry_in", delay, "err", err)
	q.enqueueLocked(item)
}
