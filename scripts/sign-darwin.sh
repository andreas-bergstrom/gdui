#!/bin/sh
set -eu

ARTIFACT="$1"
TARGET="$2"
SNAPSHOT="$3"

case "$TARGET" in
  darwin_*) ;;
  *) exit 0 ;;
esac

if [ "$SNAPSHOT" = "true" ]; then
  exit 0
fi

if [ -z "${QUILL_SIGN_P12:-}" ]; then
  echo "quill - QUILL_SIGN_P12 unset, skipping signing of $ARTIFACT" >&2
  exit 0
fi

exec quill sign-and-notarize "$ARTIFACT" -vv
