#!/bin/bash

if [[ "$1" =~ ^(-\?|-h|--help)$ ]]; then
  SCRIPT_NAME="$(basename "$0")"

  cat <<EOF
Usage: $SCRIPT_NAME [COUNT] [URL]

Run concurrent HEAD requests to a target URL and report response times.

Arguments (positional, order-independent):
  COUNT  Number of concurrent requests. Default: 5
  URL    Target URL. Default: http://localhost:7080

Examples:
  $SCRIPT_NAME
  $SCRIPT_NAME 10
  $SCRIPT_NAME http://example.com
  $SCRIPT_NAME 8 http://example.com
  $SCRIPT_NAME http://example.com 8
EOF
  exit 0
fi

for arg in "${@:1:2}"; do
  if [[ "$arg" =~ ^[0-9]+$ ]]; then
    [ -n "${COUNT+x}" ] && echo "Warning: Multiple numeric arguments provided. Using $arg" >&2
    COUNT="$arg"
    continue
  fi
  [ -n "${URL+x}" ] && echo "Warning: Multiple URL arguments provided. Using $arg" >&2
  URL="$arg"
done

COUNT="${COUNT-5}"
URL="${URL-http://localhost:7080}"

LOCK="$(mktemp)"

echo "Running $COUNT concurrent requests to $URL:"

for i in $(seq "$COUNT"); do
  { 
    result=$({ time curl -X HEAD -s "$URL" >/dev/null; } 2>&1 | awk '/^real/ {print $2}')
    (
      flock -x 200
      echo "$result"
    ) 200>"$LOCK"
  } &
done

wait
rm "$LOCK"
