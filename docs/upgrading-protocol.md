# Upgrade protocol: writing ledger entries and following upstream

<!-- Shared file: carried verbatim; never localized outside its source repository.
     Checked by scripts/check-cross-repo-sync.sh; see docs/upgrading-protocol.md. -->

Read this before adding an entry to `UPGRADING.md` (the ledger), or before following your upstream repository.

This file holds the rules of the ledger: how entries are written, how anchors are chosen, and how a downstream
repository works out its to-do list. The records themselves stay in each repository's own ledger.

**The ledger only takes entries that need action in derived repositories**: a changed public API, a changed shape of
the kernel or an assembly seam, a schema change, or a change callers must make. New capabilities, internal refactors
and bug fixes stay out; so do changes to shared files or shared segments alone, which downstream simply overwrites:
`make check-sync` finds them when following (see the procedure below).

**A repository that is forked writes the entry in the same commit as the change**, together with the docs the change
affects. The commit hook stops changes to rules, scripts and kernel trees that come without an entry; changes under
`api/`, `internal/biz|data|service/` and `go.mod` it does not see, so judge those yourself. A repository nobody forks
writes no entries of its own: its ledger holds only its "following upstream" entries.

New entries go at the top. Each says three things: **what changed, why, how to apply it**.

## Two kinds of entries, and their anchors

- **This repository's own:** the anchor is "date · number of the day", such as `## 2026-08-14 · 1`.
- **Following upstream:** the same anchor; the first line under the heading names the upstream anchor, such as
  `> Follows <upstream repository> a1b2c3d`. The steps are rewritten for this repository's shape, not copied from
  upstream.

Numbers increase within a day from bottom to top (newest on top) and do not continue across days.

**Own anchors are not commit hashes; upstream anchors are.** The reasons for both are in
`docs/adr/layout-0006-ledger-anchor-values.md`. Upstream is distributed by forking, without tags, so the upstream
anchor is always the upstream repository's short commit hash.

**The sync point is the upstream anchor of the newest "following upstream" entry.** It is not recorded anywhere else:
two records always drift. A fork writes its first following entry when it is created, naming the upstream commit it
forked from, and empties the ledger it inherited: those entries are upstream's history, already contained in the fork
(README, "Fork into your own repo"). So there is always a sync point.

## Following upstream as a derived repository's developer

```
in your clone of the upstream repository, run
   git diff <upstream anchor of your newest following entry>..HEAD -- UPGRADING.md
   the added entries = the to-do list
        │
        ▼
   decide entry by entry → follow: run the entry's steps, oldest first
                    (kernel overwrites as usual: overwrite the trees + `make rename` with the fork's parameters)
                    (generated artifacts are never copied or merged: follow their sources, regenerate with
                     `make all`, see `docs/adr/layout-0003-generated-artifacts-committed.md`)
        │
        ▼
   UPSTREAM_DIR=<parent directory of the upstream clone> make check-sync
   for each mismatch: overwrite the whole shared file, or the whole shared segment, until it passes
                    (changes to shared files alone are not in the ledger; this step catches them, see "Shared files")
        │
        ▼
   add a "following upstream" entry at the top of your ledger: the upstream anchor is the upstream HEAD's short hash
   right now (git log -1 --format=%h), the steps rewritten for your repository's shape; the ledger is up to date
```

**Run the entries oldest first** (bottom to top), and put everything into one commit: their changes, the overwritten
shared files and kernel trees, and the new "following upstream" entry.

**Declining an entry.** An entry about an assembly seam, the examples or business code may be declined: name it in
your following entry as `Not followed: <upstream anchor>: <reason>`. The next sync starts after the new sync point and
does not offer it again; to take it up later, read it in the upstream ledger. An entry about the kernel, a shared file
or the Keel version cannot be declined: `make check-sync` keeps reporting the first two, and the upstream code is
written against that version.

**Overwriting the kernel.** Overwrite each tree registered with `norm` in `scripts/cross-repo-sync.list`: a directory
entry whole (delete what upstream deleted), a directory's `!` subtree and a per-file entry file by file, keeping the
files your repository added there. Then rerun `make rename` with the parameters your first following entry records.

**Between syncs, keep the upstream clone at your sync point** (`git checkout <sync point>` in the clone): `make check`
runs `make check-sync` against it, and a clone ahead of your sync point fails unrelated work. Move it forward only
when you follow.

**After running the entries and before writing the "following upstream" entry, run `make check-sync` and fix it until
it passes**: changes that touch only shared files or shared segments are not in the ledger, and this comparison is the
only way downstream finds them. `UPSTREAM_DIR` points at the parent directory of the upstream clone, and the clone's
directory name must equal the source repository field of each line in `scripts/cross-repo-sync.list`.

Following needs a local clone of your upstream repository, for both the `git diff` and `make check-sync` above.
**Read it, never pull it into your repository**: after a fork renames its module, `git pull upstream` conflicts on
every file whose imports changed (see `docs/adr/layout-0001-template-upgrade-strategy.md`), while reading its log
merges nothing and leaves your working tree alone.

A problem in your upstream repository is fixed upstream and flows back through this procedure; never patch it
privately in the derived repository.

## The framework version

Keel is referenced by version in `go.mod`, not through the fork chain, so there is no "following Keel". Its version
follows your upstream repository, and **a derived repository does not upgrade it on its own**: upstream's code and
guides are written against that one version, and a separate upgrade no longer matches them or later ledger entries.
**Every change of the Keel version is a ledger entry**, a patch release included: it names the core version and the
pseudo-version of each optional module upstream uses. A derived repository following it sets the same in `go.mod`
(an optional module upstream does not use: fetch it at the core tag's commit, `keel:docs/quickstart.md`), runs
`go mod tidy` and `make check-mod`, and applies Keel's own `UPGRADING.md` entries between the two versions to its
code.

## Shared files

The template chain has files that belong to no single repository but are carried verbatim along the fork edges:
framework scripts, shared ADRs, this protocol, and the convention docs (guides in `docs/guides/`, practices in
`docs/practices/`). Which files are shared is listed in `scripts/cross-repo-sync.list`, the first field of each line
being the source repository; ADRs carry it even more directly in their file name prefix, see
`docs/adr/layout-0004-adr-naming-and-carry-protocol.md`.

These files are never localized outside their source repository. A local change downstream is overwritten by the
next carry from the source, so it is wasted; change the source repository and let it flow down to you. Why their text
is written independent of any repository is argued in `docs/adr/layout-0004-adr-naming-and-carry-protocol.md`; why the
convention docs are shared verbatim too and contain no demo code, in
`docs/adr/layout-0002-verbatim-shared-docs-and-segmented-entry-files.md`.

**Entry files (`AGENTS.md`, `GLOSSARY.md`) are shared by segment.** An entry file holds both upstream's rules and this
repository's own, so it is not shared as a whole; upstream's part is enclosed in a pair of whole-line HTML comments:
the begin marker starts with `<!-- shared segment begin <source repository directory>:`, the end marker is
`<!-- shared segment end <source repository directory> -->`. The keel-layout segment comes first, and each further
upstream on the template chain adds a segment after it; a segment is treated like a shared file and stays verbatim.
Such files carry `seg` at the end of their line in the list, and `make check-sync` compares only the segments found
in the upstream file.

**A derived repository's own rules go outside the segments, or into separate files.** Outside the segments an entry
file holds this repository's rules, shapes and routing table rows; longer material goes into a separate file (no
shared marker in its header, not in the sync list), registered in the routing table outside the segments of
`AGENTS.md`. Never change a shared file or segment to write about your own repository.

An upstream change to a shared file or segment needs no push downstream and no ledger entry. Syncing happens when
downstream follows: whichever shared file `make check-sync` reports is overwritten whole, whichever shared segment it
reports is overwritten whole (begin marker to end marker, both lines included), with no line-by-line comparison. A
mismatch reported by `scripts/check-cross-repo-sync.sh` (`make check-sync`) is therefore not a fault, only a reminder
that upstream moved and you have not followed yet.

A new shared file needs both: the shared marker in its header (the `Shared file: carried verbatim` comment naming
`scripts/check-cross-repo-sync.sh`, within the first 10 lines), and a line in
`scripts/cross-repo-sync.list`. A new shared segment likewise: begin and end markers, registered with `seg`. In a
template repository that is forked, `make check` checks that the two match: registered without a marker, or marked
without registration, both fail.
