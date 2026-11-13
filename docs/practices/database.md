<!-- Shared file: carried verbatim; never localized outside its source repository.
     Checked by scripts/check-cross-repo-sync.sh; see docs/upgrading-protocol.md. -->

# Database design

Read this before creating or changing a table, or writing a schema or migration script.

These are business-independent table design rules: naming, keys and IDs, types, defaults, indexes, soft delete,
partitioned tables. They target MySQL 8 (InnoDB); Keel's database component also supports PostgreSQL, and every
difference is pointed out where it occurs. This page covers how to design tables, not how to organise migration
scripts (directories, the complete schema, baseline and incremental scripts, how each environment runs them:
`docs/guides/domain-model.md`, "Schema"). **Schema changes go only through the scripts under `migrations/`**, in the
same commit as the model change; creating tables from code (`AutoMigrate`, programmatic DDL) is forbidden. Its only
exception, partition maintenance, is under "Partitioned tables" below.

## Basic rules

1. **Table and column names are snake_case, table names singular; instant columns are `*_at`, ID columns `*_id`.**
2. **An entity table's primary key is an auto-increment `id BIGINT UNSIGNED`; every ID column is `BIGINT UNSIGNED`,
   with no negative special values.**
3. **No database foreign key constraints.** The application keeps referential integrity; unique constraints keep
   uniqueness.
4. **Character set `utf8mb4`, collation `utf8mb4_0900_ai_ci`; case-sensitive columns get a byte-wise collation.**
5. **Instants are `DATETIME(3)`, always UTC; no `TIMESTAMP`.**
6. **`NOT NULL` with a default wherever possible; NULL only means "has not happened yet" or "unknown".**
7. **Decimals are `DECIMAL`, statuses short string codes, structured data a `TEXT` type; no `FLOAT`, `ENUM` or `JSON`
   column types.**
8. **Indexes follow real queries; business uniqueness is a unique constraint; index names are `uk_` / `idx_` plus the
   column names, without the table name.**
9. **Schema scripts can run twice; every table and every non-obvious column has a comment.**
10. **Large tables kept only for a period are partitioned by time and expire by dropping whole partitions, not by
    large `DELETE`s.**

## Naming

Table, column and index names are lowercase snake_case. Table names are singular (`user`, `order_item`). Tables of one
domain share a prefix (`order`, `order_item`, `order_refund`). Column names do not repeat the table name: `status` in
table `order`, not `order_status`; a column carries another table's name only when it references it (`order_id`).

- ID columns: a reference to another table is `<other>_id`; the acting user is `*_by` (`created_by`).
- Time columns: instants are `*_at` (`created_at`, `paid_at`), dates only `*_date` (`birth_date`).
- Boolean columns: an `is_` prefix (`is_root`) or a past participle (`otp_bound`); a repository picks one and new
  tables follow it.
- Avoid SQL reserved words (`order`, `key`, `sensitive`); where one cannot be avoided, quote it with backticks
  consistently across the repository rather than relying on luck.

## Primary keys and IDs

Every entity table has `id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY`, append-only tables such as logs and
detail records included. A pure relation table (many-to-many, recording only who relates to whom) uses a composite
primary key without a surrogate key: the column order follows the most frequent query direction, parent ID first
(`PRIMARY KEY (user_id, role_id)`), and the second column gets its own index for the reverse direction. Once a
relation table grows its own attributes and is referenced on its own, it is an entity table: switch back to an `id`
primary key plus a unique key.

Columns referencing another table's ID (`*_id`, `*_by`) are `BIGINT UNSIGNED` too, the same type as the key they
reference. Go uses `int64` (see `docs/practices/go.md`, "Strings, integers and IDs"). IDs have no negative special
values; an ID column meaning "none" uses `0` or NULL, one choice per repository (auto-increment starts at 1, so `0` is
never a real ID). SQL never subtracts from IDs: a negative result on an unsigned column is an error in MySQL.

IDs exposed externally that must not reveal volume or allow sequential enumeration use snowflake IDs from Keel's
`core/ksnowflake`, still stored as `BIGINT UNSIGNED`, with no `AUTO_INCREMENT` on the key. Snowflake IDs are only
non-sequential: they contain a timestamp and are guessable, so credentials that must not be guessed (share links) use
a random token. No UUID strings as primary keys.

PostgreSQL has no unsigned integers: use `BIGINT`, auto-increment as `BIGINT GENERATED ALWAYS AS IDENTITY`; "no
negative values" still holds.

## No foreign key constraints

Schema scripts contain no `FOREIGN KEY`. The lock order "insert new rows first, lock hot rows last" in
`docs/practices/transaction.md`, "Locks", assumes there are none (a foreign key check takes a shared lock on the parent
row); and tables split across two databases cannot have one anyway.

The application keeps referential integrity: both the transaction that inserts a child row and the one that deletes
the parent first lock the parent row by primary key with `FOR UPDATE`, then do their own part: insert the child, or
run the delete conditioned on "no child rows" (checking that the parent exists outside the transaction only rejects
early and proves nothing). What happens to child rows before a parent is deleted (refuse, cascade, null out) is a
business decision. How the two sides cooperate and why deleting also locks first: `docs/practices/transaction.md`,
"Parent-child consistency takes both sides". "Must not repeat" is backed by a unique constraint.

## Character set and collation

Every table ends with `ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_0900_ai_ci`, never relying on the
server default. Scripts start with `SET NAMES utf8mb4 COLLATE utf8mb4_0900_ai_ci;`: the `mysql` client often defaults
to `latin1`, which garbles non-ASCII text in the script (comments, seed data).

The default collation ignores case and accents: `Abc` and `abc` collide under a unique key. Columns compared byte by
byte (password hashes, tokens, signatures, case-sensitive codes from external systems) get `COLLATE utf8mb4_bin` on
the column; binary content (ciphertext, raw digests) is `VARBINARY`.

PostgreSQL is the other way round: case-sensitive by default. For case-insensitive uniqueness, build a unique index on
`lower(column)`.

## Time

Instants are `DATETIME(3)` (millisecond precision), never `TIMESTAMP`. The database stores UTC, and the connection
string needs both parameters: `loc=UTC` decides how Go interprets the `DATETIME` it reads and writes,
`time_zone='+00:00'` decides which time zone `CURRENT_TIMESTAMP` reads in the database. With only the first,
`DEFAULT CURRENT_TIMESTAMP` still fills in the database server's local time, hours off from rows written by Go.

```sql
created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
```

Business code never fills `created_at` and `updated_at`: gorm fills them on write by the field names `CreatedAt` and
`UpdatedAt`, and the database defaults cover writes that bypass gorm (hand-written SQL, data-fix scripts). Business
instant columns (`paid_at`, `bound_at`) allow NULL, meaning "has not happened yet", and are `*time.Time` in Go.
Date-only columns are `DATE`. Durations are integers with the unit in the column name (`timeout_ms`, `ttl_seconds`).

PostgreSQL has no `DATETIME`: instants are `timestamptz(3)`; and no `ON UPDATE`, so only gorm fills `updated_at`.

## NULL and defaults

`NOT NULL` with a `DEFAULT` wherever possible. NULL is reserved for states that mean something in themselves: "has not
happened yet", "unknown".

Empty string and NULL are not mixed: an optional string column is `NOT NULL DEFAULT ''`. Likewise for numbers and
booleans: the default is Go's zero value or an explicit business initial value.

## Types

**Booleans and statuses.** A two-state switch is `TINYINT(1) NOT NULL DEFAULT 0`, `bool` in Go, converted by gorm
(PostgreSQL: `boolean`). A status with three or more states is a short string code `VARCHAR(32)`, matching a typed
enum in Go (docs/practices/structure.md, "Types and models"). No `ENUM` column type.

**Money and decimals.** Always `DECIMAL(precision, scale)`, never `FLOAT` or `DOUBLE`. The upper bounds of precision
and scale are set by the business repository and are uniform across it; the template does not prescribe them. Go uses
`decimal.Decimal`, and APIs use strings (docs/practices/structure.md, "Types and models"). PostgreSQL: `numeric`.

**Strings.** The `n` of `VARCHAR(n)` counts characters and follows the business limit, the same value as the API
length constraint (see `docs/guides/domain-model.md`, "Where field constraints live"). Longer text is a `TEXT`
type.

**Structured data.** Stored in a `TEXT` type and encoded in Go; no MySQL `JSON` column type and no `JSON_*` functions:
the two dialects differ in JSON semantics, and `''` is not a valid JSON document, so MySQL refuses to insert it (error
3140). A field you query by becomes a column of its own.

**Large fields.** `TEXT` types and large `VARBINARY` columns stay out of list queries: a list selects only the columns
it shows, and the detail view reads the large field.

## Indexes

Indexes follow real queries. Every `*_id` column has an index unless it already is the primary key or the first column
of a composite index (without foreign keys, the database does not create one for you). Append-only log tables get a
composite index on "subject + `created_at`" (`KEY idx_operator (operator_id, created_at)`), which serves lists filtered
by subject and paged by time, newest first.

Business uniqueness is always backed by a `UNIQUE KEY`; how the normal path checks for duplicates is in
`docs/practices/transaction.md`, "Uniqueness and insert-if-absent". An InnoDB index is at most 3072 bytes, 768
characters in `utf8mb4`; a longer column does not go into a unique key directly: store its digest in a column and put
that in the unique key.

Naming: unique indexes `uk_<columns>`, other indexes `idx_<columns>`, several columns joined by underscores
(`uk_role_res`, `idx_dept`), with readable abbreviations when too long. No table name; PostgreSQL index names are
unique per schema, so there prefix the table name.

## Soft delete

A table that needs soft delete gets `deleted_at DATETIME(3) NULL` with an index; the PO uses a `gorm.DeletedAt` field.
Whether to soft-delete is a business decision.

Soft delete conflicts with unique keys, so decide before creating the table: after a delete, may the same name or code
be used again?

- **No:** build the unique key as usual; the deleted row keeps the value, and a new row with it collides.
- **Yes:** either do not soft-delete, or add a substitute column to the unique key. `deleted_at` cannot go into the
  unique key directly: NULL does not take part in uniqueness, so two live rows with the same name, both NULL, both
  insert. The substitute column is `NOT NULL`: `0` for live rows, and on delete a value that never repeats, such as
  the row's own `id`; the unique key is on "code + substitute column". Deleting then means an update of that column,
  not gorm's default soft delete alone. PostgreSQL can use a partial unique index `WHERE deleted_at IS NULL`.

## Partitioned tables

Large tables kept only for a period (logs, records) are partitioned by time, and whole expired partitions are
dropped; tables that never expire are not partitioned.

- **The partition key and retention are fixed at table creation** and written in the table comment. Prefer an instant
  the platform writes itself; when the instant comes from outside, the entry point rejects values outside "start of
  retention to tomorrow", or data with a broken clock lands in a partition that is dropped at once, or much too late.
- **The primary key is `(id, <partition key>)`**; updates by primary key carry both columns. A business unique key
  must contain the partition key too, so choose the partition key from the deduplication condition: with an instant
  that is not part of it, the same item redone on another day is stored twice.
- **The table is created with one catch-all partition** `PARTITION p_max VALUES LESS THAN (MAXVALUE)`; the program
  splits future days off it with `REORGANIZE PARTITION p_max`, not `ADD PARTITION`, so writes do not fail when
  maintenance stops. Partition names carry the date (`p20261103`), in UTC.
- **The catch-all partition stays empty.** While a partition is split the whole table is read-only (only
  `LOCK=SHARED` is supported), and the time grows with the rows of the split partition (on 8.4, 2 million rows took
  about 1.7 seconds, with inserts waiting throughout). Therefore:
  - Create partitions ahead (say 7 days past today); maintenance never stops longer than that, so every split hits
    an empty partition.
  - Before splitting, check the catch-all partition (`SELECT 1 FROM <table> PARTITION (p_max) LIMIT 1`); if it has
    rows, do not split automatically: log an ERROR alert and let a person split at a low-write time.
  - The maintenance session first sets `SET SESSION lock_wait_timeout = 5` (sized to the write pause you can
    accept): splitting and dropping briefly need an exclusive metadata lock, queue behind unfinished transactions, and
    reads and writes queue behind them; on timeout, give up and retry next round.
- **A new table finishes its first partition creation before writes are opened**, and stays closed if that fails;
  otherwise every write lands in the catch-all partition and, by the rule above, can then only be alerted on. With an
  external instant as the partition key, the first creation starts at the beginning of retention, not today.
- **Expiry only drops partitions**, never a time-based bulk `DELETE`. The statement is
  `ALTER TABLE <table> ALGORITHM=INPLACE, LOCK=NONE, DROP PARTITION <partition>`: it requires not locking writes and
  fails on the spot when it cannot.
- **Maintenance runs on a schedule in the program**, the only exception to the schema rule at the top of this page: it
  issues only split and drop statements, on a hard-coded list of tables. It checks more often than the partition
  granularity (hourly for daily partitions); a failure logs an ERROR and the next round catches up. The maintenance
  account has `ALTER`, `CREATE` and `INSERT` (the documented requirement for `ALTER TABLE`; on 8.4 splitting checks only
  `ALTER`), plus `DROP` for dropping partitions.
- "Which partitions to create, which to drop" is a pure function with unit tests; the statements can only be verified
  against a real database.

PostgreSQL has no `REORGANIZE`: create ahead with `CREATE TABLE … PARTITION OF`, expire with `DROP TABLE` on the
partition. Without a `DEFAULT` partition, the number of days created ahead is how long maintenance may stop.

## Writing scripts

- Tables with `CREATE TABLE IF NOT EXISTS`, seed data with `INSERT … SELECT … WHERE NOT EXISTS`. MySQL's
  `ALTER TABLE` has no `IF NOT EXISTS`; a change script that cannot run twice says in its header comment that it runs
  once, and its preconditions.
- Each table is preceded by a `--` comment saying what it stores; non-obvious columns get a comment with their purpose
  and values (which status codes exist, what `0` means).
- Changing a column with `MODIFY` or `CHANGE` rewrites the whole column definition: an omitted `NOT NULL`, `DEFAULT`
  or `COMMENT` is dropped. Changing only the default uses `ALTER COLUMN … SET DEFAULT`.
- Directories, the complete schema, baseline and incremental scripts, and how each environment runs them:
  `docs/guides/domain-model.md`, "Schema".

## Checklist

- [ ] Table names singular, snake_case, one prefix per domain; column names without the table name; no reserved words
- [ ] Entity tables have an auto-increment `id BIGINT UNSIGNED`; pure relation tables a composite key, parent ID first,
  with an index for the reverse direction
- [ ] ID columns `BIGINT UNSIGNED`, the same type as the key they reference; no negative special values; no
  subtraction on IDs in SQL
- [ ] No `FOREIGN KEY`; reference checks for child inserts and parent deletes are in the application, with the
  business handling defined
- [ ] Engine, character set and collation at the end of each table; `SET NAMES` at the start of the script;
  case-sensitive columns have their own collation
- [ ] Instant columns `DATETIME(3)`, no `TIMESTAMP`; the connection string has `loc=UTC` and `time_zone='+00:00'`
- [ ] `created_at` and `updated_at` have database defaults and are not filled by business code; duration columns name
  their unit
- [ ] Every nullable column has a stated meaning for NULL; optional strings are `NOT NULL DEFAULT ''`
- [ ] Two states are `TINYINT(1)`; more are string codes matching a typed Go enum; no `ENUM`
- [ ] Decimals are `DECIMAL`, no `FLOAT` or `DOUBLE`; structured data is a `TEXT` type, no `JSON` columns or `JSON_*`
  functions
- [ ] `VARCHAR` lengths equal the API constraints; large fields stay out of list queries
- [ ] Every index serves a real query; `*_id` columns are indexed; business uniqueness has a `UNIQUE KEY`; index names
  have no table name
- [ ] Soft-deleted tables have decided whether unique values may be reused, with a matching unique key
- [ ] Partitioned tables: only for large expiring tables; unique keys contain the partition key; created with only
  the `MAXVALUE` catch-all; partitions created ahead, no automatic split when the catch-all has rows; a new table
  opens writes only after its first partition creation; the maintenance session sets `lock_wait_timeout`; expiry
  drops partitions (with `LOCK=NONE`), no bulk `DELETE`
- [ ] Schema scripts can run twice; tables and non-obvious columns have comments; `MODIFY` restates the whole column
