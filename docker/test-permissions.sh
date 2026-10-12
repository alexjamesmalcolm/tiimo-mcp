#!/bin/sh
# Runs against the actual Docker image, with a fresh root-owned volume.
set -eu
image="${1:-tiimo-mcp:test}"
volume="$(docker volume create)"
container=""
cleanup() {
  if [ -n "$container" ]; then docker rm -f "$container" >/dev/null 2>&1 || true; fi
  docker volume rm "$volume" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM
container="$(docker run -d -v "$volume:/data" \
  -e TIIMO_TOKEN=mock-transport-only \
  -e TIIMO_READ_ONLY=true \
  -e PUBLIC_URL=https://tiimo.example \
  -e OAUTH_CLIENT_ID=private-client \
  -e OAUTH_CLIENT_SECRET=ssssssssssssssssssssssssssssssss \
  -e MCP_LOGIN_PASSWORD=pppppppppppppppppppppppppppppppp \
  "$image")"
ready=false
for attempt in $(seq 1 30); do
  if docker exec "$container" test -f /data/auth/auth.db 2>/dev/null; then ready=true; break; fi
  sleep 1
done
if [ "$ready" != true ]; then docker logs "$container"; exit 1; fi
docker exec "$container" sh -eu -c '
  test "$(awk "/^Uid:/{print \$2}" /proc/1/status)" = 65532
  test "$(awk "/^Gid:/{print \$2}" /proc/1/status)" = 65532
  test "$(stat -c "%u:%g:%a" /data/auth)" = 65532:65532:700
  test "$(stat -c "%u:%g:%a" /data/auth/auth.db)" = 65532:65532:600
'
docker restart "$container" >/dev/null
for attempt in $(seq 1 30); do
  if docker exec "$container" sh -c 'test "$(awk "/^Uid:/{print \$2}" /proc/1/status)" = 65532' 2>/dev/null; then break; fi
  sleep 1
done
docker exec "$container" sh -eu -c '
  test "$(awk "/^Uid:/{print \$2}" /proc/1/status)" = 65532
  test "$(stat -c "%u:%g:%a" /data/auth/auth.db)" = 65532:65532:600
'
echo 'Docker volume initialization, database ownership, privilege drop, and restart passed'
