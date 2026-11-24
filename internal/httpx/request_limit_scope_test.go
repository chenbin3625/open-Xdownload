package httpx

import (
	"context"
	"testing"
	"time"
)

func TestWaitXRequestForScopeIsolatesIndependentScopes(t *testing.T) {
	scopeA := "test-scope-a-" + t.Name()
	scopeB := "test-scope-b-" + t.Name()
	t.Cleanup(func() {
		scopedXRequests.Lock()
		delete(scopedXRequests.items, scopeA)
		delete(scopedXRequests.items, scopeB)
		scopedXRequests.Unlock()
	})

	if err := WaitXRequestForScope(context.Background(), scopeA); err != nil {
		t.Fatalf("first scopeA wait: %v", err)
	}
	SlowXRequestsForScope(scopeA)

	// Slowing scopeA must not delay or block scopeB.
	start := time.Now()
	if err := WaitXRequestForScope(context.Background(), scopeB); err != nil {
		t.Fatalf("first scopeB wait: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("scopeB was unexpectedly delayed by scopeA slow mode: %v", elapsed)
	}

	// Second request in scopeA with a short deadline should fail due to scopeA's 3s slow interval.
	ctxA, cancelA := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancelA()
	if err := WaitXRequestForScope(ctxA, scopeA); err == nil {
		t.Fatal("expected scopeA slow mode to reject short-deadline second request")
	}
}
