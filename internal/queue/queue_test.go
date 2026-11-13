package queue

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"synochan/internal/fetch"
)

type fakeItem struct {
	name     string
	prio     Priority
	asset    bool
	ctx      context.Context
	cancel   context.CancelFunc
	readyAt  atomic.Int64 // Unix nanoseconds, 0 = any time.
	process  func(ctx context.Context) (Result, error)
	runs     atomic.Int32
	mu       sync.Mutex
	runTimes []time.Time
}

func newFake(name string, prio Priority, fn func(ctx context.Context) (Result, error)) *fakeItem {
	ctx, cancel := context.WithCancel(context.Background())
	return &fakeItem{name: name, prio: prio, ctx: ctx, cancel: cancel, process: fn}
}

func (f *fakeItem) Context() context.Context { return f.ctx }
func (f *fakeItem) ShouldProcess() bool      { return f.ctx.Err() == nil }
func (f *fakeItem) ReadyAt() time.Time {
	if n := f.readyAt.Load(); n != 0 {
		return time.Unix(0, n)
	}
	return time.Time{}
}
func (f *fakeItem) Priority() Priority { return f.prio }
func (f *fakeItem) IsAsset() bool      { return f.asset }
func (f *fakeItem) String() string     { return f.name }
func (f *fakeItem) Process(ctx context.Context) (Result, error) {
	f.runs.Add(1)
	f.mu.Lock()
	f.runTimes = append(f.runTimes, time.Now())
	f.mu.Unlock()
	return f.process(ctx)
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newQueue(maxConcurrent int, oneRPS bool) *Queue {
	return New(Options{
		Limits: func() (int, bool) { return maxConcurrent, oneRPS },
		Logger: quietLogger(),
	})
}

func run(t *testing.T, q *Queue) context.CancelFunc {
	ctx, cancel := context.WithCancel(context.Background())
	go q.Run(ctx)
	t.Cleanup(func() {
		cancel()
		<-q.Done()
	})
	return cancel
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestPriorityOrderAndDistinct(t *testing.T) {
	q := newQueue(1, false)
	var mu sync.Mutex
	var order []string
	record := func(name string) func(context.Context) (Result, error) {
		return func(context.Context) (Result, error) {
			mu.Lock()
			order = append(order, name)
			mu.Unlock()
			return Result{Remove: true}, nil
		}
	}
	low := newFake("low", Low, record("low"))
	def := newFake("default", Default, record("default"))
	high := newFake("high", High, record("high"))
	q.Enqueue(low)
	q.Enqueue(def)
	q.Enqueue(def) // Duplicate is ignored.
	q.Enqueue(high)
	if s := q.Status(); s.Queued != 3 {
		t.Fatalf("queued = %d, want 3", s.Queued)
	}
	run(t, q)
	waitFor(t, "all processed", func() bool { mu.Lock(); defer mu.Unlock(); return len(order) == 3 })
	if order[0] != "high" || order[1] != "default" || order[2] != "low" {
		t.Fatalf("order = %v", order)
	}
}

func TestConcurrencyLimit(t *testing.T) {
	q := newQueue(2, false)
	var running, peak atomic.Int32
	release := make(chan struct{})
	for i := 0; i < 6; i++ {
		q.Enqueue(newFake("item", Default, func(ctx context.Context) (Result, error) {
			n := running.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			<-release
			running.Add(-1)
			return Result{Remove: true}, nil
		}))
	}
	run(t, q)
	waitFor(t, "two running", func() bool { return running.Load() == 2 })
	time.Sleep(50 * time.Millisecond)
	if peak.Load() != 2 {
		t.Fatalf("peak = %d", peak.Load())
	}
	close(release)
	waitFor(t, "drained", func() bool { s := q.Status(); return s.Queued == 0 && s.Active == 0 })
}

func TestOneRequestPerSecondSpacing(t *testing.T) {
	q := newQueue(4, true)
	var times []time.Time
	var mu sync.Mutex
	for i := 0; i < 3; i++ {
		q.Enqueue(newFake("item", Default, func(context.Context) (Result, error) {
			mu.Lock()
			times = append(times, time.Now())
			mu.Unlock()
			return Result{Remove: true}, nil
		}))
	}
	run(t, q)
	waitFor(t, "three runs", func() bool { mu.Lock(); defer mu.Unlock(); return len(times) == 3 })
	for i := 1; i < 3; i++ {
		if gap := times[i].Sub(times[i-1]); gap < 950*time.Millisecond {
			t.Fatalf("gap %v too short", gap)
		}
	}
}

func TestRequeueAndReadyAt(t *testing.T) {
	q := newQueue(1, false)
	item := newFake("tracker", Default, nil)
	item.process = func(context.Context) (Result, error) {
		item.readyAt.Store(time.Now().Add(150 * time.Millisecond).UnixNano())
		return Result{}, nil // Not removed: runs again when ready.
	}
	q.Enqueue(item)
	run(t, q)
	waitFor(t, "second run", func() bool { return item.runs.Load() >= 2 })
	item.mu.Lock()
	gap := item.runTimes[1].Sub(item.runTimes[0])
	item.mu.Unlock()
	if gap < 140*time.Millisecond {
		t.Fatalf("ran again after %v, before it was ready", gap)
	}
}

func TestAssetGivesUpAfterRepeatedFailures(t *testing.T) {
	q := newQueue(1, false)
	asset := newFake("asset", Default, func(context.Context) (Result, error) { return Result{}, errors.New("boom") })
	asset.asset = true
	q.Enqueue(asset)

	// Shrink waiting: pretend each retry is due immediately by clearing retry times as they appear.
	run(t, q)
	deadline := time.Now().Add(5 * time.Second)
	for asset.runs.Load() < maxAssetAttempts && time.Now().Before(deadline) {
		q.mu.Lock()
		for k, r := range q.retries {
			r.at = time.Time{}
			q.retries[k] = r
		}
		q.mu.Unlock()
		q.signal()
		time.Sleep(5 * time.Millisecond)
	}
	waitFor(t, "given up", func() bool { s := q.Status(); return s.Queued == 0 && s.Active == 0 })
	if n := asset.runs.Load(); n != maxAssetAttempts {
		t.Fatalf("runs = %d, want %d", n, maxAssetAttempts)
	}
}

func TestTrackerRetryBackoffGrows(t *testing.T) {
	q := newQueue(1, false)
	tracker := newFake("tracker", Default, func(context.Context) (Result, error) { return Result{}, errors.New("boom") })
	q.Enqueue(tracker)
	run(t, q)
	waitFor(t, "first failure", func() bool {
		q.mu.Lock()
		defer q.mu.Unlock()
		return q.retries[tracker].failures == 1
	})
	q.mu.Lock()
	wait := time.Until(q.retries[tracker].at)
	q.mu.Unlock()
	if wait < 4*time.Second || wait > initialRetryDelay {
		t.Fatalf("first retry in %v", wait)
	}
	if tracker.runs.Load() != 1 {
		t.Fatalf("retried too early")
	}
}

func TestTooManyRequestsBacksOffEverything(t *testing.T) {
	q := newQueue(1, false)
	var limited atomic.Bool
	first := newFake("first", High, func(context.Context) (Result, error) {
		if limited.CompareAndSwap(false, true) {
			return Result{}, &fetch.TooManyRequestsError{RetryAfter: 200 * time.Millisecond}
		}
		return Result{Remove: true}, nil
	})
	var secondAt atomic.Int64
	second := newFake("second", Default, func(context.Context) (Result, error) {
		secondAt.Store(time.Now().UnixNano())
		return Result{Remove: true}, nil
	})
	start := time.Now()
	q.Enqueue(first)
	q.Enqueue(second)
	run(t, q)
	waitFor(t, "both done", func() bool { return first.runs.Load() == 2 && second.runs.Load() == 1 })
	// The backoff (retry-after plus one second) applies before anything else is sent.
	if gap := time.Unix(0, secondAt.Load()).Sub(start); gap < 1100*time.Millisecond {
		t.Fatalf("second request sent %v after start, during the backoff", gap)
	}
}

func TestPauseAndCancelledItems(t *testing.T) {
	q := newQueue(1, false)
	q.SetPaused(true)
	ran := newFake("a", Default, func(context.Context) (Result, error) { return Result{Remove: true}, nil })
	removed := newFake("b", Default, func(context.Context) (Result, error) { return Result{Remove: true}, nil })
	q.Enqueue(ran)
	q.Enqueue(removed)
	run(t, q)
	time.Sleep(100 * time.Millisecond)
	if ran.runs.Load() != 0 {
		t.Fatal("ran while paused")
	}
	removed.cancel()
	q.Purge()
	if s := q.Status(); s.Queued != 1 || !s.Paused {
		t.Fatalf("status %+v", s)
	}
	q.SetPaused(false)
	waitFor(t, "processed after resume", func() bool { return ran.runs.Load() == 1 })
	if removed.runs.Load() != 0 {
		t.Fatal("cancelled item ran")
	}
}

func TestCancelWhileRunningDropsItem(t *testing.T) {
	q := newQueue(1, false)
	started := make(chan struct{})
	item := newFake("slow", Default, nil)
	item.process = func(ctx context.Context) (Result, error) {
		close(started)
		<-ctx.Done()
		return Result{}, ctx.Err()
	}
	q.Enqueue(item)
	run(t, q)
	<-started
	item.cancel()
	waitFor(t, "idle", func() bool { s := q.Status(); return s.Active == 0 && s.Queued == 0 })
	q.mu.Lock()
	_, retrying := q.retries[item]
	q.mu.Unlock()
	if retrying {
		t.Fatal("cancelled item scheduled for retry")
	}
}

func TestNewItemsAndTrackersCallback(t *testing.T) {
	var added []Item
	var onAddedRan atomic.Bool
	q := New(Options{
		Limits: func() (int, bool) { return 1, false },
		AddTrackers: func(source Item, trackers []Item, onAdded func()) bool {
			added = append(added, trackers...)
			onAdded()
			return true
		},
		Logger: quietLogger(),
	})
	child := newFake("child", Default, func(context.Context) (Result, error) { return Result{Remove: true}, nil })
	tracker := newFake("found thread", Default, func(context.Context) (Result, error) { return Result{Remove: true}, nil })
	parent := newFake("parent", Default, func(context.Context) (Result, error) {
		return Result{Remove: true, Enqueue: []Item{child}, Trackers: []Item{tracker}, OnTrackersAdded: func() { onAddedRan.Store(true) }}, nil
	})
	q.Enqueue(parent)
	run(t, q)
	waitFor(t, "child ran", func() bool { return child.runs.Load() == 1 })
	if len(added) != 1 || added[0] != tracker || !onAddedRan.Load() {
		t.Fatalf("callback: added=%v onAdded=%v", added, onAddedRan.Load())
	}
}
