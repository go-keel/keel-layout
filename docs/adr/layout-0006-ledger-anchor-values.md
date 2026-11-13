# Ledger anchors: own anchors are date and number, upstream anchors are short hashes

<!-- Shared file: carried verbatim; never localized outside its source repository.
     Checked by scripts/check-cross-repo-sync.sh; see docs/upgrading-protocol.md. -->

Every entry of `UPGRADING.md` needs a unique coordinate, used for two things: people in this repository point at
entries with it, and downstream records how far it has followed. The two uses need different coordinates, so the
values are chosen differently; mixing them into one scheme is the easiest mistake to make with this ledger.

## Own anchors: date · number of the day

An entry is written in a commit, and a commit hash is that commit's **output**: it does not exist yet while the entry
is written, and can only be filled in by a second commit afterwards. Every entry would take two commits, and in this
direction the hash buys nothing: an own anchor only needs to be unique and sortable for people to point at.

So own anchors are "date · number of the day", increasing from bottom to top within a day and not continuing across
days.

## Upstream anchors: the upstream repository's short commit hash

Upstream is always distributed by forking, without tags, so the upstream anchor is the upstream repository's short
commit hash.

Downstream computes its to-do list from it: in the upstream repository, `git diff <coordinate recorded last>..HEAD --
UPGRADING.md`; the added entries are the list.

## Two rejected paths

1. **Upstream anchors also as "date · number"**: downstream could only match entries by title by hand, and **revisions
   upstream makes to old entries go unnoticed**: the count of entries does not change, so counting does not show them,
   while a diff does.
2. **Upstream records each entry's hash in its own ledger for downstream to cite**: back to self-reference; each entry
   still needs a second commit, the cost only moves from downstream to upstream.

## Precondition and cost

Stable hashes depend on pushed history never being rewritten. That holds anyway,
because derived repositories fork and pull from the remote and a rewrite would split their history; the ledger adds
no new constraint.

The cost is that **at the moment of following**, a derived repository's developer needs a local clone of the upstream
repository to read its log. It is not needed otherwise (skipping the cross-repository consistency check on the derived
side is the normal case), and `git pull upstream` is never done: after the module rename it always conflicts. Reading
the log merges nothing and leaves the working tree alone, so that conflict does not arise.
