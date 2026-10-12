#!/bin/sh
set -eu
# Applies to the database, WAL, shared-memory, and journal files created later.
umask 077
export AUTH_DB_PATH="${AUTH_DB_PATH:-/data/auth/auth.db}"
case "$AUTH_DB_PATH" in
  /data/auth/*) auth_file="${AUTH_DB_PATH#/data/auth/}" ;;
  *) echo 'Container AUTH_DB_PATH must be a file inside /data/auth' >&2; exit 1 ;;
esac
case "$auth_file" in
  ''|*/*|.|..) echo 'Invalid database filename' >&2; exit 1 ;;
esac
if [ -L /data ] || [ -L /data/auth ]; then
  echo 'Auth directory must not be a symbolic link' >&2; exit 1
fi
if [ "$(id -u)" = 0 ]; then
  mkdir -p /data/auth
  chown 65532:65532 /data/auth
  chmod 0700 /data/auth
  # Fix ownership when restoring an existing database from a volume backup.
  # Touch only the database and its SQLite sidecars, never the entire volume.
  for path in "$AUTH_DB_PATH" "$AUTH_DB_PATH-wal" "$AUTH_DB_PATH-shm" "$AUTH_DB_PATH-journal"; do
    if [ -L "$path" ]; then
      echo 'Auth database files must not be symbolic links' >&2; exit 1
    fi
    if [ -e "$path" ]; then
      if [ ! -f "$path" ]; then echo 'Auth database must be a regular file' >&2; exit 1; fi
      chown 65532:65532 "$path"
      chmod 0600 "$path"
    fi
  done
  exec su-exec 65532:65532 /tiimo-mcp "$@"
fi
# Non-root starts are supported only when the directory already has correct ownership.
if [ "$(id -u)" != 65532 ] || [ ! -d /data/auth ] || [ ! -w /data/auth ]; then
  echo 'Volume initialization needs root; set RAILWAY_RUN_UID=0' >&2; exit 1
fi
exec /tiimo-mcp "$@"
