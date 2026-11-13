<!-- Shared file: carried verbatim; never localized outside its source repository.
     Checked by scripts/check-cross-repo-sync.sh; see docs/upgrading-protocol.md. -->

# Database transactions and locking

Read this before writing any code that opens a transaction, takes a locking read (`SELECT … FOR UPDATE`), runs a
conditional update (`UPDATE … WHERE <condition>`, judging success by the affected rows), or writes rows that other
requests may write concurrently.

These are business-independent practices: principles, not specific tables. They target gorm with MySQL (InnoDB, default
isolation repeatable read); Keel's database component also supports PostgreSQL (default isolation read committed), and
every difference is pointed out where it occurs.

"Lock" here means the rows a locking read locks inside a transaction, and the row locks `UPDATE` and `DELETE` take
themselves: both are held from the moment the statement runs until commit or rollback. Inserting new rows usually
contends with no one and does not count as locking.

## Basic rules

1. **biz decides which writes succeed or fail together; data opens the transaction.** biz never touches a transaction
   handle; a business decision made after locking also belongs to biz, passed to the repo method as a function.
2. **Data used for decisions is read as a snapshot outside the transaction; inside, only targeted reads and writes.**
   A snapshot can only reject early; the final decision is a conditional update inside the transaction, or a recheck
   after locking.
3. **What one conditional update can do is not done as lock-then-write.**
4. **Lock only the rows you write, by primary key or unique index, as late as possible.**
5. **A transaction locking several rows follows the repository's single lock order: cold rows first, hot rows last,
   rows of one kind by ascending ID.**
6. **While holding locks, touch only the database.** No external requests, no Redis, no events, no tasks; these come
   after commit, with a recovery path when they fail.
7. **Every read and write in the transaction uses the same `tx`, and the transaction function can run again in full.**
8. **Unique constraints back uniqueness; the normal path depends on where duplicates come from.** Unique values typed
   by people are checked before writing; deduplication of program writes is one statement that skips or updates on
   a duplicate.

## Which layer holds the transaction boundary

"Placing the order and reducing stock succeed or fail together" is a business judgement and belongs to biz; how the
transaction is opened, which rows are locked and which statements run are storage details and belong to data. In
code, one thing that must be atomic is one method on the repo interface: biz calls it, data opens the transaction
inside. biz never sees `*gorm.DB` and does not need to know how many statements run underneath (layering in
`docs/practices/structure.md`).

When one thing must be atomic across two repos, do not put a transaction handle into biz to pass to both; give the
thing its own method in data that writes both sets of tables. Repos are cut by "unit that succeeds or fails together",
not by table.

When the decision needs values read after locking (say "at most 10 tags per card" counts the tags present right now),
the decision is a pure function in biz, passed to the repo method; data locks the row in the transaction, reads the
facts, calls the function and writes according to its result:

```go
// biz: the rule lives only here and is testable without a database
func checkTagLimit(current, adding int) error {
    if current+adding > maxTagsPerCard {
        return ErrCardTagLimitExceeded
    }
    return nil
}

// data: only fetches facts and writes
func (r *cardRepo) AddTags(ctx context.Context, cardID int64, tagIDs []int64,
    check func(current, adding int) error) error {
    return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
        // lock the card row, count its current tags n
        if err := check(n, len(tagIDs)); err != nil {
            return err
        }
        // write
    })
}
```

## Snapshots and the final decision

Filtering, sorting, reading config and computing candidates are read once before the transaction starts. Only a fact
where "the value read decides what is written, and must be the value at the moment of writing" is fetched inside the
transaction with a locking read or a conditional update.

A snapshot is for filtering and early rejection only, never the final decision: anyone may change it right after it
was read. Whether the write holds is decided by a conditional update in the transaction, or a recheck after locking.
When the two disagree, the snapshot may reject more, never admit more.

A plain read inside a transaction is stale too. Under MySQL repeatable read, after the first read the transaction
keeps seeing that moment's snapshot and misses later commits; under PostgreSQL read committed each statement sees the
latest commit, but someone can still slip in between "read, compute, write back". On neither database does a plain
read support read-then-write: use a locking read or a conditional update.

## Conditional updates

When the decision fits in a `WHERE`, run one update and judge by the affected rows. The update is the decision: it
saves the "lock, read, decide, write" round, and the lock is held only from this statement:

```go
res := tx.Model(&stock{}).
    Where("id = ? AND quantity >= ?", id, n).
    Update("quantity", gorm.Expr("quantity - ?", n))
if res.Error != nil {
    return res.Error
}
if res.RowsAffected == 0 {
    return errStockShort // no match: short of stock or no such row; the caller tells them apart
}
```

`FOR UPDATE` before writing is only for decisions that read several tables and do not fit in one condition.

**An update judged by its affected rows must always change some column.** MySQL counts "set to the same value" as 0
rows affected by default, indistinguishable from "condition not met"; PostgreSQL counts matched rows and has no such
trap. On MySQL, when the update may change nothing, decide with a locking read instead, or include a column that always
changes in the `SET` (such as a version incremented every time).

## Locks

**Lock rows, never tables.** No `LOCK TABLES`. A gorm locking read is
`tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&m, id)`, and its condition must hit specific rows by primary
key or unique index. Under MySQL repeatable read, a non-unique index or a range condition also locks the scanned rows
and the gaps between them, a whole stretch; PostgreSQL has no gap locks, but a wide condition still locks many rows.
A locking query whose index you cannot name is wrong.

**Do not lock rows you do not change.** Lock only the rows this transaction writes, or rows that must exclude other
writes; rows used for reference get a plain read.

**Lock as late as possible.** Compute amounts, build parameters and validate input before the transaction; lock only
at the step that must write, and commit soon after. Inserting new rows (new records, logs, details) contends with no
one, so it comes before locking hot rows; this assumes the table has no database foreign key (this template chain
creates none, see `docs/practices/database.md`, "No foreign key constraints"). With a foreign key, InnoDB's check on a
child insert takes a shared lock on the referenced parent row; two transactions each inserting a detail for the same
parent and then both updating that parent wait for each other in a deadlock. Then the parent row must be locked by
primary key with `FOR UPDATE` before inserting the child, and foreign-key parents belong in the lock order table.
PostgreSQL's foreign key check likewise takes a `FOR KEY SHARE` lock on the parent, for the same reason. Any check
that may end the transaction early also comes before locking hot rows.

**Lock several rows in a fixed order.** Two transactions locking the same two rows in opposite order deadlock. The
order is decided by heat: the more often a row is modified concurrently, the hotter it is and the later it comes.
Handling cold rows first often shows there is nothing more to do, so the hot row is never locked; when it must be
touched, it is held only at the end of the transaction. Several rows of one kind are locked by ascending ID; sort batch
input first.

**A repository has exactly one lock order table.** The heat criterion is only for drawing up that table; once it
exists, code follows the table: a transaction locks top to bottom, may skip, never goes back. Two people judging heat
on the spot will eventually lock something in reverse. The table lists concrete tables, so the business repository
writes it in its own docs; the template does not provide one.

**Locking reads do not join.** A locking read with a join locks rows in two tables in an order the database chooses,
easily against the order table. Read the IDs to change with a plain read first, then lock table by table in order.

**Update statements do not read other tables on the side.** Under MySQL repeatable read, `UPDATE … SET x = (SELECT …)`
and `INSERT … SELECT` take shared locks on the rows they read, an extra lock outside the order table. Read the values
first and pass them in as parameters.

## Parent-child consistency takes both sides

Without foreign key constraints the database does not stop orphan child rows, so consistency needs both "insert child"
and "delete parent" to cooperate:

- **Both sides first lock the parent row by primary key with `FOR UPDATE` in the same transaction**, then do their own
  part; if the lock finds no row, the parent is gone and the domain error "parent not found" is returned.
- The inserter, holding the parent lock, inserts the child; the deleter, holding the parent lock, runs a delete
  conditioned on "no child rows": 0 affected rows means children remain, with no need to check again.

```go
// Insert a child: lock the parent in the same transaction, then insert.
func (r *childRepo) Create(ctx context.Context, row *child) error {
    return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
        if err := lockParent(tx, row.ParentID); err != nil {
            return err
        }
        return tx.Create(row).Error
    })
}

// Delete a parent: lock the parent first too, then delete conditioned on "no child rows".
func (r *parentRepo) Delete(ctx context.Context, id int64) error {
    return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
        if err := lockParent(tx, id); err != nil {
            return err
        }
        res := tx.Where("id = ? AND NOT EXISTS (SELECT 1 FROM child WHERE parent_id = ?)", id, id).
            Delete(&parent{})
        if res.Error != nil {
            return res.Error
        }
        if res.RowsAffected == 0 {
            return biz.ErrParentHasChild // illustrative: use the domain's own sentinel
        }
        return nil
    })
}

// lockParent locks the parent row by primary key; no row means the parent is gone.
func lockParent(tx *gorm.DB, id int64) error {
    err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
        Select("id").Where("id = ?", id).First(&parent{}).Error
    if errors.Is(err, gorm.ErrRecordNotFound) {
        return biz.ErrParentNotFound // illustrative: use the domain's own sentinel
    }
    return err
}
```

A condition on the delete side alone is not enough: "inserter sees the parent exists → deleter deletes the empty
parent and commits → inserter inserts" still succeeds and leaves an orphan. Checking outside the transaction that the
parent exists only rejects early; it is not the final decision.

Both sides start by locking the parent, so whoever gets the lock first finishes first and the other decides after that
commit: if the deleter wins, the inserter then finds no parent and gets "parent not found"; if the inserter wins, the
deleter's conditional delete runs after the child exists and does not hold.

**Why the delete locks the parent first too.** In `NOT EXISTS (SELECT 1 FROM child WHERE parent_id = ?)` the subquery
has a constant and does not reference the outer row, so MySQL runs it during optimisation, taking shared locks on the
child table's parent-ID index before it locks the parent row. The inserter locks the parent and then inserts the child,
the opposite order, and concurrent runs deadlock (reproduced on MySQL 8.4 under repeatable read). Rewriting the
subquery to reference the outer row (`child.parent_id = parent.id`) also avoids it, but then the optimiser decides
which table is locked first, invisibly in the code, so that is not used. It is the same reason as "update statements
do not read other tables" and "locking reads do not join": when one statement touches two tables, code no longer
decides which is locked first, so the parent lock is taken separately first and the delete with its subquery only
decides "no child rows".

## Uniqueness and insert-if-absent

Idempotency, deduplication and "only one of this thing" are always unique constraints. A program checking "does it
exist" first cannot stop duplicates under concurrency; the constraint is the final backstop. Above it, the normal path
depends on where duplicates come from.

**Unique values typed by people are checked before writing.** Names, codes, card numbers typed into a form are
duplicated as normal use: before writing, a non-locking read checks, and a hit returns a domain error that also says
which field is duplicated. A write that still hits the unique key maps to the same domain error; that step only covers
two requests submitted at the same moment. Relying on the key collision for every duplicate without a pre-check is not
used: the database component logs a colliding statement at ERROR, so normal use would pollute the error log, and after
a collision MySQL does not tell which key it was.

**Deduplication of program writes is one statement.** Writes where duplicates are routine (importing records,
preventing double submission by request ID) neither check first nor rely on collision errors: MySQL uses
`INSERT … ON DUPLICATE KEY UPDATE`, PostgreSQL `INSERT … ON CONFLICT`, and gorm's `clause.OnConflict` (`DoNothing` to
skip, `DoUpdates` to update) generates both dialects.

**Never use a locking read to look up a key that may not exist.** Under MySQL repeatable read, a locking read that
finds no row locks the gap; two transactions doing that and then both inserting wait for each other's gap locks in a
deadlock. Pre-checks are always non-locking reads; a unique key collision (MySQL error 1062, PostgreSQL 23505) is
recognised in the data layer and mapped to a domain error or turned into the update path.

**Turning a collision into an update needs care on PostgreSQL.** In MySQL a transaction continues after a statement
fails on a duplicate key, so it can update instead in the same transaction. PostgreSQL cannot: after any failed
statement the transaction is aborted and every later statement returns `current transaction is aborted`; only rollback
remains. For "insert if absent, otherwise update" that also works on PostgreSQL, pick one: (1) one statement, MySQL
`INSERT … ON DUPLICATE KEY UPDATE`, PostgreSQL `INSERT … ON CONFLICT … DO UPDATE`, both generated by gorm's
`clause.OnConflict`; (2) a savepoint before the insert (gorm's `tx.SavePoint`), `tx.RollbackTo` after a collision,
then update; (3) the insert in its own short transaction, deciding the next step outside it after a collision. "Map
the collision to a domain error and stop" is unaffected, since it rolls back anyway.

## While holding locks, touch only the database

No network calls, downstream services, Redis reads or writes, events or tasks inside a transaction. Two reasons: an
external call takes uncontrolled time while the locks stay held and other requests queue for them; and if the
transaction then rolls back, a notification already sent cannot be taken back.

Do these after commit. Work after commit should not be interrupted when the request is cancelled; the `context`
handling is in `docs/practices/go.md`. When the commit succeeded and the follow-up did not, there must be a way to make
it up: the business state is in the database, so polling or event redelivery redoes the work from that state.
Broadcasting is for speed and stored state for correctness; "committed but never notified" must not become a permanent
inconsistency. A business that cannot lose a single event uses an outbox: the event stored in the business transaction,
sent by a separate process; the template does not demonstrate it.

Redis distributed locks versus database locks: a distributed lock only reduces simultaneous conflicts and useless
retries; it never replaces a conditional update and is never taken inside a transaction. Usage and the "locks are not
renewed" constraint: `docs/guides/domain-model.md`, "Distributed lock: one instance at a time".

## One `tx`, rollback and rerunning

**Every read and write in the transaction uses the same `tx`.** A closure that accidentally uses the repo's own
database handle runs that statement on another pooled connection: it does not see the transaction's uncommitted
changes, and on a row the transaction has locked it waits for itself until the lock wait times out (50 seconds by
default in MySQL). Helper functions called inside always take `tx *gorm.DB` as a parameter.

**Control rollback with the closure's return value.** gorm's `Transaction` rolls back when the closure returns a
non-nil error and commits on nil; the closure never calls `Commit` or `Rollback` itself. A business "this batch must not
be written" also rolls back by returning an error: define a sentinel used only in that file, return it from the
closure, recognise it outside with `errors.Is`, and translate it into a domain error.

```go
// errPartial is used only in this file to trigger a rollback; it never leaves the data layer.
var errPartial = errors.New("batch partially matched")

// DeleteBatch succeeds or fails as a whole: one missing id rolls back the batch.
func (r *itemRepo) DeleteBatch(ctx context.Context, ids []int64) error {
    err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
        res := tx.Where("id IN ?", ids).Delete(&item{})
        if res.Error != nil {
            return res.Error
        }
        if res.RowsAffected != int64(len(ids)) {
            return errPartial
        }
        return nil
    })
    switch {
    case errors.Is(err, errPartial):
        return biz.ErrItemNotFound // illustrative: use the domain's own sentinel
    case err != nil:
        slog.ErrorContext(ctx, "failed to delete items", "error", err, "count", len(ids))
        return errors.Join(biz.ErrUnexpectedError, err)
    }
    return nil
}
```

**The transaction function can run again in full.** A deadlock retry runs the whole closure again. The closure changes
no outside variables, sends no notifications, enqueues no tasks; results to hand out are computed in local variables
and assigned outside only after the transaction succeeds. Keel has no built-in deadlock retry; when needed, wrap one in
the data layer, recognised by error code (deadlock: MySQL 1213, PostgreSQL 40P01), with a bounded retry count.

**Deadlock handling is a backstop, not the norm.** The normal path avoids deadlocks by lock order, not by retrying.
Every deadlock retry and every lock wait timeout (MySQL 1205) is logged and counted; a rising rate means an order was
reversed somewhere again.

## Batches and tests

**Split large batches into one short transaction per item.** Scan the IDs to process outside the transaction, then
process each in its own short transaction; never loop inside one large transaction, never change thousands of rows in
one statement. One failure does not affect the others, and locks are held briefly. A batch can be interrupted and
resumed through the state of each item, not "where the last run stopped".

**Test locking on a real database.** Fakes have no row locks: lock order, deadlocks and lock wait timeouts cannot
be tested on them. Key concurrent paths (two operations changing the same row at once) need a truly concurrent
test on MySQL or PostgreSQL: several goroutines start together, and the test asserts consistent final data and the
expected domain error for the loser, so a later change cannot silently reverse the order. Real-database tests run under
`make integration` (`docs/guides/testing.md`).

## Checklist

- [ ] One thing that must be atomic is one repo method; biz has no transaction handle
- [ ] A decision made after locking is a function biz passes to the repo; the rule's numbers and conditions live only
  in biz
- [ ] Filtering, sorting and config are read as a snapshot outside the transaction; the snapshot only rejects early,
  the final decision is inside
- [ ] Read-then-write uses a locking read or a conditional update, not a plain read in the transaction
- [ ] What one conditional update can do is not lock-then-write; an update judged by affected rows always changes a
  column
- [ ] Every lock hits specific rows by primary key or unique index, and you can name the index; locking reads do not
  join
- [ ] Every locked row is written by this transaction or must exclude other writes
- [ ] Lock order follows this repository's order table, rows of one kind by ascending ID; checks that may exit early
  come before locking hot rows
- [ ] New rows are inserted before hot rows (assuming no foreign keys; with them, lock the parent first); update
  statements read no other tables
- [ ] Both the child insert and the parent delete lock the parent first; the parent delete is conditioned on no child
  rows
- [ ] Unique values typed by people are pre-checked with a non-locking read, and a key collision maps to the same
  domain error; program-write deduplication is one `OnConflict` statement; no locking read on a key that may not exist
- [ ] No external requests, Redis, events or tasks inside the transaction; work after commit has a recovery path
- [ ] Reads, writes and helpers in the transaction all use the same `tx`
- [ ] Rerunning the closure leaves no trace: no outside variables changed, results assigned only after success
- [ ] Deadlock retries and lock wait timeouts are logged and counted
- [ ] Batches use one short transaction per item; key concurrent paths have truly concurrent tests on a real database
