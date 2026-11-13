VERSION?=$(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# buf plugins are pinned in buf.gen.yaml as go run ...@version; nothing to install globally.
# buf itself is pinned too: @latest would generate different output on different machines and days, and the
# generated files are committed, so that drift shows up as "make proto gives me a huge diff".
.PHONY: init
init:
	go install github.com/bufbuild/buf/cmd/buf@v1.75.0

.PHONY: proto
# scripts/sync-proto.sh first copies the imported .proto files into third_party/ from the versions in go.mod; then
# generate from the root buf.yaml workspace, api/ only. Delete api/'s generated files first: buf generate only writes
# outputs for .proto files that still exist, so a deleted .proto would leave its *.pb.go behind, still compiling.
# Not buf generate --clean: the outputs are api/ itself and the repo root, so it would delete sources too.
proto:
	scripts/sync-proto.sh
	find api -name '*.pb.go' -delete
	buf generate api --template api/buf.gen.yaml -o api

# app.proto goes through buf too, not a global protoc: protoc and protoc-gen-go would have to be installed on each
# machine, and whether and which version is installed would change the output, which is committed. app.proto
# imports only one WKT (duration), which buf bundles, so no --proto_path.
# The template is inline rather than a file: internal/conf has a single proto, not worth another config file.
# Keep the protoc-gen-go version the same as in api/buf.gen.yaml.
CONF_GEN_TEMPLATE = {"version":"v2","plugins":[{"local":["go","run","google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.11"],"out":".","opt":["paths=source_relative"]}]}

.PHONY: config
config:
	cd internal/conf && buf generate --template '$(CONF_GEN_TEMPLATE)' .

# Generated files (defined in GLOSSARY.md) are committed but written only by the generate commands. Regenerate
# and compare checksums before and after: a difference means someone edited a generated file by hand, or changed
# its source without regenerating. It compares the working tree before and after, not git: right after a proto
# change, before committing, generated files always differ from git, and that is exactly when check runs most.
# On a mismatch the generated files are already fresh: review the diff, commit them, and the next run passes.
.PHONY: check-generated
check-generated:
	@list() { { find api internal/conf -name '*.pb.go'; find third_party -type f; echo openapi.yaml; } | LC_ALL=C sort | xargs cksum; }; \
	before=$$(mktemp); after=$$(mktemp); trap 'rm -f "$$before" "$$after"' EXIT; \
	list >"$$before"; $(MAKE) -s proto config >/dev/null || exit 1; list >"$$after"; \
	diff "$$before" "$$after" >/dev/null || { \
		echo "Generated files do not match their sources (edited by hand, or the source changed without regenerating). Regenerated; the files below changed; review the diff and commit them:"; \
		diff "$$before" "$$after" | sed -n 's/^[<>] [0-9]* [0-9]* /  /p' | sort -u; exit 1; }

.PHONY: generate
generate:
	go mod tidy
	go generate ./...

# The binaries go straight to production; every entry point shares these build flags:
#   CGO_ENABLED=0  fully static, no dependency on the target's C library; runs in minimal images (scratch /
#                  distroless);
#   -trimpath      strips the build machine's absolute source paths: stack traces leak no local directories, and
#                  the same source builds the same on any machine;
#   -s -w          strips the symbol table and DWARF debug info, 20-30% smaller. The cost is that debuggers such
#                  as dlv cannot attach to production processes; panic stack traces are unaffected (they use
#                  pclntab, not the symbol table).
GO_BUILD = CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.Version=$(VERSION)"

.PHONY: build-api
build-api:
	mkdir -p bin/ && $(GO_BUILD) -o ./bin/api ./cmd/api

.PHONY: build-svc
build-svc:
	mkdir -p bin/ && $(GO_BUILD) -o ./bin/svc ./cmd/svc

.PHONY: build-worker
build-worker:
	mkdir -p bin/ && $(GO_BUILD) -o ./bin/worker ./cmd/worker

.PHONY: build
build: build-api build-svc build-worker

# `bin/` is in .gitignore, so git will not clean it, and `git clean -fdx` would also take `configs/*.dev.yaml` and
# private keys with it; without this target there is no safe way to clear build output.
# Not a prerequisite of build: a full rebuild should not be the cost of every compile.
.PHONY: clean
clean:
	rm -rf bin/

# Whether repository paths referenced in the docs exist. AGENTS.md tells the AI to read the matching guide before
# acting; with a wrong path it works from the wrong thing, and questions it less than a human would.
#
# Two tiers, see the distribution boundary in `scripts/check-doc-links.sh`: `lint` checks only the kernel /
# scripts / docs, the tier shipped to derived repositories; `lint-strict` checks everything, **for this
# repository's self-check**. This repository is a template: run lint-strict after changing docs; derived
# repositories run lint.
# lint-strict also checks the registration of shared content, both ways for each form, by marker, not file name:
#   whole shared files: a file under docs/, scripts/ or deploy/ with the shared-file marker in its first 10 lines
#   must be registered in the last block of the sync list, and a registered file must carry the marker;
#   shared segments: any .md with a whole-line `<!-- shared segment begin keel-layout: ` marker must be
#   registered as seg, and a file registered as seg must have that marker, with begin and end markers paired.
# keel-layout does not compare them itself; the registrations are for derived repositories, and each one
# missing is one check fewer for them.
.PHONY: lint
lint:
	@scripts/check-doc-links.sh

.PHONY: lint-strict
lint-strict:
	@STRICT=1 scripts/check-doc-links.sh
	@list=scripts/cross-repo-sync.list; \
	whole=$$(awk '/^#?keel-layout[ \t]/ { s = 0; for (i = 3; i <= NF; i++) if ($$i == "seg") s = 1; if (!s) print $$2 }' $$list); \
	segs=$$(awk '/^#?keel-layout[ \t]/ { for (i = 3; i <= NF; i++) if ($$i == "seg") print $$2 }' $$list); \
	start='^<!-- shared segment begin keel-layout: '; end='^<!-- shared segment end keel-layout -->$$'; \
	miss=0; \
	for f in $$(find docs scripts deploy -type f \( -name '*.md' -o -name '*.sh' -o -name '*.conf' -o -name '*.example' \)); do \
		head -n 10 "$$f" | grep -qF 'check-cross-repo-sync.sh' || continue; \
		printf '%s\n' "$$whole" | grep -qxF "$$f" || { echo "  not registered: $$f"; miss=1; }; \
	done; \
	for f in $$whole; do \
		[ -f "$$f" ] || { echo "  registered but missing: $$f"; miss=1; continue; }; \
		head -n 10 "$$f" | grep -qF 'check-cross-repo-sync.sh' || { echo "  registered but no shared-file marker in the header: $$f"; miss=1; }; \
	done; \
	for f in $$(grep -rlE "$$start" --include='*.md' --exclude-dir=.git --exclude-dir=.claude --exclude-dir=node_modules . | sed 's#^\./##'); do \
		printf '%s\n' "$$segs" | grep -qxF "$$f" || { echo "  has shared segments but is not registered as seg: $$f"; miss=1; }; \
	done; \
	for f in $$segs; do \
		[ -f "$$f" ] || { echo "  registered as seg but missing: $$f"; miss=1; continue; }; \
		n=$$(grep -cE "$$start" "$$f" || true); \
		[ "$$n" -gt 0 ] || { echo "  registered as seg but has no shared segment begin marker: $$f"; miss=1; continue; }; \
		[ "$$n" -eq "$$(grep -cE "$$end" "$$f" || true)" ] || { echo "  shared segment begin and end markers are not paired: $$f"; miss=1; }; \
	done; \
	[ $$miss -eq 0 ] || { echo "Markers and registrations of shared files and segments must match both ways: every file with the shared-file marker or a keel-layout shared segment must be registered in the last block of scripts/cross-repo-sync.list (the latter as seg; for a new registration, the UPGRADING.md entry tells derived repositories to add the line), and every registered file must carry the matching marker"; exit 1; }

# Rename after forking: replaces this repository's module references in go.mod, non-generated .go, .proto
# go_package and .md, together with the proto package segment under api/ (directory and package), in one go;
# never touches *.pb.go (sed would corrupt rawDesc). The package segment defaults to the last element of the
# module path; API_DOMAIN overrides it. Idempotent; the reasons are in the script header.
.PHONY: rename
rename:
	@test -n "$(MODULE)" || (echo "Usage: make rename MODULE=example.com/demo [API_DOMAIN=demo]" && exit 1)
	@MODULE="$(MODULE)" API_DOMAIN="$(API_DOMAIN)" scripts/rename.sh

# Cross-repository verbatim check: how a derived repository finds upstream changes to shared files and kernel trees
# (skipped, with a note, when no upstream clone is checked out locally). The list is scripts/cross-repo-sync.list; the reasons are at the top of
# scripts/check-cross-repo-sync.sh.
.PHONY: check-sync
check-sync:
	@scripts/check-cross-repo-sync.sh

# git hooks do not travel with a clone; run this once after forking. .githooks/pre-commit stops a commit that
# changes the propagation surface (AGENTS.md / GLOSSARY.md / docs/ / scripts/ / Makefile) without touching
# UPGRADING.md, and checks for dead links; the criterion and exemption are at the top of that file. Automatic
# merges do not trigger pre-commit; pre-merge-commit checks dead links for them.
.PHONY: init-hooks
init-hooks:
	git config core.hooksPath .githooks

# Derived repositories have no go.work; modules resolve through go.mod. go.work hides the versions in go.mod, so a
# local build passing does not mean a derived repository builds; GOWORK=off reproduces their resolution.
# Always run it after changing the github.com/go-keel/keel version; the version follows upstream and derived repositories do
# not upgrade it on their own (see docs/upgrading-protocol.md).
.PHONY: check-mod
check-mod:
	GOWORK=off go build ./...

# Real-database tests (build tag integration): the same data-layer cases as make check, on a fresh MySQL database
# built from migrations/*/0_schema.sql. Needs TEST_MYSQL_DSN, e.g. 'root:keel@tcp(127.0.0.1:53306)/?parseTime=true'; a missing
# variable fails. Not part of make check.
.PHONY: integration
integration:
	GOWORK=off go test -race -tags integration -count=1 -p 1 ./...

# golangci-lint runs with go run at a pinned version: nothing to install, not in go.mod. Default linters.
GOLANGCI = github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0

# The agent's self-check: these repositories have no CI, so consistency is up to the agent; every command needed
# to confirm the repository is clean, in one. Run it after changing code or docs; it stops at the first failing
# step. make echoes each command before running it, so on failure the last echoed line is the failing step.
# lint-strict is this repository's (where distribution starts) self-check tier; a derived repository changes it
# to lint after forking, for the reasons in the README section on forking.
# Tests, like check-mod, turn go.work off: derived repositories test against the versions in go.mod, and only
# this way shows whether a test passes only because of unreleased behavior in the local Keel source.
# check-generated goes first: only once the generated files come from the current sources do the build and tests
# that follow test the real thing. It needs buf installed (make init).
.PHONY: check
check:
	$(MAKE) check-generated
	@# Only files git knows (tracked + new, not ignored): recursing by directory would scan the full repository
	@# copies of subagents under .claude/worktrees/, which git treats as nested repositories and does not list.
	@bad=$$(gofmt -l $$(for f in $$(git ls-files -co --exclude-standard '*.go'); do [ -f "$$f" ] && echo "$$f"; done)); \
		test -z "$$bad" || { echo "$$bad"; echo "gofmt: the files above are not formatted"; exit 1; }
	go build ./...
	go vet ./...
	GOWORK=off go vet -tags integration ./...
	GOWORK=off go run $(GOLANGCI) run ./...
	GOWORK=off go test -race ./... -count=1
	buf lint
	$(MAKE) check-mod
	$(MAKE) lint-strict
	@# This template's own rule that its text is English; a derived repository deletes this line unless it wants the same.
	@scripts/check-english.sh
	$(MAKE) check-sync

.PHONY: all
all: proto config generate

# goi18n, like the buf plugins, is pinned as go run ...@version: nothing to install, no drift; its version follows
# go-i18n in go.mod.
GOI18N := go run github.com/nicksnyder/go-i18n/v2/goi18n@v2.6.1

.PHONY: i18n-extract
i18n-extract:
	$(GOI18N) extract -sourceLanguage en -outdir internal/i18n -format toml

# Run twice: the first run exports new or changed source entries to translate.<language>.toml; after translating,
# run again to merge the translate files back into active.<language>.toml, then delete the translate files by hand.
.PHONY: i18n-merge
i18n-merge:
	cd internal/i18n && $(GOI18N) merge -sourceLanguage en -outdir . active.*.toml $$(ls translate.*.toml 2>/dev/null)

