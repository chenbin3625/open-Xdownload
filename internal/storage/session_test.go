package storage

import (
	"context"
	"crypto/sha256"
	"path/filepath"
	"testing"
	"time"
)

func TestSessionExpiryAndRevocation(t *testing.T) {
	for _, test := range []struct {
		name      string
		age, idle time.Duration
		valid     bool
	}{
		{"active", time.Hour, time.Minute, true},
		{"absolute", SessionLifetime + time.Minute, time.Minute, false},
		{"idle", SessionIdleTimeout + time.Hour, SessionIdleTimeout + time.Minute, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, err := Open(filepath.Join(t.TempDir(), "test.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			ctx := context.Background()
			now := time.Now()
			if err := store.CreateSession(ctx, "token", now.Add(-test.age)); err != nil {
				t.Fatal(err)
			}
			hash := sha256.Sum256([]byte("token"))
			if _, err := store.db.Exec(`UPDATE app_sessions SET last_seen_at=? WHERE token_hash=?`, now.Add(-test.idle).UnixMilli(), hash[:]); err != nil {
				t.Fatal(err)
			}
			if valid, err := store.SessionValid(ctx, "token", now); err != nil || valid != test.valid {
				t.Fatalf("valid=%v err=%v", valid, err)
			}
			if err := store.DeleteSession(ctx, "token"); err != nil {
				t.Fatal(err)
			}
			if valid, err := store.SessionValid(ctx, "token", now); err != nil || valid {
				t.Fatalf("revoked session valid=%v err=%v", valid, err)
			}
		})
	}
}

func TestConcurrentSessionTouchesDoNotRejectOlderRequests(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Now()
	if err := store.CreateSession(ctx, "token", now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, when := range []time.Time{now.Add(time.Second), now} {
		if valid, err := store.SessionValid(ctx, "token", when); err != nil || !valid {
			t.Fatalf("out-of-order request rejected: valid=%v err=%v", valid, err)
		}
	}
	var lastSeen int64
	if err := store.db.Get(&lastSeen, `SELECT last_seen_at FROM app_sessions`); err != nil || lastSeen != now.Add(time.Second).UnixMilli() {
		t.Fatalf("last activity moved backwards: %d err=%v", lastSeen, err)
	}
}
