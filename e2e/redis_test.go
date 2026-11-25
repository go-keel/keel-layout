//go:build e2e

// Example file: delete the whole file after forking.

package e2e

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/go-keel/keel/conf"
	"github.com/go-keel/keel/core/kconfig"
	"github.com/go-keel/keel/core/kredis/lock"
	"github.com/go-keel/keel/core/kredis/ratelimit"
	"github.com/go-keel/keel/core/ksnowflake"

	"github.com/go-keel/keel-layout/e2e/harness"
)

// The template exposes no endpoint over Keel's lock, rate limiter or snowflake IDs, so these tests call the packages
// directly against the round's Redis, as a service's own code would.

func wantRule(t *testing.T, err error, id string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), id) {
		t.Fatalf("got %v, want keel/%s", err, id)
	}
}

// Basis: keel:docs/redis.md, "Distributed lock"; keel:docs/errors.md, kredis.lock-ttl.
func testRedisLock(t *testing.T, e *env) {
	ctx := context.Background()
	l := lock.NewLocker(e.rdb)
	key := "e2e:lock:" + uniq("k")
	unlock, err := l.TryAcquireLock(ctx, key, 30)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.TryAcquireLock(ctx, key, 30); !errors.Is(err, lock.ErrLockNotAcquired) {
		t.Fatalf("second acquire: %v, want ErrLockNotAcquired", err)
	}
	unlock()
	unlock, err = l.TryAcquireLock(ctx, key, 30)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	unlock()
	_, err = l.TryAcquireLock(ctx, key, 0)
	wantRule(t, err, "kredis.lock-ttl")

	// An expired holder releasing late must not delete the next holder's lock, and logs an ERROR.
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(prev)
	late, err := l.TryAcquireLock(ctx, key, 1)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the 1 s lock expired", func() bool { return e.rdb.Exists(ctx, key).Val() == 0 })
	second, err := l.TryAcquireLock(ctx, key, 30)
	if err != nil {
		t.Fatalf("acquire after the ttl: %v", err)
	}
	late()
	if _, err := l.TryAcquireLock(ctx, key, 30); !errors.Is(err, lock.ErrLockNotAcquired) {
		t.Fatalf("the late release freed the second holder's lock: %v", err)
	}
	second()
	if !strings.Contains(logs.String(), `"level":"ERROR"`) || !strings.Contains(logs.String(), `"ttl_exceeded":true`) {
		t.Fatalf("the late release logged %s", logs.String())
	}
}

// Basis: keel:docs/redis.md, "Rate limiting"; keel:docs/errors.md, the kredis.ratelimit-* rules.
func testRedisRatelimit(t *testing.T, e *env) {
	ctx := context.Background()
	_, err := ratelimit.New(e.rdb, 0)
	wantRule(t, err, "kredis.ratelimit-bucket")
	lim, err := ratelimit.New(e.rdb, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	key := "e2e:rl:" + uniq("k")
	window := 300 * time.Millisecond
	for i := 1; i <= 3; i++ {
		if err := lim.Allow(ctx, key, 3, window); err != nil {
			t.Fatalf("allow #%d: %v", i, err)
		}
	}
	if err := lim.Allow(ctx, key, 3, window); !errors.Is(err, ratelimit.ErrLimitExceeded) {
		t.Fatalf("allow #4: %v, want ErrLimitExceeded", err)
	}
	wantRule(t, lim.Allow(ctx, key, 3, 150*time.Millisecond), "kredis.ratelimit-window")
	wantRule(t, lim.Allow(ctx, key, 0, window), "kredis.ratelimit-limit")
	// The window slides on the Redis server's clock: wait it out, then one more is allowed.
	time.Sleep(window + 50*time.Millisecond)
	if err := lim.Allow(ctx, key, 3, window); err != nil {
		t.Fatalf("after the window: %v", err)
	}
}

// Basis: keel:docs/utilities.md, "Snowflake IDs"; keel:docs/errors.md, ksnowflake.allocator-mismatch.
func testSnowflake(t *testing.T, e *env) {
	appDomain, appName := domain(t), harness.Config(t, "config.yaml", "app.name")
	cfg := func(node int64) *kconfig.Config {
		return &kconfig.Config{Config: &conf.Config{App: &conf.App{Domain: appDomain, Name: appName, Id: "e2e", NodeId: node}}}
	}
	_, _, err := ksnowflake.NewAuto(cfg(7), e.rdb)
	wantRule(t, err, "ksnowflake.allocator-mismatch")
	_, err = ksnowflake.NewStatic(cfg(-1))
	wantRule(t, err, "ksnowflake.allocator-mismatch")

	st, err := ksnowflake.NewStatic(cfg(7))
	if err != nil {
		t.Fatal(err)
	}
	a, _ := st.Next()
	b, _ := st.Next()
	if nodeOf(a) != 7 || b <= a {
		t.Fatalf("static: node %d, %d then %d", nodeOf(a), a, b)
	}

	g1, c1, err := ksnowflake.NewAuto(cfg(-1), e.rdb)
	if err != nil {
		t.Fatal(err)
	}
	defer c1()
	g2, c2, err := ksnowflake.NewAuto(cfg(-1), e.rdb)
	if err != nil {
		t.Fatal(err)
	}
	defer c2()
	x, err := g1.Next()
	if err != nil {
		t.Fatal(err)
	}
	y, err := g2.Next()
	if err != nil {
		t.Fatal(err)
	}
	if nodeOf(x) == nodeOf(y) || x.Base58() == "" {
		t.Fatalf("two leased generators share node %d", nodeOf(x))
	}
}

// nodeOf reads the 10 node bits of a snowflake ID (12 sequence bits below them).
func nodeOf(id ksnowflake.ID) int64 { return (id.Int64() >> 12) & 0x3ff }
