<!-- Shared file: carried verbatim; never localized outside its source repository.
     Checked by scripts/check-cross-repo-sync.sh; see docs/upgrading-protocol.md. -->

# Writing Go

Read this once before writing any Go code, and again when unsure whether a piece of code is right.

This page holds only the Go rules specific to this repository or contrary to common habit; general Go practice is
assumed. The hard rules are not repeated here: no schema changes from code in `docs/practices/database.md`; layering,
dependency direction, `decimal.Decimal` for amounts and typed enums for statuses in `docs/practices/structure.md`;
package-level slog in `docs/guides/observability.md`; constructors that only assign in `docs/guides/domain-model.md`.

## context

No `context.Background()` or `context.TODO()` on a request path: the trace breaks and work keeps running after the
request is cancelled (the logging side is in `docs/guides/observability.md`). Only process entry points, assembly code
and the root of a long-running loop create a `ctx`; tests use `t.Context()`.

Cleanup after a commit, or work that must finish after the request returns, uses `context.WithoutCancel(ctx)`: it
keeps the trace and metadata and drops the request's cancellation and deadline. It then has no timeout at all, so add
one:

```go
bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
defer cancel()
```

The caller sets timeouts; the callee only respects `ctx`. Do not add a hard-coded timeout deep down, which overrides
whatever the caller allowed.

## Errors

Which way errors travel and which layer logs them: `docs/guides/errors.md`. This section is about how to write them.

- To get a concrete error type, prefer `errors.AsType[*T](err)` over `errors.As` (one temporary variable fewer).
- An unexpected error underneath is logged once where it is recognised and returned as
  `errors.Join(biz.ErrUnexpectedError, err)`; upper layers pass the wrapped error on unchanged, without logging again.
- `fmt.Errorf("…: %w", err)` is only for internal errors read by developers (startup, assembly, config loading,
  tooling), never on a domain error chain that reaches a caller.

## Concurrency

Wait for a group with the `Go` method of `sync.WaitGroup` (Go 1.25+) instead of hand-written `Add` / `Done`. For
"cancel the rest on the first error" or a limit on concurrent runs, use `errgroup` (`WithContext` plus `SetLimit`). It
lives in `golang.org/x/sync`; the first direct import follows the AGENTS.md rule for new dependencies.

Work that must happen does not rely on a goroutine nobody waits for, which is lost on restart; hand it to a task or an
event, which retry.

## Time

Business logic that needs "now" does not call `time.Now()` directly; it holds a `now func() time.Time` field, set to
`time.Now` at assembly and to a fixed instant in tests.

Storage and transport use UTC; splitting by day (daily reports, daily limits) names the time zone explicitly instead
of relying on the server's. Durations in config are proto `google.protobuf.Duration`, written `30s` in YAML (see
`docs/guides/config.md`).

## Strings, integers and IDs

- Limit length in characters with `utf8.RuneCountInString`, not `len` (reason in `docs/guides/domain-model.md`,
  "Where field constraints live").
- IDs are `int64`, without negative special values (such as `-1` for "system"); the table side is in
  `docs/practices/database.md`, "Primary keys and IDs".

## JSON

Encode and decode structs with Keel's `pkg/json`, which keeps map keys ordered
([JSON](keel:docs/utilities.md#json-pkgjson)). Use the standard library directly only where it does not cover the
case (streaming reads and writes, output inlined into HTML that needs escaping), with a comment saying why. Proto
messages use `protojson`, never a general JSON library: field names, enums and `int64` encode differently.

## Packages

No `init()`, no package-level variables that change at run time: they make execution order and test isolation
unpredictable. Package-level sentinel errors and task and event contracts, never changed after startup, are fine.

## Tests

Seams, the strategy per layer and how to write fakes: `docs/guides/testing.md`. Time-dependent concurrent logic
(timeouts, retry intervals, timers) uses `testing/synctest`: inside `synctest.Test` time is fake, `time.Sleep` advances
at once, and `synctest.Wait` waits until every goroutine is blocked, with no real waiting.

## Discarded return values

When a return value really is discarded, write `_ = x.Close()` with a comment saying why it may be dropped.
`defer x.Close()` on a read-only resource is idiomatic and exempt.
