package queue

import (
	"context"
	"errors"
	"fmt"
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
	order    Order
	asset    bool
	ctx      context.Context
	cancel   context.CancelFunc
	readyAt  atomic.Int64 // Unix nanoseconds, 0 = any time.
	process  func(ctx context.Context) (Result, error)
	onGiveUp func()
	runs     atomic.Int32
	mu       sync.Mutex
	runTimes []time.Time
}

func newFake(name string, lane Lane, fn func(ctx context.Context) (Result, error)) *fakeItem {
	ctx, cancel := context.WithCancel(context.Background())
	return &fakeItem{name: name, order: Order{Rank: NoRank, Lane: lane}, ctx: ctx, cancel: cancel, process: fn}
}

func newAsset(name string, rank int, fn func(ctx context.Context) (Result, error)) *fakeItem {
	f := newFake(name, Default, fn)
	f.asset = true
	f.order.Rank = rank
	return f
}

func (f *fakeItem) Context() context.Context { return f.ctx }
func (f *fakeItem) ShouldProcess() bool      { return f.ctx.Err() == nil }
func (f *fakeItem) ReadyAt() time.Time {
	if n := f.readyAt.Load(); n != 0 {
		return time.Unix(0, n)
	}
	return time.Time{}
}
func (f *fakeItem) Order() Order   { return f.order }
func (f *fakeItem) IsAsset() bool  { return f.asset }
func (f *fakeItem) String() string { return f.name }
func (f *fakeItem) Process(ctx context.Context) (Result, error) {
	f.runs.Add(1)
	f.mu.Lock()
	f.runTimes = append(f.runTimes, time.Now())
	f.mu.Unlock()
	return f.process(ctx)
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// newQueue makes a queue without the one second tracker spacing unless spaced is set, to keep tests fast.
func newQueue(maxDownloads int, spaced bool) *Queue {
	spacing := time.Millisecond
	if spaced {
		spacing = time.Second
	}
	return New(Options{
		Limits:         func() Limits { return Limits{MaxDownloads: maxDownloads, SpaceDownloads: spaced} },
		TrackerSpacing: spacing,
		Logger:         quietLogger(),
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

func TestOrderTrackersThenRanksThenLanes(t *testing.T) {
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
	plain := newAsset("asset", NoRank, record("asset"))
	thumb := newAsset("thumb", NoRank, record("thumb"))
	thumb.order.Lane = Low
	second := newAsset("rank2", 2, record("rank2"))
	first := newAsset("rank1", 1, record("rank1"))
	tracker := newFake("tracker", Default, record("tracker"))
	newThread := newFake("new thread", High, record("new thread"))
	for _, it := range []*fakeItem{thumb, plain, second, first, tracker, newThread} {
		q.Enqueue(it)
	}
	q.Enqueue(plain) // Duplicate is ignored.
	if s := q.Status(); s.Queued != 6 {
		t.Fatalf("queued = %d, want 6", s.Queued)
	}
	run(t, q)
	waitFor(t, "all processed", func() bool { mu.Lock(); defer mu.Unlock(); return len(order) == 6 })
	// Trackers run in their own slot, so they lead; ranked assets come before the rest, thumbnails last.
	want := []string{"new thread", "tracker", "rank1", "rank2", "asset", "thumb"}
	trackers, assets := []string{}, []string{}
	for _, n := range order {
		if n == "tracker" || n == "new thread" {
			trackers = append(trackers, n)
		} else {
			assets = append(assets, n)
		}
	}
	if fmt.Sprint(trackers) != fmt.Sprint(want[:2]) || fmt.Sprint(assets) != fmt.Sprint(want[2:]) {
		t.Fatalf("order = %v", order)
	}
}

func TestRankChangeWhileQueued(t *testing.T) {
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
	a := newAsset("a", NoRank, record("a"))
	b := newAsset("b", NoRank, record("b"))
	q.Enqueue(a)
	q.Enqueue(b)
	b.order.Rank = 1 // Prioritised after queueing; read at pick time.
	run(t, q)
	waitFor(t, "both", func() bool { mu.Lock(); defer mu.Unlock(); return len(order) == 2 })
	if order[0] != "b" {
		t.Fatalf("order = %v", order)
	}
}

func TestTrackersNotBlockedByDownloads(t *testing.T) {
	q := newQueue(1, false)
	release := make(chan struct{})
	slow := newAsset("slow download", NoRank, func(context.Context) (Result, error) {
		<-release
		return Result{Remove: true}, nil
	})
	var checked atomic.Bool
	tracker := newFake("check", Default, func(context.Context) (Result, error) {
		checked.Store(true)
		return Result{Remove: true}, nil
	})
	q.Enqueue(slow)
	run(t, q)
	waitFor(t, "download running", func() bool { return slow.runs.Load() == 1 })
	q.Enqueue(tracker)
	waitFor(t, "check while the download slot is busy", checked.Load)
	close(release)
}

func TestConcurrencyLimit(t *testing.T) {
	q := newQueue(2, false)
	var running, peak atomic.Int32
	release := make(chan struct{})
	for i := 0; i < 6; i++ {
		q.Enqueue(newAsset("item", NoRank, func(ctx context.Context) (Result, error) {
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

func TestTrackersAndSpacedDownloadsOnePerSecond(t *testing.T) {
	q := newQueue(4, true)
	var times []time.Time
	var mu sync.Mutex
	var assetTimes []time.Time
	for i := 0; i < 3; i++ {
		q.Enqueue(newFake("tracker", Default, func(context.Context) (Result, error) {
			mu.Lock()
			times = append(times, time.Now())
			mu.Unlock()
			return Result{Remove: true}, nil
		}))
		q.Enqueue(newAsset("asset", NoRank, func(context.Context) (Result, error) {
			mu.Lock()
			assetTimes = append(assetTimes, time.Now())
			mu.Unlock()
			return Result{Remove: true}, nil
		}))
	}
	run(t, q)
	waitFor(t, "all runs", func() bool { mu.Lock(); defer mu.Unlock(); return len(times) == 3 && len(assetTimes) == 3 })
	for i := 1; i < 3; i++ {
		if gap := assetTimes[i].Sub(assetTimes[i-1]); gap < 950*time.Millisecond {
			t.Fatalf("download gap %v too short", gap)
		}
	}
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
	asset := newAsset("asset", NoRank, func(context.Context) (Result, error) { return Result{}, errors.New("boom") })
	q.Enqueue(asset)
	var gaveUp atomic.Bool
	asset.onGiveUp = func() { gaveUp.Store(true) }

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
	waitFor(t, "give up notification", gaveUp.Load)
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
		Limits:         func() Limits { return Limits{MaxDownloads: 1} },
		TrackerSpacing: time.Millisecond,
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

func (f *fakeItem) GaveUp() {
	if f.onGiveUp != nil {
		f.onGiveUp()
	}
}
