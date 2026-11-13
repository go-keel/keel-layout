# Convention docs are shared verbatim, entry files by segment, and conventions contain no demo code

<!-- Shared file: carried verbatim; never localized outside its source repository.
     Checked by scripts/check-cross-repo-sync.sh; see docs/upgrading-protocol.md. -->

Every repository on the template chain carries a set of conventions: entry files (`AGENTS.md`, `GLOSSARY.md`) and
guides (`docs/guides/`). When guides are localized per repository and entry files copied and then rewritten, one
rule ends up in several versions across repositories: upstream fixes it, downstream keeps the old copy, and no check
notices; and a derived repository's reason to rewrite is mostly just "the guide cites upstream's demo code, which this
repository does not have".

## Decision

1. **Convention docs are shared verbatim.** Guides and practices are shared files; derived repositories follow them
   and never rewrite them, guarded by `make check-sync`. Content specific to a derived repository goes into a
   separate file. Changes to shared files alone are not in the ledger: when following, downstream runs
   `make check-sync` and overwrites each file it reports, whole.
2. **Conventions contain only general content independent of any business.** They cite no repository's demo code,
   demo routes, demo script names or concrete config values: derived repositories usually delete the demo code, and a
   convention citing it is wrong there. Where the demo code is, the repository holding it says outside the segments
   of its entry files.
3. **Entry files are segmented within one file.** Upstream's rules are enclosed by a pair of whole-line HTML comments
   as a shared segment, identical to upstream; this repository's own rules go outside. Each further upstream on the
   chain adds a segment, in order. The list registers them with `seg`, and `make check-sync` compares segment by
   segment.

Whether a sentence goes inside or outside a segment: does it still hold in any derived repository (one with a single
entry point, one without a message queue, one that deleted the demo code)? If yes, inside; if not, outside, or rewrite
it in general terms. Relative references: `layout-0004-adr-naming-and-carry-protocol.md`, "To point at one
repository, write role plus repository name".

## Rejected paths

1. **Localize per repository and accept drift.** Fixes to conventions do not travel down, and
   "same origin, guarded by nobody" makes readers think the copies agree.
2. **Split into several files, with entry files referencing upstream's copy via `@path`.** Only Claude Code expands
   such references; other AI tools see a line with a path, and the conventions are never loaded.
3. **Copy upstream's entry files whole and put this repository's content in another file.** AI tools read only the
   entry file by default, so the derived repository's own rules would be missed.

## Cost

- The comparison script has one more mode (`seg`), and entry files a pair of markers to maintain.
- A repository without a message queue or asynchronous tasks still carries those two guides verbatim; they load on
  demand and cost nothing unread.
- Practices specific to a derived repository live in its own separate file, registered in the routing table, not in
  the guides.
