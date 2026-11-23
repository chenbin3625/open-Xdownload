package httpx

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func resetXRequestsForTest(t *testing.T) {
	t.Helper()
	xRequests.Lock()
	xRequests.last = time.Time{}
	xRequests.slowUntil = time.Time{}
	xRequests.waiters = nil
	xRequests.Unlock()
	t.Cleanup(func() {
		xRequests.Lock()
		xRequests.last = time.Time{}
		xRequests.slowUntil = time.Time{}
		xRequests.waiters = nil
		xRequests.Unlock()
	})
}

func TestWaitXRequestReservesStaggeredSlotsAndCancelRollsBack(t *testing.T) {
	resetXRequestsForTest(t)

	if err := WaitXRequest(context.Background()); err != nil {
		t.Fatalf("first wait: %v", err)
	}

	// Cancel a queued waiter: must return context.DeadlineExceeded and roll back reservation.
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if err := WaitXRequest(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancelled wait err = %v, want DeadlineExceeded", err)
	}

	// Two concurrent callers should reserve distinct staggered slots (diff ~1s).
	var wg sync.WaitGroup
	times := make([]time.Time, 2)
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			errs[idx] = WaitXRequest(context.Background())
			times[idx] = time.Now()
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent wait %d err = %v", i, err)
		}
	}
	diff := times[1].Sub(times[0])
	if diff < 0 {
		diff = -diff
	}
	if diff < 800*time.Millisecond {
		t.Fatalf("concurrent slots not staggered: diff = %v", diff)
	}
}

func TestWaitXRequestDeadlineBeforeQueueNeverAdvancesLast(t *testing.T) {
	resetXRequestsForTest(t)

	if err := WaitXRequest(context.Background()); err != nil {
		t.Fatalf("first wait: %v", err)
	}
	xRequests.Lock()
	initialLast := xRequests.last
	xRequests.Unlock()

	for i := 0; i < 20; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		err := WaitXRequest(ctx)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("burst %d err = %v, want DeadlineExceeded", i, err)
		}
	}

	xRequests.Lock()
	afterLast := xRequests.last
	waiterCount := len(xRequests.waiters)
	xRequests.Unlock()

	if !afterLast.Equal(initialLast) {
		t.Fatalf("xRequests.last advanced from %v to %v on rejected short-deadline callers", initialLast, afterLast)
	}
	if waiterCount != 0 {
		t.Fatalf("waiters = %d, want 0", waiterCount)
	}
}

func TestWaitXRequestMiddleWaiterCancelDoesNotStallBehindWaiter(t *testing.T) {
	resetXRequestsForTest(t)

	// Next slot is ~200ms away.
	xRequests.Lock()
	xRequests.last = time.Now().Add(-800 * time.Millisecond)
	xRequests.Unlock()

	waitForQueueLen := func(want int) {
		t.Helper()
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			xRequests.Lock()
			n := len(xRequests.waiters)
			xRequests.Unlock()
			if n >= want {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for queue len %d", want)
	}

	headDone := make(chan error, 1)
	go func() {
		headDone <- WaitXRequest(context.Background())
	}()
	waitForQueueLen(1)

	midCtx, cancelMid := context.WithCancel(context.Background())
	midDone := make(chan error, 1)
	go func() {
		midDone <- WaitXRequest(midCtx)
	}()
	waitForQueueLen(2)

	tailStart := time.Now()
	tailDone := make(chan error, 1)
	go func() {
		tailDone <- WaitXRequest(context.Background())
	}()
	waitForQueueLen(3)

	// Cancel the middle waiter before head finishes.
	cancelMid()
	if err := <-midDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("middle waiter err = %v, want Canceled", err)
	}
	if err := <-headDone; err != nil {
		t.Fatalf("head waiter err = %v", err)
	}
	if err := <-tailDone; err != nil {
		t.Fatalf("tail waiter err = %v", err)
	}
	// If middle waiter had consumed a slot, tail would wait ~2.2s instead of ~1.2s.
	if elapsed := time.Since(tailStart); elapsed > 1800*time.Millisecond {
		t.Fatalf("tail waiter stalled after middle cancel: elapsed = %v", elapsed)
	}
}

func TestWaitXRequestAheadCancelLetsBehindWaiterSucceed(t *testing.T) {
	resetXRequestsForTest(t)

	// Next slot is ~150ms away.
	xRequests.Lock()
	xRequests.last = time.Now().Add(-850 * time.Millisecond)
	xRequests.Unlock()

	headCtx, cancelHead := context.WithCancel(context.Background())
	headDone := make(chan error, 1)
	go func() {
		headDone <- WaitXRequest(headCtx)
	}()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		xRequests.Lock()
		n := len(xRequests.waiters)
		xRequests.Unlock()
		if n == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Behind waiter has a 500ms deadline: enough if head cancels, not enough if forced to wait 1.15s.
	behindCtx, cancelBehind := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancelBehind()
	behindDone := make(chan error, 1)
	go func() {
		behindDone <- WaitXRequest(behindCtx)
	}()

	time.Sleep(30 * time.Millisecond)
	cancelHead()

	if err := <-headDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("head err = %v, want Canceled", err)
	}
	if err := <-behindDone; err != nil {
		t.Fatalf("behind waiter err = %v, want nil after head cancelled", err)
	}
}

func TestWaitXRequestSlowModeTransitions(t *testing.T) {
	t.Run("slow_to_normal", func(t *testing.T) {
		resetXRequestsForTest(t)
		now := time.Now()
		xRequests.Lock()
		// Last request was 1.5s ago; slow mode ends in 120ms.
		// Once slowUntil expires, normal 1s interval is already satisfied so caller should pass at ~120ms.
		xRequests.last = now.Add(-1500 * time.Millisecond)
		xRequests.slowUntil = now.Add(120 * time.Millisecond)
		xRequests.Unlock()

		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		start := time.Now()
		if err := WaitXRequest(ctx); err != nil {
			t.Fatalf("WaitXRequest across slow->normal err = %v, want nil", err)
		}
		elapsed := time.Since(start)
		if elapsed < 90*time.Millisecond || elapsed > 450*time.Millisecond {
			t.Fatalf("slow->normal elapsed = %v, want ~120ms", elapsed)
		}
	})

	t.Run("normal_to_slow_wakes_head_waiter", func(t *testing.T) {
		resetXRequestsForTest(t)
		xRequests.Lock()
		// Normal slot in 300ms, slow slot in 2.3s.
		xRequests.last = time.Now().Add(-700 * time.Millisecond)
		xRequests.Unlock()

		ctx, cancel := context.WithTimeout(context.Background(), 1000*time.Millisecond)
		defer cancel()

		start := time.Now()
		done := make(chan error, 1)
		go func() {
			done <- WaitXRequest(ctx)
		}()

		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			xRequests.Lock()
			n := len(xRequests.waiters)
			xRequests.Unlock()
			if n == 1 {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}

		SlowXRequests()
		err := <-done
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("normal->slow head waiter err = %v, want DeadlineExceeded", err)
		}
		if elapsed := time.Since(start); elapsed > 400*time.Millisecond {
			t.Fatalf("head waiter did not wake promptly on SlowXRequests: elapsed = %v", elapsed)
		}
	})
}
