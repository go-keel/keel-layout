#!/bin/sh
# Shared file: carried verbatim; never localized outside its source repository.
# Checked by scripts/check-cross-repo-sync.sh; see docs/upgrading-protocol.md.
#
# Dead doc link check: scans every .md file in the repository and checks that the repository paths it references exist.
#
# Two tiers, see "Distribution boundary" below: lenient by default (only internal/core, scripts and docs), STRICT=1
# checks everything and is the template's self-check.
#
# It checks only that paths exist, never semantics (permission codes, config keys, error reasons). Docs are narrative:
# they weigh trade-offs, give counterexamples, write "X was tried and rejected", and a checker cannot tell a statement of
# fact from an example; false positives quickly get it turned off, and the checker itself is code that would rot with
# the docs, only less visibly. Semantics are covered by the AGENTS.md rule: before changing a mechanism, a permission
# code or a config key, run `grep -rn "<keyword>" --include="*.md" .`.
#
# Framework links: a doc points into the Keel framework's docs as `keel:docs/<page>.md` or
# `keel:docs/<page>.md#<anchor>` (in a Markdown link or in inline code). They resolve against the keel module
# this repository's go.mod pins (`GOWORK=off go list -m`), not a local checkout: the page must exist there, and the
# anchor must be the GitHub-style slug of one of its headings (lowercase; characters other than letters, digits,
# spaces, `-` and `_` dropped; spaces become `-`). Checked in both tiers, because the pinned version is exactly what
# a derived repository reads.
set -eu

cd "$(dirname "$0")/.."

# A candidate path contains `/` and its first segment exists at the repository root. The rule adapts to the
# repository layout, needs no allowlist, and also excludes the three most common false hits: module paths
# (example.com/...), MIME types (text/event-stream) and URL paths (/v1/sys/...). The cost: a bare file name without `/`
# (a mistyped `NOTES.md`) is not caught; guessing "path or noun" with a regex costs far more in false positives.
roots=$(ls -A)

report=$(mktemp)
trap 'rm -f "$report"' EXIT

# UPGRADING.md is exempt as a whole: its job is to say "it was X, now it is Y", and the removed old path necessarily does
# not exist, an inherent false positive this check cannot tell apart. The cost: a wrong new path in it is not caught
# either; the new paths of a migration are the ones in the other docs, which are checked.
#
# .claude/ is skipped entirely: it is the local AI tools' working directory (skill links, whole-repository worktrees
# that subagents create under .claude/worktrees/), and none of it is committed. A worktree's UPGRADING.md is not at the
# root, so the exemption above would not cover it and every line would be reported; the other .md files there are
# copies from other branches and say nothing about this repository.
files=$(find . -name '*.md' \
	-not -path '*/node_modules/*' \
	-not -path '*/dist/*' \
	-not -path './bin/*' \
	-not -path './UPGRADING.md' \
	-not -path './.claude/*' \
	-not -path './.git/*' -print)
printf '%s\n' "$files" |
	while IFS= read -r f; do
		{
			# Only two reference forms are scanned: inline Markdown links (anchor cut off) and inline code.
			# Titled `[x](path "title")` and reference-style `[x][ref]` / `[ref]: path` links are not scanned: these docs
			# have none, and no regex is kept for an imagined case. A dead link written that way passes silently; add it
			# then.
			grep -noE '\]\([^)#]+' "$f" | sed 's/](//' || true
			grep -noE '`[^`]+`' "$f" | tr -d '`' || true
		} | while IFS= read -r hit; do
			line=${hit%%:*}
			tok=${hit#*:}
			# In the Go call form (kconfig.NewDynamic[…]("configs/app.yaml")) the quotes after `](` are captured with the
			# token; strip them and check it as a normal path
			tok=${tok#\"}
			tok=${tok%\"}
			case "$tok" in
			# Anything with placeholders, globs, spaces or command fragments is not a checkable path
			*' '* | *'<'* | *'>'* | *'*'* | *'{'* | *'|'* | *'('* | *'$'* | *'...'*) continue ;;
			# Go package.ExportedIdentifier (data.Data, timefmt.RFC3339) is not a path
			*.[A-Z]*) continue ;;
			*/*) ;;
			*) continue ;;
			esac
			# The trailing slash of a directory reference (docs/adr/) is stripped after the "contains /" test,
			# otherwise a one-segment directory (contrib/) would become a bare name and skip the existence check
			tok=${tok%/}

			head=${tok%%/*}
			match=0
			for e in $roots; do
				if [ "$e" = "$head" ]; then
					match=1
					break
				fi
			done
			[ "$match" = 1 ] || continue

			# Distribution boundary: strict inside, lenient outside. By default only targets that exist in every derived
			# repository are checked: the kernel (`internal/core/`, read-only downstream), the framework scripts and
			# `docs/` (guides and practices carried verbatim). Two kinds are deliberately not checked: the own area
			# (`internal/biz|data|service/<domain>.go`, `web/src/routes/_app/<domain>/`) differs per repository, and the
			# extension area (`cmd/<app>/di.go`, `api/<app>/v1/error_reason.proto`, files whose baseline the template ships
			# and downstream extends) is renamed per repository. Checking hard-coded paths there would make every derived
			# repository red out of the box, and red must mean "upstream shipped a mistake", not "you changed your
			# business", or the first reaction is to turn the lint off. The upstream template checks itself with STRICT=1.
			#
			# This is an allowlist while the candidate rule above is not: these three are backed by the kernel definition
			# and by verbatim-carried docs, do not change with the business, and need no upkeep.
			if [ -z "${STRICT:-}" ]; then
				case "$tok" in
				internal/core | internal/core/* | scripts/* | docs/*) ;;
				*) continue ;;
				esac
			fi

			# Both bases count: Markdown links are relative to their file, paths in prose and inline code are by habit
			# relative to the repository root. Either match means the path exists.
			[ -e "$tok" ] && continue
			[ -e "$(dirname "$f")/$tok" ] && continue
			# Ignored files (configs/*.dev.yaml and the like) and directories (build output) are not meant to be in the
			# repository. Directory rules end with a slash (`dist/`), and git cannot tell whether a missing path is a
			# directory, so asking without the slash says "not ignored": the pre-commit hook runs on a snapshot of the
			# index that never has build output, and every normal reference would be reported. Ask again with a slash.
			git check-ignore -q "$tok" 2>/dev/null && continue
			git check-ignore -q "$tok/" 2>/dev/null && continue

			printf '%s:%s: %s\n' "${f#./}" "$line" "$tok" >>"$report"
		done
	done

# keel: links, as described at the top. The module is looked up only when a doc has such a link.
keel_links=$(printf '%s\n' "$files" | while IFS= read -r f; do
	grep -noE 'keel:docs/[A-Za-z0-9_./-]+\.md(#[A-Za-z0-9_-]+)?' "$f" | sed "s#^#${f#./}:#" || true
done)
if [ -n "$keel_links" ]; then
	GOWORK=off go mod download github.com/go-keel/keel
	# KEEL_MODULE_DIR / KEEL_MODULE_VERSION let a caller that runs this script outside the repository (the pre-commit
	# hook's snapshot of the index) pass the module resolved in the real checkout.
	keel_dir=${KEEL_MODULE_DIR:-$(GOWORK=off go list -m -f '{{.Dir}}' github.com/go-keel/keel)}
	keel_ver=${KEEL_MODULE_VERSION:-$(GOWORK=off go list -m -f '{{.Version}}' github.com/go-keel/keel)}
	if [ -z "$keel_dir" ] || [ ! -d "$keel_dir" ]; then
		echo "keel: links need the keel module, but 'GOWORK=off go list -m -f {{.Dir}} github.com/go-keel/keel' gave '${keel_dir}'."
		echo "  Check that go.mod requires github.com/go-keel/keel and that any replace directive points to an existing directory."
		exit 1
	fi
	# GitHub heading anchors of a Markdown file, one per line; fenced code is skipped, and repeated headings get
	# -1, -2, … as on GitHub. Bytes >= 0x80 are kept as letters (non-ASCII letters keep their case).
	anchors() { # <file>
		awk '/^(```|~~~)/ { f = !f; next } !f && /^#+ / { sub(/^#+ +/, ""); sub(/[ #]*$/, ""); print }' "$1" |
			tr 'A-Z' 'a-z' | LC_ALL=C tr -cd 'a-z0-9 _\n\200-\377-' | tr ' ' '-' |
			awk '{ n = c[$0]++; print n ? $0 "-" n : $0 }'
	}
	printf '%s\n' "$keel_links" | while IFS= read -r hit; do
		where=${hit%:keel:*}
		target=${hit#"$where":keel:}
		page=${target%%#*}
		if [ ! -f "$keel_dir/$page" ]; then
			printf '%s: keel:%s not found in keel %s\n' "$where" "$target" "$keel_ver" >>"$report"
		elif [ "$page" != "$target" ] && ! anchors "$keel_dir/$page" | grep -qxF "${target#*#}"; then
			printf '%s: keel:%s not found in keel %s\n' "$where" "$target" "$keel_ver" >>"$report"
		fi
	done
fi

if [ -s "$report" ]; then
	echo "Dead doc links (repository paths that do not exist):"
	cat "$report"
	exit 1
fi

echo "doc links ok"

# ADR naming protocol (docs/adr/layout-0004-adr-naming-and-carry-protocol.md): the prefix names the source
# repository. Decisions made here use this repository's prefix; ADRs carried from upstream keep their source prefix
# (like every shared file: same name, same bytes). The default NNNN-slug.md naming is reserved for derived
# repositories: their tooling generates it out of the box, and the two namings share no counter, so they never collide
# after a fork.
#
# Decided by go.mod's module name, not by STRICT: a derived repository always replaces the module name, so even if it
# runs lint-strict by mistake this rule does not fire and its legitimate unprefixed ADRs stay green. This rule matters
# more than dead links: two ADRs with the same number are two different files to git, live side by side silently after a
# sync, and surface only when someone writes "see ADR-0002", when it is already ambiguous.
module=$(sed -n 's/^module //p' go.mod 2>/dev/null || true)
case "$module" in
github.com/go-keel/keel-layout) adr_own=layout; adr_up='' ;;
github.com/go-keel/keel-admin)  adr_own=admin;  adr_up='layout' ;;
*)                        adr_own='';     adr_up='' ;;   # derived repository: the default naming is its own; not checked
esac
if [ -n "$adr_own" ] && [ -d docs/adr ]; then
	pat="^$adr_own-"
	for p in $adr_up; do pat="$pat|^$p-"; done
	bad=$(ls docs/adr 2>/dev/null | grep -Ev "$pat" || true)
	if [ -n "$bad" ]; then
		echo "Invalid ADR prefix (this repository's own ADRs use $adr_own-NNNN-slug.md; ADRs carried from upstream keep their source prefix${adr_up:+: $adr_up}):"
		echo "$bad" | sed 's/^/  docs\/adr\//'
		exit 1
	fi
fi

# A bare `$var` in a message, directly followed by a multibyte character: bash 3.2 (macOS /bin/sh) in a UTF-8
# locale reads the character's first byte as part of the variable name, looks up a variable that does not exist and
# exits under `set -eu`. Write `${var}`.
#
# A machine must guard this: it only shows in a UTF-8 locale, environments with an empty LANG (CI containers, agent
# runners) stay green, and reviewers do not see it. It has broken once already: a branch of the cross-repository check
# crashed in a real terminal, the opposite of what it said it did.
#
# LC_ALL=C makes grep look at bytes; the negated set `[^<tab> -~]` is "not printable ASCII and not tab", where the first
# byte of a multibyte character falls, and neither `${var}` nor `$var(` matches it.
#
# The tab is spliced in from a variable rather than typed in the bracket: a raw tab is invisible in diffs and editors,
# and it is unclear whether it is a tab or a `\t` escape (someone already misreported it once).
# Not [:print:] / [:space:]: BSD grep in the C locale counts 0x80-0xFF as [:print:], which would disable this gate
# silently, and a silent pass is much harder to find than a false alarm.
tab=$(printf '\t')
bad=$(LC_ALL=C grep -nE '\$[A-Za-z_][A-Za-z0-9_]*[^'"${tab}"' -~]' scripts/*.sh .githooks/* 2>/dev/null || true)
if [ -n "$bad" ]; then
	echo "Shell variable followed by a multibyte character (bash 3.2 + UTF-8 fails with unbound variable; write \${var}):"
	printf '%s\n' "$bad" | sed 's/^/  /'
	exit 1
fi
