#!/usr/bin/env sh
set -eu

if [ "$#" -lt 2 ]; then
  echo "usage: $0 <har-dir> <out-dir> [extra deconstruct-eval args...]" >&2
  exit 2
fi

HAR_DIR=$1
OUT_DIR=$2
shift 2

mkdir -p "$OUT_DIR"
make build >/dev/null

failed=0
for har in "$HAR_DIR"/*.har; do
  [ -e "$har" ] || continue
  name=$(basename "$har" .har)
  if ! ./bin/deconstruct-eval -har "$har" -name "$name" -out "$OUT_DIR/$name" "$@" >"$OUT_DIR/$name.report.json"; then
    failed=1
  fi
done

exit "$failed"
