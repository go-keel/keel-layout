# Glossary

<!-- shared segment begin keel-layout: carried verbatim; never localized outside its source repository. Checked by scripts/check-cross-repo-sync.sh; see docs/upgrading-protocol.md. -->

This file is the glossary. The paragraphs delimited by `shared segment begin` / `shared segment end` come verbatim
from upstream and are the terms shared along the template chain; everything outside them belongs to this repo.

## Roles

Use these words when writing docs and commits.

**User** (of the framework):
A project that depends directly on the Keel framework, this repository included.
_Avoid_: consumer, downstream project

**Derived repo**:
A repo forked from one on the template chain, renamed, and evolving on its own.
_Avoid_: fork project, business project, business team

**Upstream**:
The repo you forked from, counting only the direct level, never further up. keel-layout has no upstream: it is
where distribution starts. The Keel framework is referenced by version through go.mod and is nobody's upstream.
_Avoid_: calling the Keel framework "upstream"; skipping levels (such as a derived repo bypassing its direct
upstream to treat an earlier level as its upstream)

**Consumer**:
Defined in Keel's GLOSSARY.md (the consuming side of a message queue, nothing else; a JetStream consumer is written
**durable**). Here it is a handler registered in `internal/server/`.
_Avoid_: using "consumer" for a user or a derived repo

## Layering concepts

Three model shapes flow through three layers, and `service` converts at the boundary.

**DTO** (Data Transfer Object):
The request/response types generated from proto. Only the `service` layer touches them.
_Avoid_: VO, request body, pb type

**DO** (Domain Object):
The pure domain model owned by `biz`, with no proto and no storage tags. It is what crosses layers.
_Avoid_: Entity, domain entity, model

**PO** (Persistent Object):
The GORM model private to `data`. It never leaves the `data` package: the type is named after its table in
lowerCamelCase without a `Model` suffix, and the lowercase start lets the compiler enforce this.
_Avoid_: Model, DB entity, table struct

**repo interface**:
The dependency-inversion seam that `biz` declares and `data` implements. Constructors return the interface, not the
concrete type.
_Avoid_: DAO, repository implementation

**Port**:
An interface `biz` declares for an external system **whose contract someone else defines**: a downstream gRPC
service, a third-party HTTP API, things that evolve independently of this service. Like repo it is an inversion
seam; the difference is that repo faces this service's storage, Port faces someone else's system.

Mechanisms the framework provides (`knats`, `kasynq`, `kdb`, `kredis`) **do not go through a Port**; `biz` uses
their types directly: Keel defines the contract, it follows the version, breaking changes are covered by UPGRADING,
and another wrapper would only duplicate the abstraction.
_Avoid_: wrapping framework mechanisms in a Port; calling a repo a Port (repo faces this service's storage)

**Assembly seam**:
`cmd/<app>/di.go`. The hand-written construction order, with cleanups released LIFO. Derived repos append here
without changing the structure. There are at least three more of the same shape, where upstream provides the
baseline and the derived repo appends to the same file:
`api/<domain>/<version>/error_reason.proto`, `internal/biz/errors.go` and `internal/service/errors.go`, and the
message files under `internal/i18n/`; any others this repo has are listed outside the segment.
**Syncing an assembly seam always means a manual merge, by design**: it is neither overwritten wholesale like the
zero-diff area below, nor left untouched by upstream like a derived repo's own business code.
_Avoid_: dependency injection, DI container (this project has no DI tool); protecting the assembly seam like a
framework file, or giving it up like a business file

**Zero-diff area**:
The parts a derived repo replaces wholesale without conflicts: in every repo at least those registered in
`scripts/cross-repo-sync.list` (framework scripts and the shared files and shared segments described below); a
repo that has more lists them outside the segment. It is what lets a derived repo follow upstream for the long run:
syncing never stalls on accumulated conflicts. _Avoid_: confusing it with "read-only": read-only means "do not change",
zero-diff means "replacing it causes no conflict"

**Generated artifact**:
A file the generate commands can recompute from source files in the repo: at least `*.pb.go` and `openapi.yaml`;
any others this repo has are listed outside the segment. Committed, but written only by the generate commands.
Trade-offs: `docs/adr/layout-0003-generated-artifacts-committed.md`.
_Avoid_: merging a generated artifact by hand like an assembly seam (merge its source instead)

**Ledger**:
`UPGRADING.md`: one entry per change derived repositories must act on, plus this repository's "following upstream"
entries. The upstream anchor of the newest following entry is the **sync point**, where the next sync starts. Rules:
`docs/upgrading-protocol.md`.
_Avoid_: changelog (bug fixes and new capabilities stay out); recording the sync point anywhere else

**Sentinel error**:
A stdlib `errors.New` value in `internal/biz/errors.go`, mapped by `service` to a Kratos error and proto reason.
The `biz` layer does not know Kratos.
_Avoid_: business exception, error code

## Conventions and sharing

**Shared file**:
A file carried verbatim along the template chain and never rewritten outside its source repo: framework scripts,
shared ADRs, the upgrading protocol, and the two kinds of convention docs below; listed in
`scripts/cross-repo-sync.list` (`docs/upgrading-protocol.md`, "Shared files"). Trade-offs:
`docs/adr/layout-0002-verbatim-shared-docs-and-segmented-entry-files.md`.
_Avoid_: "a locally adapted copy per repo" (convention docs are never localized)

**Shared segment**:
A passage in an entry file (`AGENTS.md`, `GLOSSARY.md`) delimited by a pair of whole-line HTML comments and taken
verbatim from upstream; both markers name the source repo. Each additional upstream level along the template chain adds
one segment, in order; outside the segments the file belongs to this repo.
_Avoid_: copying upstream's whole entry file and rewriting it; writing this repo's rules inside a segment

**Guide**:
The conventions under `docs/guides/` loaded per task: steps and rules for one kind of work (adding an endpoint,
assembly, publishing events...). Shared files, independent of any business
(`docs/adr/layout-0002-verbatim-shared-docs-and-segmented-entry-files.md`). _Avoid_: writing this repo's business,
example code paths or concrete config values into a guide

**Practice**:
The task-independent coding conventions under `docs/practices/`, one topic per page (writing Go code, database
design, database transactions...). Also shared files; they differ from guides in when they load: a guide loads per
task, a practice before writing that kind of code.
_Avoid_: confusing them with guides

## Keel terms in this template

Authentication, contract endpoint, raw route, event contract, task contract and process facility are defined in
Keel's GLOSSARY.md. Only what this template adds is written here.

**Authentication**:
Keel's server-side check only; business checks such as a ban check are a separate step after it
(`docs/guides/auth.md`).

**Contract endpoint** / **raw route**:
The raw-http guide numbers their kinds 1a-1d and 2a-2e.
_Avoid_: type 1, type 2

**Pre-routing filter**:
A filter mounted with `sc.Filter` that runs on every request of a port before routing, outside the middleware chain
(kind 2e in the raw-http guide).
_Avoid_: wrapping the server's whole `Handler`

<!-- shared segment end keel-layout -->

## This repo: keel-layout

keel-layout is a service template plus usage guide built on the Keel framework: it delivers layering conventions
and a skeleton, and becomes a real service once forked and renamed.

- The **zero-diff area** and **assembly seams** are only those listed in the shared segment; nothing is added.
- **Generated artifacts** also include `third_party/`, copied by `make proto` from the versions in `go.mod`.
- **Consumer**: registered in `internal/server/consumer.go`, hosted by `cmd/worker`.
