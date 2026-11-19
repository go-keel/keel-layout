#!/bin/sh
# Shared file: carried verbatim; never localized outside its source repository.
# Checked by scripts/check-cross-repo-sync.sh; see docs/upgrading-protocol.md.
#
# Cross-repository verbatim check: turns "these files must match upstream byte for byte" from a verbal
# promise into an executable test.
#
# Why it exists: the template chain has files that claim to be identical across repositories (framework
# scripts, shared ADRs, convention docs), but keeping them in sync relied on someone remembering to cp.
# They did drift: upstream changed, downstream was forgotten, both evolved on their own, and the claim of
# being identical stayed in the docs. A claim nobody guards is worse than no claim: readers trust it and
# never compare.
#
# The list is scripts/cross-repo-sync.list, one entry per line:
# `<upstream repo directory> <relative path> [norm|seg] [!<subpath>…]`.
# Upstream repositories are sibling directories (../<directory>) by default; UPSTREAM_DIR sets their parent.
#
# A path may be a file or a directory. A directory is compared as a whole tree, and the two file sets
# must match too (one missing is a forgotten carry, one extra is a local addition; both are drift).
# `!<subpath>` (full path) after a directory entry excludes a subtree on both sides, for directories
# where derived repositories may add files of their own (such as a CLI-generated component directory).
#
# `norm` means "compare after normalizing": first replace the upstream module path (the module line of
# each go.mod) with this repository's, run `.go` through gofmt, and when `.ts` / `.tsx` still differ,
# reformat with this repository's frontend Biome (the nearest ancestor of the entry with biome.json and
# installed dependencies); then compare byte for byte. It is for kernel trees carried whole: their only
# legitimate difference from upstream is the module path make rename replaces (a kernel's proto package
# keeps its name in every repository), and renaming reorders imports and changes line widths, so without
# reformatting every file would show red.
# Without this mode the kernel, the part most worth guarding, could not be listed at all.
#
# `seg` means "compare only the shared segments", for entry files such as AGENTS.md and GLOSSARY.md that
# hold both upstream rules and this repository's own. The upstream part is delimited by a pair of
# whole-line HTML comments:
#   begin: `<!-- shared segment begin <source repo directory>: …note… -->`
#   end:   `<!-- shared segment end <source repo directory> -->`
# A segment is the begin and end lines plus everything between. **Every** segment in the upstream file
# (whatever its name) must exist with the same name in this repository's file and match verbatim; a
# missing segment or a differing one is a mismatch. Extra segments here (this repository is their
# source, left for downstream) are not reported. An upstream file with no segment means the list is
# stale. Segments hold only repository-neutral content with no module path, so seg is never combined
# with norm; it applies to single files only, not directories.
#
# `WRITE=1` (make sync-upstream) also writes: every file and norm entry is brought in line with upstream, missing
# files are written, differing ones overwritten (norm entries already carrying this repository's names, the same
# normalization the comparison uses) and extra ones in a directory entry deleted, excluded subtrees untouched. This is
# how a derived repository takes upstream's kernel and shared files when it follows, so the module rename runs only
# once, when the repository is created, and never again over its own files. seg entries are only reported: a shared
# segment sits among this repository's own text, so it is replaced by hand.
#
# **Without a local clone of upstream the script skips and says so.** A derived repository keeps one,
# checked out at its sync point (docs/upgrading-protocol.md): this comparison is how it finds changes to
# shared files and kernel trees, which carry no ledger entry of their own.
set -eu

cd "$(dirname "$0")/.."
list=scripts/cross-repo-sync.list
[ -f "$list" ] || { echo "cross-repo sync: no list, skipped"; exit 0; }

up_root=${UPSTREAM_DIR:-..}
write=
[ "${WRITE:-}" = 1 ] && write=1
miss=0
bad=0
n=0

# Prints upstream's file as this repository should hold it: in norm mode the upstream module path replaced with this
# repository's, `.go` through gofmt and `.ts` / `.tsx` through Biome. The comparison and the writing both use it, so
# a written file always compares equal.
render_one() { # <src> <dst> <mode>
	case "$3:$1" in
	norm:*.go) sed "$norm_expr" "$1" | gofmt ;;
	norm:*.ts | norm:*.tsx) sed "$norm_expr" "$1" | biome_format "$2" ;;
	norm:*) sed "$norm_expr" "$1" ;;
	*) cat "$1" ;;
	esac
}

# Compares one pair of files verbatim, in norm mode against render_one. Most files match once the module path is
# replaced, so that is tried first: gofmt and Biome (one process start per file) run only when it still differs.
cmp_one() { # <src> <dst> <mode>
	[ "$3" = norm ] || { cmp -s "$1" "$2"; return; }
	sed "$norm_expr" "$1" | cmp -s - "$2" || render_one "$@" | cmp -s - "$2"
}

# Formats standard input with this repository's frontend Biome config; <path> only tells Biome the file type
# and which config applies.
biome_format() { # <path>
	(cd "$fe" && node_modules/.bin/biome format --stdin-file-path="${1#"$fe"/}")
}

# The frontend root nearest to the entry: an ancestor with biome.json and installed dependencies
# (node_modules/.bin/biome); prints nothing if there is none.
frontend_root() { # <path>
	d=$1
	while [ "$d" != . ] && [ "$d" != / ]; do
		if [ -f "$d/biome.json" ] && [ -x "$d/node_modules/.bin/biome" ]; then
			printf '%s' "$d"
			return 0
		fi
		d=$(dirname "$d")
	done
}

# Names of the shared segments in an upstream file (deduplicated). Names have only letters, digits, _ and -,
# so they need no escaping in a regex.
seg_names() { # <file>
	sed -n 's/^<!-- shared segment begin \([A-Za-z0-9_-]*\): .*/\1/p' "$1" | sort -u
}

# The sed expression that extracts one shared segment (begin and end lines plus everything between); printed
# as-is so a human can reproduce a difference.
seg_expr() { # <segment name>
	printf '/^<!-- shared segment begin %s: /,/^<!-- shared segment end %s -->$/p' "$1" "$1"
}

# An entry may be a file or a directory: find returns a single file itself, so both share one loop.
# The current entry's `!<subpath>` exclusions are filtered out here.
# (Paths are not IFS-protected: the compared scope is a source tree, without file names containing spaces.)
list_files() { # <root> <path>
	(cd "$1" && find "$2" -type f 2>/dev/null | sort) |
		awk -v ex="$excl" 'BEGIN { n = split(ex, p, " ") }
			{ for (i = 1; i <= n; i++) if (index($0, p[i] "/") == 1) next; print }'
}

while read -r repo path rest; do
	# blank and comment lines
	case "${repo:-#}" in '#'*) continue ;; esac
	[ -n "${path:-}" ] || continue
	n=$((n + 1))
	mode=
	excl=
	for w in ${rest:-}; do
		case "$w" in
		'!'*) excl="$excl ${w#!}" ;;
		*) mode=$w ;;
		esac
	done

	if [ ! -d "$up_root/$repo" ]; then
		miss=$((miss + 1))
		echo "  skipped ${path} (upstream ${repo} is not in ${up_root})"
		continue
	fi
	# The upstream repository is here but the file is not: upstream renamed or deleted it, so the entry is
	# stale. Counted as a mismatch rather than skipped: treating it as "upstream not local" would let a
	# rename slip through green.
	if [ ! -e "$up_root/$repo/$path" ]; then
		echo "  upstream has no ${path} (${repo} is local; the file was renamed or deleted, update the list)"
		bad=$((bad + 1))
		continue
	fi

	if [ "$mode" = seg ]; then
		if [ ! -f "$up_root/$repo/$path" ]; then
			echo "  ${path} is not a single file (seg compares shared segments within one file)"
			bad=$((bad + 1))
			continue
		fi
		if [ ! -f "$path" ]; then
			echo "  missing ${path} (the list says its shared segments are carried from ${repo}, but this repository has no such file)"
			bad=$((bad + 1))
			continue
		fi
		names=$(seg_names "$up_root/$repo/$path")
		if [ -z "$names" ]; then
			echo "  ${path} in upstream ${repo} has no shared segment (the list registers it as seg; the entry is stale)"
			bad=$((bad + 1))
			continue
		fi
		for s in $names; do
			e=$(seg_expr "$s")
			if ! grep -q "^<!-- shared segment begin ${s}: " "$path"; then
				echo "  missing segment in ${path}: no shared segment ${s} (upstream ${repo} has it)"
				echo "    sed -n '${e}' $up_root/$repo/$path"
				bad=$((bad + 1))
			elif [ "$(sed -n "$e" "$up_root/$repo/$path")" != "$(sed -n "$e" "$path")" ]; then
				echo "  mismatch in shared segment ${s} of ${path}  <- $repo"
				echo "    diff <(sed -n '${e}' $up_root/$repo/$path) <(sed -n '${e}' $path)"
				bad=$((bad + 1))
			fi
		done
		continue
	fi

	if [ "$mode" = norm ]; then
		up_mod=$(sed -n 's/^module //p' "$up_root/$repo/go.mod" 2>/dev/null | head -1)
		my_mod=$(sed -n 's/^module //p' go.mod 2>/dev/null | head -1)
		if [ -z "$up_mod" ] || [ -z "$my_mod" ]; then
			miss=$((miss + 1))
			echo "  skipped ${path} (norm needs the module line of both go.mod files; one is missing)"
			continue
		fi
		if ! command -v gofmt >/dev/null 2>&1; then
			miss=$((miss + 1))
			echo "  skipped ${path} (norm needs gofmt to reorder renamed imports; not found)"
			continue
		fi
		fe=
		if { list_files "$up_root/$repo" "$path"; list_files . "$path"; } | grep -qE '\.tsx?$'; then
			fe=$(frontend_root "$path")
			if [ -z "$fe" ]; then
				miss=$((miss + 1))
				echo "  skipped ${path} (norm needs Biome to rewrap renamed lines; no frontend directory with installed dependencies)"
				continue
			fi
		fi
		norm_expr="s#${up_mod}#${my_mod}#g"
	fi

	for rel in $(list_files "$up_root/$repo" "$path"); do
		if [ -n "$write" ] && { [ ! -f "$rel" ] || ! cmp_one "$up_root/$repo/$rel" "$rel" "$mode"; }; then
			mkdir -p "$(dirname "$rel")"
			if render_one "$up_root/$repo/$rel" "$rel" "$mode" >"$rel.sync-tmp"; then
				mv "$rel.sync-tmp" "$rel"
				if [ -x "$up_root/$repo/$rel" ]; then chmod +x "$rel"; fi
				echo "  wrote ${rel}  <- $repo"
			else
				rm -f "$rel.sync-tmp"
				echo "  could not write ${rel} (rendering the upstream file failed; see the error above)"
				bad=$((bad + 1))
			fi
		elif [ ! -f "$rel" ]; then
			echo "  missing ${rel} (the list says it is carried from ${repo}, but this repository does not have it)"
			bad=$((bad + 1))
		elif ! cmp_one "$up_root/$repo/$rel" "$rel" "$mode"; then
			echo "  mismatch $rel  <- $repo"
			case "$mode:$rel" in
			norm:*.go) echo "    sed '${norm_expr}' $up_root/$repo/$rel | gofmt | diff - $rel" ;;
			norm:*.ts | norm:*.tsx)
				echo "    sed '${norm_expr}' $up_root/$repo/$rel | (cd $fe && node_modules/.bin/biome format --stdin-file-path=${rel#"$fe"/}) | diff - $rel"
				;;
			norm:*) echo "    sed '${norm_expr}' $up_root/$repo/$rel | diff - $rel" ;;
			*) echo "    diff $up_root/$repo/$rel $rel" ;;
			esac
			bad=$((bad + 1))
		fi
	done
	# Reverse direction: an extra file in a directory carried whole is drift too (a no-op for single files)
	for rel in $(list_files . "$path"); do
		if [ ! -f "$up_root/$repo/$rel" ] && [ -n "$write" ]; then
			rm "$rel"
			echo "  deleted ${rel} (upstream ${repo} has no such file)"
		elif [ ! -f "$up_root/$repo/$rel" ]; then
			echo "  extra ${rel} (upstream ${repo} has no such file)"
			bad=$((bad + 1))
		fi
	done
done <"$list"

if [ "$bad" -gt 0 ]; then
	echo "cross-repo sync: $bad mismatch(es) with upstream ($n entries in the list)"
	echo "  These files claim to be identical. Either sync the upstream change here, or change upstream first"
	echo "  and then sync. Do not edit this repository's copy in place: the two would evolve separately."
	exit 1
fi

if [ "$n" -eq 0 ]; then
	# No entry in the list is active. Only keel-layout legitimately has no upstream; a derived repository
	# that forgot to enable entries after forking also lands here, and letting it pass would mean never
	# comparing, so the two are told apart by module name.
	if [ "$(sed -n 's/^module //p' go.mod 2>/dev/null)" != github.com/go-keel/keel-layout ]; then
		echo "cross-repo sync: no entry in the list is active; after forking, enable the commented-out entries at the end of $list"
		exit 1
	fi
	echo "cross-repo sync: this repository is where distribution starts; no upstream to compare (the entries are for derived repositories to enable)"
elif [ "$miss" -eq "$n" ]; then
	echo "cross-repo sync: no upstream repository is local, all skipped (normal for a derived repository)"
else
	echo "cross-repo sync ok ($((n - miss))/$n entries checked)"
fi
