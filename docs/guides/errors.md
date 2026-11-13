# Errors end to end (domain errors, reasons, sentinel, i18n)

<!-- Shared file: carried verbatim; never localized outside its source repository.
     Checked by scripts/check-cross-repo-sync.sh; see docs/upgrading-protocol.md. -->

Read this before defining a domain error, choosing an error reason or code, changing the sentinel mapping, translating
an error message, or deciding which layer logs an error.

The rule: **log technical errors and keep them from the caller; return a friendly message chosen by business meaning,
and never leak technical details to the client.** What sentinel does with matched and unmatched errors is in
`keel:docs/middleware.md#sentinel`; where errors are logged and why, in
`keel:docs/architecture.md#errors-and-logs-log-each-error-once`.

## How an error travels

```
error_reason.proto  reason enum + errors.code → make proto generates v1.ErrorXxx / v1.IsXxx
        │
data       recognises driver errors → expected conditions become domain errors; anything else is logged and wrapped
           in ErrUnexpectedError
        │
biz        passes data's errors through; unmet business conditions return domain errors; downstream gRPC errors
           pass through
        │
service    passes through unchanged (no translation)
        │
sentinel   matches domain errors with errors.Is → Kratos errors (with i18n text and HTTP code)
        │
fallback   unmatched → context errors become 499/504; downstream Kratos errors with details pass through; the rest is
           logged and becomes 500 INTERNAL
```

## Steps: adding a domain error

1. Add a reason to `api/<domain>/<version>/error_reason.proto` with `(errors.code)` (numbers continue the file's
   existing ranges; when an upstream baseline and this repository's additions use separate ranges, follow the range
   convention written in the file) → `make proto`, which generates the `v1.ErrorXxx(format, args...)` constructor and
   the `v1.IsXxx(err)` check.
2. Define the domain sentinel in `internal/biz/errors.go` (plain `errors.New`, no proto dependency). Name it `Err` +
   domain noun + state or action: `ErrOrderNotFound`, `ErrInsufficientFunds`.
3. Data returns it when it recognises the matching driver condition (`gorm.ErrRecordNotFound`, `gorm.ErrDuplicatedKey`,
   `redis.Nil`, ...); upper layers never branch on driver errors.
4. Add the mapping to the sentinel group in `internal/service/errors.go`: `On` (no i18n) or `OnLocaled` (translated
   text). **Every new domain error needs a mapping**, or it falls through to 500.
5. For translated text, add the entries for every language under `internal/i18n/`. Whether to extract and merge with
   the tools or write them by hand, and the file names, follow the i18n targets in this repository's `Makefile`, if it
   has them, and the files already in `internal/i18n/`.

Done when `make check` passes and a test or request returns the new reason with the expected code.

## HTTP status codes

**HTTP success is always 200. For a failure, first choose the gRPC code by the definitions in `google.rpc.Code`, then
take the HTTP code from this table.**

| gRPC | HTTP |
|---|---|
| INVALID_ARGUMENT, OUT_OF_RANGE | 400 |
| UNAUTHENTICATED | 401 |
| PERMISSION_DENIED | 403 |
| NOT_FOUND | 404 |
| ALREADY_EXISTS, ABORTED | 409 |
| FAILED_PRECONDITION | 412 |
| RESOURCE_EXHAUSTED | 429 |
| INTERNAL, UNKNOWN, DATA_LOSS | 500 |
| UNIMPLEMENTED | 501 |
| UNAVAILABLE | 503 |
| DEADLINE_EXCEEDED / CANCELLED | 504 / 499 (set by the sentinel fallback, never by hand) |

- Use no HTTP code outside the table: when the error crosses services over gRPC, Kratos turns an unknown code into
  `UNKNOWN`, which arrives as 500.
- Where several gRPC codes share an HTTP code, converting back keeps only one (400 → INVALID_ARGUMENT, 409 → ABORTED,
  500 → INTERNAL); `reason` is what tells them apart.

## Code patterns

The examples use fictional wallet, order and user domains to show several reasons and wrapping cases; these errors
need not exist in your `internal/biz/errors.go`. Use your own domain.

### Reason enum (`api/<domain>/<version>/error_reason.proto`)

```proto
enum ErrorReason {
  option (errors.default_code) = 500;

  UNEXPECTED_ERROR = 0 [(errors.code) = 500];
  INVALID_ARGUMENT = 1 [(errors.code) = 400];
  USER_NOT_FOUND = 100 [(errors.code) = 404];
  ORDER_NOT_FOUND = 200 [(errors.code) = 404];
  INSUFFICIENT_FUNDS = 201 [(errors.code) = 412];
}
```

### Domain sentinels (internal/biz/errors.go)

```go
package biz

import "errors"

var (
	ErrUnexpectedError   = errors.New("unexpected error") // wraps unexpected underlying errors
	ErrInvalidArgument   = errors.New("invalid argument")
	ErrUserNotFound      = errors.New("user not found")
	ErrOrderNotFound     = errors.New("order not found")
	ErrWalletExists      = errors.New("wallet already exists")
	ErrInsufficientFunds = errors.New("insufficient funds")
)
```

### Data: recognise the driver error → log → wrap

```go
func (r *walletRepo) GetByUserID(ctx context.Context, userID uint64) (*biz.Wallet, error) {
	var m wallet
	err := r.db.WithContext(ctx).Where("user_id = ?", userID).First(&m).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// an expected business case → a specific domain error; no log, or warn/info as needed
			return nil, biz.ErrWalletNotFound
		}
		// unexpected (connection lost, timeout, ...) → log at error and wrap; the details stay in data
		slog.ErrorContext(ctx, "failed to get wallet by userId",
			"error", err,
			"userId", userID,
		)
		return nil, errors.Join(biz.ErrUnexpectedError, err)
	}
	return m.toBiz(), nil
}

func (r *walletRepo) Create(ctx context.Context, w *biz.Wallet) error {
	m := wallet{UserID: w.UserID}
	if err := r.db.WithContext(ctx).Create(&m).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			// Unique key collision → a domain error, not logged again (kdb already logged the statement as ERROR).
			// Biz checks first on the normal path, so only a race gets here. The only key this insert can hit
			// is uk_user_id (one wallet per user, auto-increment primary key), so it maps directly; on MySQL the
			// translated error does not say which key was hit, so an insert that can hit two keys cannot do this.
			return biz.ErrWalletExists
		}
		slog.ErrorContext(ctx, "failed to create wallet", "error", err, "userId", w.UserID)
		return errors.Join(biz.ErrUnexpectedError, err)
	}
	w.ID = m.ID
	return nil
}
```

"Only one of this may exist" is guaranteed by a unique constraint: biz checks values people enter first and returns the
domain error when one exists; a collision in data maps to the same error and only covers races; deduplicating writes
made by code use a single `OnConflict` statement instead of relying on the collision error (see
`docs/practices/transaction.md`). In the check, only the "not found" error lets the insert go on; a failed query is
returned as it is, never treated as "not found". Which GORM calls translate driver errors and which do not:
`keel:docs/database.md#driver-errors`.

### Biz: pass through, business conditions, fire-and-forget

```go
func (uc *WalletUseCase) Withdraw(ctx context.Context, userID uint64, amount decimal.Decimal) error {
	// A failure in an external component (lock, cache, ...) → log and wrap.
	// The lock only stops concurrent submissions by one user: it is not renewed and another request takes it
	// after 30 seconds. The balance cannot go negative because UpdateBalance does a conditional update in data
	// (deduct only if enough), not because of the lock.
	unlock, err := uc.locker.TryAcquireLock(ctx, fmt.Sprintf("wallet:withdraw:%d", userID), 30)
	if errors.Is(err, lock.ErrLockNotAcquired) {
		return ErrDuplicateRequest // a normal state: this user's other submission holds the lock
	}
	if err != nil {
		slog.ErrorContext(ctx, "failed to acquire wallet lock", "error", err, "userId", userID)
		return errors.Join(ErrUnexpectedError, err)
	}
	defer unlock()

	wallet, err := uc.repo.GetByUserID(ctx, userID)
	if err != nil {
		return err // already wrapped by data → pass through
	}
	if wallet.AvailableBalance.LessThan(amount) {
		return ErrInsufficientFunds // an unmet business condition → a domain error, not logged
	}
	if err := uc.repo.UpdateBalance(ctx, wallet.ID, amount.Neg()); err != nil {
		return err
	}
	// a downstream gRPC service → its error is already shaped; pass through
	if _, err := uc.accountClient.GetAccountInfo(ctx, &accv1.GetAccountInfoRequest{UserId: userID}); err != nil {
		return err
	}
	// fire-and-forget: log, do not fail the main flow
	if err := uc.producer.PublishWithdrawn(ctx, userID, amount); err != nil {
		slog.ErrorContext(ctx, "failed to publish withdraw event", "error", err, "userId", userID)
	}
	return nil
}
```

The service does not translate: `return s.uc.Withdraw(ctx, ...)` passes the error to sentinel unchanged.

### Wrapping at a glance

| Case | Pattern | Log level |
|------|---------|-----------|
| Not found, with business meaning | `return nil, biz.ErrXxxNotFound` | none / warn |
| Unique key collision (this insert can hit only that key; biz checks first) | `errors.Is(err, gorm.ErrDuplicatedKey)` → `return biz.ErrXxxExists` | none (kdb logged ERROR) |
| Unexpected DB, Redis or MQ error | log + `errors.Join(biz.ErrUnexpectedError, err)` | Error |
| Already wrapped by data or biz | `return err` | — |
| Downstream gRPC service error | `return err` (already shaped) | — |
| Unmet business condition | `return biz.ErrInsufficientFunds` | — |
| Fire-and-forget (publishing, ...) | log, do not return | Error |
| Merged for safety (sign-in, against enumeration) | map to one shared error | — |

### Sentinel mapping (internal/service/errors.go)

The mapping lives in `internal/service/errors.go`. Follow the function name and composition already in that file; when
an upstream template provides a base group, add this repository's mappings to it instead of creating another group:

```go
func NewErrorSentinel() *sentinel.Group {
	return sentinel.NewGroup().
		// translated text: OnLocaled + the generated v1.ErrorXxx (carries reason and HTTP code)
		OnLocaled(biz.ErrUserNotFound, translateUserNotFound).
		OnLocaled(biz.ErrUnexpectedError, translateUnexpectedError).
		// no translation: On + a helper (NotFound / Conflict / BadRequest / PreconditionFailed)
		On(biz.ErrDuplicateRequest, sentinel.Conflict("DUPLICATE_REQUEST"))
	// the fallback is sentinel.InternalWithLog; .Default(handler) replaces it
}

func translateUserNotFound(_ context.Context, localizer *goi18n.Localizer, _ error) error {
	msg := i18n.MustLocalizeMessage(localizer, &goi18n.Message{
		ID:    "ErrUserNotFound",
		Other: "User not found.", // English fallback when no translation file matches
	})
	return v1.ErrorUserNotFound("%s", msg)
}
```

`v1.ErrorUserNotFound` is generated from `error_reason.proto` and returns a Kratos error with reason `USER_NOT_FOUND`
and HTTP 404; a client checks it with `v1.IsUserNotFound(err)`.

### Enabling it in newApp (cmd/<app>/main.go)

```go
chain := mw.Default(c.Config)
mw.WithLocale(chain, i18n.NewBundle()) // locale first, so OnLocaled has a localizer
mw.WithSentinel(chain, sg)
```

`sg` (`*sentinel.Group`) is built in `di.go` with the function from `internal/service/errors.go` and passed to
`newApp`.

## Which layer logs what

Each error is logged once, by the layer that knows the most about it
([Errors and logs](keel:docs/architecture.md#errors-and-logs-log-each-error-once)); the sentinel fallback
`InternalWithLog` is the last line, not the normal path.

```go
// right: ctx (links the trace_id) and structured key-value pairs
slog.ErrorContext(ctx, "failed to create user", "error", err, "userId", userID)

// wrong: fmt.Sprintf loses the structured fields
slog.ErrorContext(ctx, fmt.Sprintf("failed to create user %d: %v", userID, err))
```

## Anti-patterns

- ❌ Upper layers branching on driver errors (`errors.Is(err, gorm.ErrRecordNotFound)` in biz or service): driver
  errors stay in data and become domain errors before they leave it.
- ❌ Returning raw error text to the client (`fmt.Errorf("db error: %v", err)` passed all the way up): it leaks
  details. Unexpected errors are always `errors.Join(biz.ErrUnexpectedError, err)`.
- ❌ Logging the same error in data, biz and service: log once where it is wrapped; passing through logs nothing.
- ❌ A new domain error without a sentinel mapping: the client gets 500 INTERNAL instead of the intended code.
- ❌ Building Kratos errors by hand in the service instead of sentinel: codes and translations scatter and cannot be
  managed in one place.
- ❌ Logging without ctx (`slog.Error(...)` on a request path): the trace_id is lost and the log cannot be linked.
- ❌ Returning `v1.ErrorXxx(...)` (a transport error) from biz: biz only knows `biz.ErrXxx` sentinels; transport errors
  belong at the edge.

## Related

- Where the sentinel and locale middleware are mounted: `docs/guides/assembly.md`
- Extracting and merging i18n entries: the i18n targets in this repository's `Makefile`
- Logs and traces: `docs/guides/observability.md`
- `keel:docs/middleware.md#sentinel` and `keel:docs/middleware.md#locale`: the middleware itself
