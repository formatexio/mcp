#!/usr/bin/env bash
#
# sync-from-api.sh - regenerate the server sources of this repository from the
# private FormaTeX API repository, where the MCP server is developed.
#
# Usage:
#   scripts/sync-from-api.sh /path/to/formatex-api
#
# Mapping:
#   <api>/cmd/mcp/*.go       -> internal/app/*.go
#                               "package main" becomes "package app" and
#                               "func main()" becomes "func Main()"
#   <api>/internal/mcp/*.go  -> internal/mcp/*.go
#                               the API module's import path becomes this module's
#   <api>/smithery.yaml      -> smithery.yaml (unchanged; a test in internal/mcp
#                               keeps it in step with the code)
#
# internal/app and internal/mcp are replaced as a whole, so a file removed in the
# API repository disappears here too, and running the script twice gives the same
# tree. Sources are staged and checked first: a failed run leaves the tree as it
# was. The API repository is only read, and git is not touched in this one.
#
# After a sync, review the diff, then run: go vet ./... && go test ./... -race

set -euo pipefail

die() {
	echo "sync-from-api: $*" >&2
	exit 1
}

[ $# -eq 1 ] || die "usage: scripts/sync-from-api.sh /path/to/formatex-api"
[ -d "$1" ] || die "$1 is not a directory"

api=$(cd "$1" && pwd)
repo=$(cd "$(dirname "$0")/.." && pwd)
module=github.com/formatexio/mcp

[ -f "$api/cmd/mcp/main.go" ] || die "$api is not the FormaTeX API repository: cmd/mcp/main.go not found"
[ -f "$api/internal/mcp/server.go" ] || die "$api is not the FormaTeX API repository: internal/mcp/server.go not found"
[ -f "$api/smithery.yaml" ] || die "$api/smithery.yaml not found"
[ -f "$api/go.mod" ] || die "$api/go.mod not found"
[ "$api" != "$repo" ] || die "the source and the destination are the same directory"
command -v gofmt >/dev/null || die "gofmt not found in PATH"

# The import path the API repository uses for its internal/mcp package.
api_module=$(awk '$1 == "module" { print $2; exit }' "$api/go.mod")
[ -n "$api_module" ] || die "cannot read the module path from $api/go.mod"

stage=$(mktemp -d "${TMPDIR:-/tmp}/formatex-mcp-sync.XXXXXX")
trap 'rm -rf "$stage"' EXIT
mkdir "$stage/app" "$stage/mcp"

# transform <source> <destination>: rewrite the package clause, the entry point
# name and the module path of one Go file. awk keeps this portable between the
# BSD and GNU toolchains, and index() avoids treating the dots of the module
# path as a regular expression.
transform() {
	awk -v from="$api_module" -v to="$module" '
		/^package main$/       { print "package app"; next }
		/^func main\(\) \{$/   { print "func Main() {"; next }
		{
			while ((i = index($0, from)) > 0) {
				$0 = substr($0, 1, i - 1) to substr($0, i + length(from))
			}
			print
		}
	' "$1" >"$2"
}

for f in "$api"/cmd/mcp/*.go; do
	transform "$f" "$stage/app/$(basename "$f")"
done
for f in "$api"/internal/mcp/*.go; do
	transform "$f" "$stage/mcp/$(basename "$f")"
done

# Fail loudly if the server grew a dependency on another package of the private
# module: it cannot be built from this repository.
if grep -rn "$api_module" "$stage"; then
	die "the server imports packages of $api_module that are not part of this repository"
fi
grep -q '^func Main() {$' "$stage/app/main.go" || die "cmd/mcp/main.go has no func main() to export as Main()"

gofmt -w "$stage/app" "$stage/mcp"

mkdir -p "$repo/internal/app" "$repo/internal/mcp"
rm -f "$repo"/internal/app/*.go "$repo"/internal/mcp/*.go
cp "$stage"/app/*.go "$repo/internal/app/"
cp "$stage"/mcp/*.go "$repo/internal/mcp/"
cp "$api/smithery.yaml" "$repo/smithery.yaml"

echo "synced from $api"
if rev=$(git -C "$api" rev-parse --short HEAD 2>/dev/null); then
	branch=$(git -C "$api" rev-parse --abbrev-ref HEAD 2>/dev/null || echo "?")
	echo "  api revision: $rev ($branch)"
fi
echo "  internal/app: $(ls "$repo"/internal/app/*.go | wc -l | tr -d ' ') files"
echo "  internal/mcp: $(ls "$repo"/internal/mcp/*.go | wc -l | tr -d ' ') files"
echo "  smithery.yaml"
