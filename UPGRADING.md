# Upgrading guide

This file holds entries only, one per change that actually happened; how to write entries and how to follow
upstream: `docs/upgrading-protocol.md`.

## 2026-11-19 · 1

**What changed.** Following upstream no longer reruns `make rename`. `make sync-upstream` writes upstream's shared
files and `norm` kernel trees into this repository, already carrying its module path (the same normalization
`make check-sync` compares with), deletes what upstream deleted in a directory entry, and only reports
shared segments. `make rename` runs once, when the repository is created, and refuses to rename a repository that
already has its own module path.

**Why.** Rerunning the rename over the whole repository rewrote the repository's own files by text on every upgrade;
only the files taken from upstream ever need the names changed.

**How to apply.**

1. Take `scripts/check-cross-repo-sync.sh` from upstream by hand this once (it carries no names); step 3 writes the
   other shared files, `docs/upgrading-protocol.md` included.
2. Add the `sync-upstream` target to your `Makefile`, as upstream's: `@WRITE=1 scripts/check-cross-repo-sync.sh`.
3. From now on follow with `make sync-upstream` instead of copying trees and rerunning `make rename`. Your
   `scripts/rename.sh` needs no change.

## 2026-11-18 · 1

**What changed.** A repository may vendor `.proto` files Keel does not ship into directories of its own under
`third_party/`. `scripts/sync-proto.sh` now replaces only the four directories it owns (`keel`, `protovalidate`,
`googleapis`, `kratos`) and leaves any other directory there alone, and `buf.yaml` is no longer a shared file: each
repository adds a module per directory of its own.

**Why.** A contract importing, say, `google/type/money.proto` had nowhere to put it: `make proto` deleted all of
`third_party/`, and `buf.yaml`, where the new directory must be declared, had to match upstream verbatim.

**How to apply.**

1. Take `scripts/sync-proto.sh` and the shared docs from upstream (`make check-sync` lists them).
2. Delete the `keel-layout buf.yaml` line from `scripts/cross-repo-sync.list`. Keep `buf.yaml` as it is; from now on
   it is yours, and later upstream changes to it arrive as ledger entries.
3. For each extra `.proto` your contract imports, put it at a fixed version under `third_party/<name>/` and add a module
   for that directory to `buf.yaml`, with the same lint and breaking ignores as the others.
