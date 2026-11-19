# Template upgrade strategy: derived repositories follow by hand, the template ships no upgrade tool

<!-- Shared file: carried verbatim; never localized outside its source repository.
     Checked by scripts/check-cross-repo-sync.sh; see docs/upgrading-protocol.md. -->

After forking this template, a derived repository changes the module name as part of onboarding (`make rename`,
see `README.md`), and from then on diverges from the template for good: `git pull upstream`
conflicts on every file whose imports changed.

Decision: **no upgrade mechanism**. The template writes the motivation, approach and impact of every change into the
root `UPGRADING.md` (the derived repository's copy is its sync ledger; usage at the top of that file), and each
derived repository decides whether and how to follow. Problems found in a derived repository are fixed in the
template repository and reach the derived repository through its `UPGRADING.md` entry.

**The rename runs once.** Upstream's kernel files import the template's module path, so they cannot be copied in as
they are. They are taken with `make sync-upstream`, which writes them already renamed to the derived repository's
module path: the same normalization `make check-sync` uses to compare them. The rename itself
(`make rename`) runs only when the repository is created; rerunning it over the whole repository would rewrite the
derived repository's own files by text. "Read-only" in the docs means **the derived repository does not change it**.

**The same holds for schemas.** A template ships only the complete schema of each database (`docs/guides/domain-model.md`,
"Schema"); a derived repository whose database already exists runs the `ALTER` given in the matching `UPGRADING.md`
entry: the template repository has no existing deployment and cannot produce incremental scripts that truly fit a
derived repository.

Two rejected paths:

1. **Derived repositories keep the module name, so they can `git pull` directly**: not possible. Module paths must
   be unique: several derived repositories existing at once would collide, and one `go get`-ing another and
   importing its `api/` proto stubs would not work at all.
2. **An upgrade tool that applies ledger entries and generates migrations**: with one maintainer and a limited
   number of derived repositories, maintaining the tool costs more than it saves. `make sync-upstream` is not one: it
   only writes the files `make check-sync` already compares, with the names it already normalizes; entries are still
   followed by hand.

Cost: changes reach downstream only as text, so it must be written as a formal change record. The carrier is the
**root `UPGRADING.md`** (distributed with the template; the derived repository's copy is its sync ledger, usage at
the top of that file). The `BREAKING CHANGE` section of commit messages is still written, but only as an aid for
git archaeology: after forking, a derived repository cannot pull upstream, so relying on commit messages would rely on
a step derived repositories may never take. (Reading upstream's git log when following is something else: it locates
which ledger entries are new, it does not use commit messages as the channel.)

The companion on the framework side is Keel's own `UPGRADING.md`, read on every upgrade of the Keel version.
