# ADR admission, naming and carry-over: the prefix names the source repository, shared copies are identical

<!-- Shared file: carried verbatim; never localized outside its source repository.
     Checked by scripts/check-cross-repo-sync.sh; see docs/upgrading-protocol.md. -->

Every repository has its own `docs/adr/`, and decisions are carried down along the fork edges: a repository may have
an upstream, downstreams, or both, in no fixed shape. Without a common protocol three forms drift in: one decision
named differently in two repositories, ADRs added independently in two repositories taking the same number, and a
downstream copy changed in place without anyone noticing. All three show only when someone cites that ADR, and by then
the reference no longer resolves; so the protocol has to exist before the first shared ADR.

This ADR is itself a live example of the protocol: its source is keel-layout, so it takes the `layout-` prefix and
the next number in the layout sequence, and keeps its name when carried downstream.

## Three admission criteria

An ADR is written only when all three hold:

1. **Hard to reverse**: changing one's mind has a real cost;
2. **Confusing without context**: a future reader will ask "why was this decided";
3. **A real trade-off**: there was an opposing option, and this one was chosen for reasons that can be stated.

Rules that fail the criteria do not take an ADR: what must be followed on every change goes into each repository's
AGENTS.md (always in context), what is followed step by step goes into each repository's guides and operating docs
(loaded on demand). Conversely, the argument for a decision that passes the criteria does not belong in AGENTS.md: it
is only needed when someone wants to overturn the decision, yet it would be paid for on every turn.

## Naming: the prefix names the source repository, not the holder

A decision is named after the repository where it was made, in every repository. keel-layout's are
`layout-NNNN-slug.md`, and a further template uses its own short repository name as prefix; the number
increases in **the source repository's own sequence**, the sequences are independent, so an ADR added downstream
never collides with one upstream adds later.

**The default naming (`NNNN-slug.md`) is reserved for derived repositories.** A derived repository's own decisions
use the default naming and never collide with the prefixed files carried in by the fork: the two namings share no
counter. If a derived repository is itself forked later (becoming a new upstream), it prefixes its ADRs with its own
repository name, and the rule applies recursively.

References always use the full file name (such as `layout-0005-doc-link-check-distribution-boundary.md`), never
"ADR-0002": a number is unique only within its source repository, so a bare number across repositories points at
nothing.

## Carry-over: the name stays, the text is repository-independent

An upstream decision carried downstream **keeps its file name**: the name itself is what ties the two copies together.
Renamed, the link could only be kept by a hand-written "source is …", a manual copy with no sync mechanism, which is
bound to drift.

The text of a shared ADR is written **independent of any repository**: placeholder descriptions instead of concrete
paths, and repository-specific practice only as "see each repository's AGENTS.md". Guides and practices likewise only
name paths every repository has, or use placeholders (see
`layout-0002-verbatim-shared-docs-and-segmented-entry-files.md`).

Repository-independent text lets the copies be **identical**, so they can go into the sync list and be guarded by
`make check-sync`. This is a hard pairing, not an option: **claiming consistency that nobody guards is worse than not
claiming it**: a reader who believes it does not compare, so drift lives longer. A shared text therefore has only two
legal states: in the list and checked, or explicitly allowed to be localized per repository.

When a decision really cannot be written independent of the repository, the repository-specific part stays in that
repository's own AGENTS.md and the shared ADR keeps only the general part, rather than writing concrete paths into the
shared text and relying on people to remember to change them per repository.

## To point at one repository, write role plus repository name

Shared text is distributed with the fork, and relative references such as "this repository" or "upstream" read as
whichever repository the reader is in. So they are only used for rules true in every repository; to point at one
particular repository, write the role plus its name ("the keel-layout maintainer …", "a derived repository's developer
…"), or the same sentence means opposite things read upstream and read in a derived repository.
