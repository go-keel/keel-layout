# Generated artifacts are committed and written only by generation commands

<!-- Shared file: carried verbatim; never localized outside its source repository.
     Checked by scripts/check-cross-repo-sync.sh; see docs/upgrading-protocol.md. -->

A file that a generation command can recompute from source files in the repository is a generated artifact: code and
API descriptions generated from proto, and the types generated from those in turn. Which ones a repository has is in
its `GLOSSARY.md`, "Generated artifacts".

## Decision

1. **Committed.**
2. **Written only by generation commands**: never edited by hand; when following upstream, never copied or merged:
   follow their source files and regenerate.
3. **Guarded by a machine**: `make check` regenerates and compares checksums before and after; a difference fails.

## Why committed

Go fetches only source from git when it references another repository; it does not run the referenced repository's
generation commands. Services `go get`-ing each other's api packages is the norm on this template chain (see
`docs/guides/grpc-client.md`), so without committed artifacts the referencing side does not compile. The rule is one
for the whole chain: a repository nobody references yet follows it too, so it need not change rules the day it is
referenced.

## Why not copied by hand

A derived repository has changed its module name and proto package name; upstream's generated artifacts carry
upstream's names and do not work when copied: the same source generates different content in the two repositories. The
source is what is followed.

## Why compare the working tree before and after, not against git

After changing proto and regenerating, before committing, is when `make check` runs most often, and then the generated
artifacts necessarily differ from git. Comparing checksums before and after answers one question only: are the current
artifacts computed from the current source.

## Rejected path

**Artifacts not committed, generated at build time**: clean diffs, and hand edits cannot happen. But the referencing
side fails to compile after `go get`; working around that means every service publishing proto through BSR and every
referencing side generating for itself, which changes how services reference each other.
