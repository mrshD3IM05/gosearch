#!/bin/sh
set -eu

if [ "$#" -gt 1 ]; then
  echo "Usage: ./dev.sh [sqlite-db-path]" >&2
  exit 2
fi

cd "$(dirname "$0")"

db_path=${1-}
if [ -n "$db_path" ] && [ ! -f "$db_path" ]; then
  echo "Database file not found: $db_path" >&2
  exit 2
fi

build_dir=$(mktemp -d "${TMPDIR:-/tmp}/gosearch-dev.XXXXXX")
api_pid=
web_pid=

cleanup() {
  trap - 0 INT TERM
  if [ -n "$api_pid" ]; then kill "$api_pid" 2>/dev/null || true; fi
  if [ -n "$web_pid" ]; then kill "$web_pid" 2>/dev/null || true; fi
  if [ -n "$api_pid" ]; then wait "$api_pid" 2>/dev/null || true; fi
  if [ -n "$web_pid" ]; then wait "$web_pid" 2>/dev/null || true; fi
  rm -f "$build_dir/gosearch$exe" "$build_dir/webtest$exe"
  rmdir "$build_dir"
}
trap cleanup 0
trap 'exit 130' INT
trap 'exit 143' TERM

exe=
if [ "$(go env GOOS)" = windows ]; then exe=.exe; fi
go build -o "$build_dir/gosearch$exe" ./cmd/gosearch
go build -o "$build_dir/webtest$exe" ./cmd/webtest

echo "Starting API on http://localhost:9200"
if [ -n "$db_path" ]; then
  "$build_dir/gosearch$exe" -db "$db_path" &
else
  "$build_dir/gosearch$exe" &
fi
api_pid=$!

echo "Starting UI on http://localhost:5000"
"$build_dir/webtest$exe" &
web_pid=$!

echo "Press Ctrl+C to stop both servers."
while kill -0 "$api_pid" 2>/dev/null && kill -0 "$web_pid" 2>/dev/null; do
  sleep 1
done

if ! kill -0 "$api_pid" 2>/dev/null; then
  wait "$api_pid"
else
  wait "$web_pid"
fi
