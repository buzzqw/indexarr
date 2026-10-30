#!/bin/sh
# Container entry point.
#
# umask 077 keeps the SQLite database and the vault key private (0600) even
# when the volume was created by an older image with the default 0022.
# INDEXARR_LISTEN and INDEXARR_CONFIG allow changing the address and the
# configuration file without rebuilding the image; extra arguments are
# appended and win over the defaults (the Go flag package keeps the last one).
set -eu
umask 077

config="${INDEXARR_CONFIG:-/data/indexarr.json}"
listen="${INDEXARR_LISTEN:-:8788}"

exec /usr/local/bin/indexarr -config "$config" -listen "$listen" "$@"
