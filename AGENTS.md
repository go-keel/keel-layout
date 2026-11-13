# Agent guide

<!-- shared segment begin keel-layout: carried verbatim; never localized outside its source repository. Checked by scripts/check-cross-repo-sync.sh; see docs/upgrading-protocol.md. -->

# Repository guide

This repository is a service on the Keel framework (Kratos v3, standard `slog`, hand-written assembly), derived from
the keel-layout template. Keel's own docs are linked as `keel:docs/<page>.md`: the files of that name in the Keel
module your `go.mod` resolves to (`go list -m -f '{{.Dir}}' github.com/go-keel/keel`).

**This file has segments.** Text between `shared segment begin` / `shared segment end` comes verbatim from upstream:
keel-layout's segment first, then one per further template on the chain. Follow it; change it in its source repository,
not here. Text outside the segments is this repository's own. `docs/guides/` (playbooks per task) and `docs/practices/`
(rules independent of the task) are shared files too, checked by `make check-sync`; this repository's own conventions go
in new files routed from outside the segments.

## Done

A change is done when `make check` passes; run it before every commit. After changing data-layer code or a migration,
also run `make integration`. Generated files (`*.pb.go`, `*_grpc.pb.go`, `*_http.pb.go`,
`*_errors.pb.go`, `openapi.yaml`, `third_party/`) come only from `make all`; never edit them, never `sed` a `.pb.go`.

## Read before you act

Read the page before doing the task. A routing table outside the segments adds this repository's own pages.

| Before you | Read |
|---|---|
| write any Go code, or doubt a pattern | `docs/practices/go.md` |
| add a package, a resource or a dependency between layers; choose a name | `docs/practices/structure.md` |
| add or change an HTTP / gRPC endpoint, list endpoints included | `docs/guides/api-endpoint.md` |
| add a webhook, SSE, file download, log stream or pre-routing filter | `docs/guides/raw-http.md` |
| publish or consume events | `docs/guides/mq.md` |
| add an async task or a cron job | `docs/guides/task.md` |
| call another service (gRPC / HTTP) | `docs/guides/grpc-client.md` |
| change `di.go`, add a `cmd/`, add infrastructure | `docs/guides/assembly.md` |
| add a domain error, error code or sentinel mapping | `docs/guides/errors.md` |
| add a domain model, GORM model or repo | `docs/guides/domain-model.md` |
| create or change a table, write a migration | `docs/practices/database.md` |
| open a transaction, lock, conditional update, concurrent writes to one row | `docs/practices/transaction.md` |
| configure authentication, anonymous endpoints, claims | `docs/guides/auth.md` |
| add a config field | `docs/guides/config.md` |
| log, trace, add metrics | `docs/guides/observability.md` |
| deploy behind nginx; anything about the client IP | `docs/guides/deploy-nginx.md` |
| write tests | `docs/guides/testing.md` |
| write an error message, an i18n entry or any text end users read | `docs/practices/user-text.md` |
| follow upstream (a new template or Keel version); change something derived repositories must act on; have a commit stopped for want of an `UPGRADING.md` entry | `docs/upgrading-protocol.md` |
| write an ADR | `docs/adr/layout-0004-adr-naming-and-carry-protocol.md` |
| look up what Keel provides | `keel:docs/README.md` |
| use a term | `GLOSSARY.md` |

## Rules for every change

- **Use Keel first.** HTTP/gRPC servers, event consumers, tasks, cron, long-running workers, distributed locks, rate
  limits, snowflake IDs, JWT, password hashing, decimal conversion, redaction and hot-reloaded config come from Keel;
  do not rewrite them or add a library for them.
- **One concept, one home.** Each business rule, query criterion or state transition is defined in one function or repo
  method. Before coding, list the new concepts and their homes; when logic appears a second time, extract it first.
- **Layers point one way**: service → biz → data, wired only in `di.go` (`docs/practices/structure.md`).
- **Keel and upstream template code are read-only** (module cache, `go.work` targets, sibling checkouts). When one of
  them gets in the way (a bug, a missing mechanism, a rule that does not fit), open an issue in that repository: Keel's
  for the framework, your direct upstream's for the template. Give the reproduction, the suspected cause and what you
  need; upstream weighs it against its own scope, and a fix arrives when this repository follows upstream. Until then,
  work around it in this repository's own code.
- **Before changing a mechanism, a public API or a config key**, `grep -rn "<keyword>" --include="*.md" .` and update
  every doc it hits (shared ones in their source repository).

## Ask before acting

Report the plan and its impact, then wait for confirmation, when the work involves (additions outside the segments
apply too):

1. a fix that needs a change to the external API or observable behaviour;
2. a conflict with an existing decision (`AGENTS.md`, `GLOSSARY.md`, `docs/adr/`, specs listed outside the segments,
   commit history);
3. a high-risk or destructive problem (data loss, a security hole);
4. a new third-party dependency;
5. **a new file outside the business area.** New files in `internal/biz/`, `internal/data/`, `internal/service/`,
   `api/`, `migrations/` (and business areas listed outside the segments) need no question; everywhere else (new
   directories, a new `cmd/`, the repository root, `docs/`, `scripts/`, `internal/conf/`, `internal/server/`) ask.

## Git

Conventional Commits; a regenerated file goes in the same commit as its source. `configs/` holds only local development
placeholders, never real credentials. Push or tag only when asked; the permission covers that one push.

## Decisions

`GLOSSARY.md` decides wording; `docs/adr/` holds decisions. State a contradiction with them explicitly instead of
overriding it silently.

<!-- shared segment end keel-layout -->

## This repository: keel-layout

**Maintenance.** This template is maintained only by its maintainer and their agents, inside the maintainer's
workspace with this repository checked out in the workspace's directory layout; the rules for changing the template
itself live in the workspace's `AGENTS.md`. A derived repository rewrites this section as its own.

Module: `github.com/go-keel/keel-layout`. Entries: `cmd/api` (HTTP), `cmd/svc` (gRPC), `cmd/worker` (event consumers,
tasks and cron).

### Example code

A Todo resource runs through every entry as a living example of the shared rules; shared docs never cite it, and a
derived repository usually deletes it (`README.md`, "Delete the examples"). Where to look:

- Full resource: `api/keel_layout/v1/todo.proto` → `internal/service/todo.go` → `internal/biz/todo.go` →
  `internal/data/todo.go`, table in `migrations/business/0_schema.sql`, data-layer tests on MySQL
  (under `make integration`) in `internal/data/todo_integration_test.go`.
- List endpoint: `ListTodo` (tri-state filter `completed`, sort enum `sort`, list row `TodoListItem`, default page size in
  biz).
- Streaming RPCs: `WatchTodo` (server stream → SSE), `StreamTodoLookup` (bidirectional → WebSocket); binary response:
  `ExportTodo` (`google.api.HttpBody`).
- Raw routes: under `/examples/`, registered in `internal/server/http.go`.
- Event and task contracts and how tests replace them: `internal/biz/todo.go`, `internal/biz/todo_test.go`.
