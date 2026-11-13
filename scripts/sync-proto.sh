#!/usr/bin/env bash
# Shared file: carried verbatim; never localized outside its source repository.
# Checked by scripts/check-cross-repo-sync.sh; see docs/upgrading-protocol.md.
# Copies the .proto files this service imports but does not own into third_party/, from the versions in go.mod:
# Keel's proto/keel/ and its third_party/ (protovalidate, googleapis) from github.com/go-keel/keel, and
# errors/errors.proto verbatim from github.com/go-kratos/kratos/v3. `make proto` runs it before generating; `make check`
# compares the result with what is committed. No version is written here: upgrading Keel or Kratos in go.mod and
# running `make proto` is the whole upgrade. Do not edit third_party/ by hand.
set -euo pipefail
cd "$(dirname "$0")/.."
export GOWORK=off

KEEL=github.com/go-keel/keel
KRATOS=github.com/go-kratos/kratos/v3

go mod download "$KEEL" "$KRATOS"
moddir() {
	local dir
	dir=$(go list -m -f '{{.Dir}}' "$1")
	# go list -m prints an empty Dir, without failing, for a module that is not in the module cache.
	[ -n "$dir" ] && [ -d "$dir" ] || { echo "sync-proto: no source directory for $1 (is it in go.mod?)" >&2; exit 1; }
	echo "$dir"
}
keel=$(moddir "$KEEL")
kratos=$(moddir "$KRATOS")

rm -rf third_party
mkdir -p third_party/keel third_party/kratos/errors
cp -R "$keel/proto/keel" third_party/keel/
cp -R "$keel/third_party/protovalidate" "$keel/third_party/googleapis" third_party/
cp "$kratos/errors/errors.proto" third_party/kratos/errors/
# The module cache is read-only and the copies keep its modes: make them writable before deleting from them.
chmod -R u+w third_party
find third_party -type f ! -name '*.proto' -delete
