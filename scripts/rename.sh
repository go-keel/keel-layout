#!/bin/sh
# Rename after forking: replace this repository's module path with the target module name everywhere.
#
#   make rename MODULE=example.com/demo [API_DOMAIN=demo]
#
# It also renames the proto package segment under api/: the directory `api/keel_layout/`, `package
# api.keel_layout.v1`, and the imports and doc paths that reference them. The segment goes into full gRPC method
# names and OpenAPI type names and is the service's public name: keep the template name and, once clients call it,
# it can no longer change. It defaults to the last segment of the module path (lowercased, `-` `.` `~` `+` turned
# into `_`; when the last segment is a Go `/vN` version suffix, the one before it); set API_DOMAIN when no valid
# name can be derived or you want another one.
#
# Why a script instead of a list of sed commands in the docs:
#
#   - `find . -name "*.go" -exec sed` also hits `*.pb.go`. **Never sed `.pb.go`** (see the AGENTS.md rule on not
#     hand-editing generated artifacts): rawDesc is a length-prefixed string literal; changing a module path inside
#     it raises no error and only blows up at runtime with `slice bounds out of range`. Generated artifacts are
#     outside the replacement scope; `make all` regenerates and overwrites them.
#   - A hand-written sed misses `option go_package` in `.proto`, so the next `make proto` after the rename brings
#     the old module name back.
#   - `github.com/go-keel/keel` is the framework dependency and must stay as is. This script replaces only this
#     repository's own module path (longer than the framework path, with a right boundary), so it is never hit.
#
# Idempotent: places already carrying the target name are left alone; a second run changes nothing.
set -eu

cd "$(dirname "$0")/.."

# The template repository's own module path. **Derived repos must not change this line.** It is the second source
# string besides go.mod: files copied over wholesale from upstream carry this name, while go.mod has long had the
# derived repo's own name, so relying on go.mod alone would miss them.
TEMPLATE_MODULE=github.com/go-keel/keel-layout

new=${MODULE:-}
if [ -z "$new" ]; then
	echo "usage: make rename MODULE=example.com/demo" >&2
	exit 1
fi
# This script rewrites imports across the repository; finding an invalid path only at go list is too late, as by
# then go.mod and dozens of files are already mangled and only a git rollback helps. So validate first.
#
# Let Go judge validity instead of hand-written case branches: `.` / `..` in segments, trailing dots, double
# slashes, illegal characters, case escaping... the rules live in golang.org/x/mod, and copying them always misses
# one. Try the edit on a temporary go.mod: on failure go mod edit writes nothing, and its error is more specific
# than a home-made one (it names the segment and the character).
if command -v go >/dev/null 2>&1; then
	probe=$(mktemp -d)
	printf 'module m\n' >"${probe}/go.mod"
	err=$(go mod edit -module="$new" "${probe}/go.mod" 2>&1) || {
		rm -rf "${probe}"
		echo "rename: ${err#go: invalid -module: }" >&2
		exit 1
	}
	rm -rf "${probe}"
else
	echo "rename: go not found, skipping module path validation" >&2
fi

# This is a convention of this repository, not a Go rule (Go allows single-segment module names): a derived repo
# needs a real, fetchable path.
case "$new" in
*/*) ;;
*)
	echo "rename: the module name needs a path (such as example.com/demo), got '${new}'" >&2
	exit 1
	;;
esac
# Restrict the character set to Go module paths. This is not pedantry: `$new` goes straight into the sed
# replacement, where `&` means "the whole match", `\` a back-reference and `|` clashes with the delimiter; all three
# silently write wrong content instead of failing. Go module paths forbid them anyway, and rejecting them at the
# entry is more reliable than escaping later.
case "$new" in
*[!A-Za-z0-9._~/+-]*)
	echo "rename: the module name contains illegal characters (Go module paths allow only letters, digits and - . _ ~ / +)" >&2
	exit 1
	;;
esac

current=$(sed -n 's/^module //p' go.mod | head -1)
if [ -z "$current" ]; then
	echo "rename: go.mod has no module line" >&2
	exit 1
fi

# Module path → package segment under api/. Rules in the header comment.
domain_of() {
	seg=${1##*/}
	if printf '%s' "$seg" | grep -qE '^v[0-9]+$'; then
		rest=${1%/*}
		seg=${rest##*/}
	fi
	printf '%s' "$seg" | sed 'y/ABCDEFGHIJKLMNOPQRSTUVWXYZ.~+-/abcdefghijklmnopqrstuvwxyz____/'
}

new_domain=${API_DOMAIN:-$(domain_of "$new")}
# It is both a segment of the proto package name and a directory in Go import paths; package names are all
# lowercase by buf convention. Also rejected before touching any file: it goes into the sed replacement too.
case "$new_domain" in
'' | [!a-z]* | *[!a-z0-9_]*)
	if [ -n "${API_DOMAIN:-}" ]; then
		echo "rename: API_DOMAIN '${new_domain}' is invalid (must start with a lowercase letter and contain only lowercase letters, digits and _)" >&2
	else
		echo "rename: the proto package segment '${new_domain}' derived from ${new} is invalid; set it with API_DOMAIN=xxx (must start with a lowercase letter and contain only lowercase letters, digits and _)" >&2
	fi
	exit 1
	;;
esac

# Old package segments to rename: the one derived from the current module plus the template's own (files copied
# over from upstream carry it, same reason as TEMPLATE_MODULE). Only one of these two directories may exist under
# api/, and the target must not exist yet: merging two proto packages is not a decision for a rename script, so fail
# before touching files.
old_domains=$(printf '%s\n%s\n' "$(domain_of "$current")" "$(domain_of "$TEMPLATE_MODULE")" | sort -u)
move_from=
for d in $old_domains; do
	[ "$d" != "$new_domain" ] && [ -d "api/$d" ] || continue
	if [ -n "$move_from" ]; then
		echo "rename: both api/${move_from} and api/${d} exist; cannot tell which one to rename to api/${new_domain}" >&2
		exit 1
	fi
	move_from=$d
done
if [ -n "$move_from" ] && [ -e "api/$new_domain" ]; then
	echo "rename: api/${new_domain} already exists; cannot rename api/${move_from} to it" >&2
	exit 1
fi

tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT

# Escape sed BRE metacharacters (the `.` in module paths). The delimiter is `|`, which module paths never contain.
escape() { printf '%s' "$1" | sed 's/[.[\*^$]/\\&/g'; }

# Replacement scope: non-generated `.go`, `.proto`, `.md`, plus the module line of go.mod.
# Excludes `.git` / `node_modules` / `dist` / `bin` and every `*.pb.go`.
# Shell scripts under `scripts/` are **deliberately out of scope**: `check-doc-links.sh` is a shared script kept
# verbatim across repositories, and the module name in it is a constant it decides by (a renamed derived repo falls
# into the "no ADR prefix check" tier); replacing it would break both the verbatim check and that deliberate
# behaviour.
list_files() {
	find . \
		\( -name .git -o -name node_modules -o -name dist -o -name bin \) -prune -o \
		-type f \( \( -name '*.go' ! -name '*.pb.go' \) -o -name '*.proto' -o -name '*.md' \) -print
}

# Right boundary: a module path followed by [A-Za-z0-9_-] is another, longer module name and is left alone; only
# one followed by `/` (a subpackage), punctuation or end of line is replaced.
replace_all() {
	old=$1
	if [ "$old" = "$new" ]; then
		return 0
	fi
	old_re=$(escape "$old")
	expr="s|${old_re}\\([^A-Za-z0-9_-]\\)|${new}\\1|g;s|${old_re}\$|${new}|g"

	sed "s|^module ${old_re}\$|module ${new}|" go.mod >"$tmp"
	if ! cmp -s "$tmp" go.mod; then
		cat "$tmp" >go.mod
		echo "  go.mod"
	fi

	list_files | while IFS= read -r f; do
		grep -q "$old_re" "$f" || continue
		sed "$expr" "$f" >"$tmp"
		if ! cmp -s "$tmp" "$f"; then
			cat "$tmp" >"$f"
			echo "  ${f#./}"
		fi
	done
}

# The package segment has only lowercase letters, digits and _, so it needs no escaping in a regex. The left
# boundary protects longer names (such as openapi/…); the pattern's own `/` `.` give the right boundary.
replace_domain() {
	od=$1
	if [ "$od" = "$new_domain" ]; then
		return 0
	fi
	expr="s|^api/${od}/|api/${new_domain}/|;s|\\([^A-Za-z0-9_-]\\)api/${od}/|\\1api/${new_domain}/|g"
	expr="${expr};s|^api\\.${od}\\.|api.${new_domain}.|;s|\\([^A-Za-z0-9_]\\)api\\.${od}\\.|\\1api.${new_domain}.|g"
	expr="${expr};s|import \"${od}/|import \"${new_domain}/|g"

	list_files | while IFS= read -r f; do
		grep -q "$od" "$f" || continue
		sed "$expr" "$f" >"$tmp"
		if ! cmp -s "$tmp" "$f"; then
			cat "$tmp" >"$f"
			echo "  ${f#./}"
		fi
	done
}

changed=$(
	# Move the directory first, so the changed files listed below carry the renamed path
	if [ -n "$move_from" ]; then
		mv "api/$move_from" "api/$new_domain"
		echo "  api/${move_from}/ → api/${new_domain}/"
	fi
	replace_all "$current"
	if [ "$TEMPLATE_MODULE" != "$current" ]; then
		replace_all "$TEMPLATE_MODULE"
	fi
	for d in $old_domains; do
		replace_domain "$d"
	done
)
# A file may be changed by both the module-path and package-segment passes; list it once
changed=$(printf '%s\n' "$changed" | awk 'NF && !seen[$0]++')

if [ ! -d "api/$new_domain" ]; then
	# Reached when an earlier rename set a different API_DOMAIN and this one does not: the old directory cannot be derived, so it is left alone.
	echo "rename: note: api/${new_domain} does not exist; if an earlier rename set the package segment with API_DOMAIN, pass it again" >&2
fi

if [ -z "$changed" ]; then
	echo "rename: module name ${new} and proto package segment ${new_domain} already in place, nothing changed"
	exit 0
fi

echo "rename: → ${new} (api/ package segment ${new_domain})"
printf '%s\n' "$changed"

# Replacing names of different length breaks the alignment and order of import groups, so reformat afterwards;
# otherwise the derived repo's first commit carries entry files that are not gofmt-clean. gofmt ships with Go, which
# this repository requires anyway; if it is really missing, print a hint instead of failing the rename.
if command -v gofmt >/dev/null 2>&1; then
	# No IFS=: each line of $changed has a two-space indent, which default word splitting strips
	printf '%s\n' "$changed" | while read -r f; do
		case "$f" in *.go) gofmt -w "$f" ;; esac
	done
else
	echo "rename: gofmt not found; run go fmt ./... yourself afterwards"
fi

cat <<EOF

Next:

  make init-hooks    # install the commit hooks (git hooks do not travel with a clone)
  make all           # regenerate: \`*.pb.go\` and openapi.yaml still carry the old module name; this script does not touch them

A few things the script cannot do (switch the lint tier, change the template identity in config, delete the
examples): follow "Fork into your own repo" in the README. The checklist lives only there and is not repeated here.
EOF
