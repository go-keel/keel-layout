# Doc link check: lenient by default where distributed, STRICT for template self-checks

<!-- Shared file: carried verbatim; never localized outside its source repository.
     Checked by scripts/check-cross-repo-sync.sh; see docs/upgrading-protocol.md. -->

`scripts/check-doc-links.sh` is distributed verbatim to derived repositories, but the paths it checks include ones
that **differ per repository**. As soon as a derived repository deletes the demo business domain or renames a
directory under `cmd/`, `make lint` turns red out of the box, and every failure is the derived repository
**normally changing its own business**, not upstream shipping something wrong.

A red light must mean "upstream shipped something wrong". Once it also means "you did your normal work", the first
reaction is to turn the lint off, and real upstream dead links get through with it, which is exactly why the check
exists (AGENTS.md tells the AI to "read the guide first"; with a wrong path it follows the wrong thing, and doubts it
less than a person would).

Decision: **two tiers**. The default is lenient and checks only the three places whose link targets necessarily exist
in every derived repository: the kernel tree (read-only in derived repositories and overwritten whole; never hit in
a repository without a kernel, harmlessly), `scripts/` (framework scripts) and `docs/` (guides are shared verbatim,
`layout-0002-verbatim-shared-docs-and-segmented-entry-files.md`). The upstream template repository checks itself with `make lint-strict` (`STRICT=1`), checking
everything.

The criterion is not a choice; it follows from existing decisions: the kernel is read-only with zero diff, framework
scripts are distributed with the template, guides are shared verbatim; none of the three depends on
what business shape a derived repository keeps. So the three **do not change with a derived repository's business
shape** and need no maintenance; the objection at the top of the script against allowlists ("must be maintained with
the repository structure") does not apply to them.

Three rejected paths:

1. **List what not to check (a denylist)**: an open set that misses cases at once: directory names
   under `cmd/` change per repository; `error_reason.proto` looks like a framework file but is an assembly seam,
   since the framework's baseline reasons and the reasons a derived repository adds share one enum, and a derived
   repository certainly adds to it. The list would grow with every derived repository's shape, while the script must
   stay identical across the template chain.
2. **Rewrite the demo-domain paths in the docs as placeholders**: cures only the current batch. The `cmd/` renames
   still fail, and every derived repository has to do another round of content rewriting, of the same nature as
   "overwriting the whole directory is not free" in `layout-0001-template-upgrade-strategy.md`: one-off manual work
   spread to every derived repository and redone each time.
3. **Let derived repositories configure their own exclusions**: the script forks per repository on the spot. Zero
   diff is what lets a derived repository keep following upstream long term; breaking it for a checker costs far more
   than it gains.

Cost: derived repositories do not check links into assembly seams and their own business areas (`error_reason.proto`,
`di.go` under `cmd/` and the like). Accepted: such a link breaking in a derived repository is mostly the result of its
own rename, not an upstream mistake; and what upstream really gets wrong is caught by the template repository's
STRICT tier before distribution.

The matching enforcement points are `.githooks/pre-commit` (once installed, runs on every commit) and `make check`
(one command aggregating all self-checks). In the template, `.githooks/pre-commit` and `make check` run the STRICT
tier; a derived repository switches both to `lint` after forking (README), and keeps STRICT only if it is itself
forked. **For the STRICT tier to actually run, it must sit in an action that gets executed,
not only in a written rule.**
