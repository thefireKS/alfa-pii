#!/bin/sh
# Build a source distribution ZIP for pii-service.
#
# The archive contains only the explicitly listed source files, module files,
# configuration examples (without secrets), the Dockerfile, compose file,
# Makefile and this packaging script. It excludes .git, .agents, skills-lock.json,
# AGENTS.md, keys, .env with secrets, caches, dependencies, built binaries,
# reports, dumps and previous archives.
#
# Safety: every included path is checked to be a regular file (no symlinks) and
# to resolve inside the project root, so the packager never reads files outside
# the project. A SHA256 manifest of the included files and the git revision/state
# used for the build are written into the archive.
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$ROOT"

OUT_DIR="${OUT_DIR:-dist}"
ARCHIVE_NAME="${ARCHIVE_NAME:-pii-service-src.zip}"
ARCHIVE="$OUT_DIR/$ARCHIVE_NAME"
MANIFEST="$OUT_DIR/SHA256SUMS.txt"
REVISION_FILE="$OUT_DIR/REVISION.txt"

# Explicit list of files to include. Paths are relative to the project root.
# Keep this list in sync with the source tree; the packager fails if a listed
# file is missing.
FILES="
go.mod
go.sum
Dockerfile
docker-compose.yml
compose.server.yml
init-deploy-env.py
smoke-deploy.py
store-fill-test.py
replay-boundary-test.py
Makefile
.env.example
.dockerignore
.gitignore
.gitattributes
.editorconfig
README.md
cmd/pii-service/main.go
cmd/demo/main.go
cmd/demo-remote/main.go
cmd/eval/main.go
cmd/loadtest/main.go
config/config.example.env
config/consumers.example.json
config/consumers.local.json
internal/app/app.go
internal/app/app_test.go
internal/app/consumer_test.go
internal/config/config.go
internal/config/config_test.go
internal/config/consumers.go
internal/config/consumers_test.go
internal/demo/demo.go
internal/demo/demo_test.go
internal/eval/dataset.go
internal/eval/eval.go
internal/eval/eval_test.go
internal/eval/fuzz_test.go
internal/eval/generator.go
internal/eval/types.go
internal/httpapi/consumer_test.go
internal/httpapi/httpapi.go
internal/httpapi/httpapi_test.go
internal/httpapi/observability_test.go
internal/loadgen/client.go
internal/loadgen/loadgen_test.go
internal/loadgen/observe.go
internal/loadgen/pacer.go
internal/loadgen/report.go
internal/loadgen/runner.go
internal/loadgen/scenario.go
internal/loadgen/scheduler.go
internal/loadgen/stats.go
internal/loadgen/textgen.go
internal/masker/masker.go
internal/masker/masker_test.go
internal/masker/bench_test.go
internal/metrics/metrics.go
internal/metrics/metrics_test.go
internal/recognizer/address.go
internal/recognizer/birthdate.go
internal/recognizer/birthplace.go
internal/recognizer/card.go
internal/recognizer/cardholder.go
internal/recognizer/casefold_test.go
internal/recognizer/citizenship.go
internal/recognizer/context_recognizers_test.go
internal/recognizer/cvv.go
internal/recognizer/department.go
internal/recognizer/driverlicense.go
internal/recognizer/email.go
internal/recognizer/fullname.go
internal/recognizer/inn.go
internal/recognizer/labeled.go
internal/recognizer/passport.go
internal/recognizer/passportauthority.go
internal/recognizer/passportissuedate.go
internal/recognizer/phone.go
internal/recognizer/pin.go
internal/recognizer/recognizer.go
internal/recognizer/recognizer_test.go
internal/recognizer/recognizers_test.go
internal/recognizer/registry.go
internal/recognizer/registry_test.go
internal/recognizer/regression_test.go
internal/recognizer/regression2_test.go
internal/recognizer/resolve_test.go
internal/recognizer/scan.go
internal/store/observer_test.go
internal/store/bench_test.go
internal/store/store.go
internal/store/store_test.go
tests/eval-results.txt
scripts/make-dist.sh
"

# Reject any path that is a symlink or that resolves outside the project root.
check_safe() {
    f="$1"
    if [ -L "$f" ]; then
        echo "error: unexpected symlink in archive list: $f" >&2
        exit 1
    fi
    if [ ! -f "$f" ]; then
        echo "error: listed file missing: $f" >&2
        exit 1
    fi
    # Resolve the real path and require it to stay under the project root.
    real=$(CDPATH= cd -- "$(dirname -- "$f")" && pwd -P)
    case "$real" in
        "$ROOT"/*|"$ROOT") ;;
        *)
            echo "error: file resolves outside project root: $f" >&2
            exit 1
            ;;
    esac
}

mkdir -p "$OUT_DIR"
rm -f "$ARCHIVE" "$MANIFEST" "$REVISION_FILE"

# Record the git revision and working-tree state used for this build.
if git rev-parse --git-dir >/dev/null 2>&1; then
    REV=$(git rev-parse HEAD 2>/dev/null || echo "unknown")
    DIRTY=$(git status --porcelain 2>/dev/null | wc -l | tr -d ' ')
    if [ "$DIRTY" -gt 0 ]; then
        REV="$REV (dirty: $DIRTY uncommitted change(s))"
    fi
else
    REV="unknown (not a git checkout)"
fi
printf 'revision: %s\n' "$REV" > "$REVISION_FILE"

# Build the archive from the explicit list. Each entry is validated first.
# The manifest is generated from the same files that go into the archive.
: > "$MANIFEST"
for f in $FILES; do
    check_safe "$f"
    sha256sum "$f" >> "$MANIFEST"
done

# Add the revision file to the manifest. The manifest itself is not
# self-referenced (its own checksum cannot be verified against itself); its
# integrity is guaranteed by the ZIP archive's per-entry CRC.
sha256sum "$REVISION_FILE" >> "$MANIFEST"

# Create the ZIP. Use zip if available; otherwise fall back to a portable
# Python-based packer that also refuses symlinks.
if command -v zip >/dev/null 2>&1; then
    (cd "$ROOT" && zip -q -X "$ARCHIVE" $FILES "$MANIFEST" "$REVISION_FILE")
else
    python3 - "$ARCHIVE" "$MANIFEST" "$REVISION_FILE" $FILES <<'PY'
import os, sys, zipfile
archive, manifest, revision = sys.argv[1], sys.argv[2], sys.argv[3]
files = sys.argv[4:]
root = os.getcwd()
with zipfile.ZipFile(archive, "w", zipfile.ZIP_DEFLATED) as z:
    for f in files + [manifest, revision]:
        p = os.path.join(root, f)
        if os.path.islink(p):
            sys.exit(f"error: unexpected symlink in archive list: {f}")
        if not os.path.isfile(p):
            sys.exit(f"error: listed file missing: {f}")
        if not os.path.realpath(p).startswith(os.path.realpath(root) + os.sep):
            sys.exit(f"error: file resolves outside project root: {f}")
        z.write(p, f)
PY
fi

echo "archive: $ARCHIVE"
echo "manifest: $MANIFEST"
echo "revision: $REV"
echo "files: $(wc -l < "$MANIFEST")"