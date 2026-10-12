Sourcing ~/.zshenv
# Tiimo MCP — personal Go server

Read and manage Tiimo from an MCP client without operating the web UI. Built with the official Go MCP SDK. Runs as one Go process with embedded SQLite; no Node, separate database server, or background AI service. Railway terminates HTTPS in front of the binary.

## Status

The Tiimo endpoints and field mappings were checked against Tiimo's live OpenAPI document on **2026-10-07**. Local tests exercise MCP stdio/in-memory and Streamable HTTP, task preservation, minute-to-second conversion, timezone handling, native scheduling, OAuth PKCE, replay protection, SQLite persistence across restarts, refresh rollback, and atomic refresh rotation. No authenticated live Tiimo account was available during development: use the read-only check before relying on writes. Docker deployment and ChatGPT account linking still need to be verified after deployment.

This is an unofficial, **single-owner** integration, not a shared service. All authorized clients operate on the one Tiimo account configured in the service environment.

## Tools

| Tool | Purpose |
| --- | --- |
| `list_profiles` | Discover profile IDs |
| `list_task_lists` | Read Work / School / BSF lists |
| `list_tasks`, `get_task` | Read unscheduled tasks and their details |
| `create_task_list` | Create a list |
| `create_task` | Add a task, estimate, notes, list, emoji, tags, and checklist |
| `update_task` | Change selected fields while preserving existing details |
| `set_task_completed` | Complete or reopen a task |
| `delete_task` | Delete one task |
| `list_activities`, `get_activity` | Read the timeline |
| `create_activity` | Create a new scheduled activity |
| `schedule_task` | Move a list task to the timeline using native conversion |
| `reschedule_activity` | Move a single nonrepeating Tiimo activity |
| `complete_activity` | Record completion for an occurrence |
| `delete_activity` | Delete a single nonrepeating Tiimo activity |

Set `TIIMO_READ_ONLY=true` to expose only the six read tools. UUIDs come from Tiimo reads; estimates are **minutes of work**, converted to API seconds. Scheduling tools accept exactly one of these choices:

- `start`: an RFC3339 date-time with its UTC offset (for example, `2026-10-07T10:00:00-04:00`). New York switches offsets with daylight saving time; choose the offset for the date being scheduled.
- `date` plus `timeOfDay`: a profile-local calendar date (`YYYY-MM-DD`) and one of `Anytime`, `Morning`, `Afternoon`, or `Evening`. Tiimo's API labels Afternoon as `Day`; this server sends `grouping: null` for Anytime so Tiimo does not default it to Morning.

The API body mapping is covered by local MCP and HTTP-mock tests. A live Tiimo round-trip confirmed Morning, Afternoon (`Day`), and Evening groupings. It also showed that omitting grouping for Anytime defaults a converted task to Morning; the explicit null in this change addresses that behavior and should be confirmed in the deployed connector.

Tiimo's task API does not expose a due-date field. Notes can record a deadline, but this does not create a native deadline. A list read may return grouped tasks; results retain Tiimo's original response structure. `create_activity` creates a separate item; use `schedule_task` to move an existing task.

## Build and test

Requires Go 1.25 or newer and a C compiler (GCC or Clang). CI and Docker use Go 1.27.1. The SQLite driver embeds SQLite in the binary via CGO; no SQLite service or external database library is required. Docker includes the build compiler only in the build stage.

```sh
go test -race ./...
go vet ./...
CGO_ENABLED=1 go build -tags sqlite_omit_load_extension -buildvcs=false -trimpath -ldflags='-s -w' -o bin/tiimo-mcp ./cmd/tiimo-mcp
```

## Get a Tiimo access token

Tiimo's API advertises a JWT bearer token. This project deliberately does not guess a Tiimo OAuth client registration or accept your Tiimo password.

1. Sign in normally to Tiimo's web app on your own computer.
2. Open browser developer tools → Network, reload, and select an authenticated request to `api.tiimoapp.com` (such as `/api/profiles`).
3. Copy the `Authorization` request header's token (omit the `Bearer ` prefix) **directly into Railway's `TIIMO_TOKEN` secret variable**, or into a private local token file. Do not put it in an issue, commit, chat, screenshot, or exported network log.
4. Initially omit `TIIMO_PROFILE_ID`; `list_profiles` will return it. If your account has multiple profiles, choose yours explicitly.

Tiimo controls this token's lifetime. A 401 means it must be replaced. Automatic Tiimo refresh is not implemented because no verified third-party client registration/refresh contract was available. This is the main remaining operational limitation. Local `TIIMO_TOKEN_FILE` is read on every call, so replacing its contents does not require restarting. Updating a Railway environment variable redeploys the service.

A read-only credential check:

```sh
# Supply TIIMO_TOKEN in the environment, or use TIIMO_TOKEN_FILE.
TIIMO_TOKEN_FILE=/absolute/path/tiimo.token ./bin/tiimo-mcp -doctor
```

The doctor reads `/api/profiles` and prints its response. It does not create or update tasks.

## Railway and GitHub

Copy this directory into your repository root, or configure Railway's Root Directory to this directory if it is a subfolder. The included GitHub workflow runs tests, race detection, vet, and a CGO build. No repository has been created or pushed by this project.

1. In Railway, create a service from the GitHub repository. The included `Dockerfile` and `railway.toml` configure the build, health check, and one replica.
2. Attach a volume to this service with the mount path `/data`. Set `RAILWAY_RUN_UID=0` so the entrypoint can initialize volume permissions. The entrypoint prepares `/data/auth` with owner UID/GID `65532:65532` and mode `0700`, fixes the database/sidecar ownership, then uses `su-exec` to replace itself with the Go server running as UID/GID 65532. A `077` umask keeps SQLite files private. Root is used only for initialization, not for the running server.
3. Generate a Railway public domain. Set `PUBLIC_URL` to its HTTPS **origin**, without `/mcp`, or let the server use Railway's `RAILWAY_PUBLIC_DOMAIN`.
4. Set these Railway variables:

   | Variable | Value |
   | --- | --- |
   | `TIIMO_TOKEN` | Your current Tiimo access token |
   | `TIIMO_PROFILE_ID` | Optional until profile discovery; then your UUID |
   | `TIIMO_READ_ONLY` | `true` for initial connection; `false` for writes |
   | `AUTH_DB_PATH` | `/data/auth/auth.db` (default) |
   | `RAILWAY_RUN_UID` | `0` for volume initialization; entrypoint drops to UID 65532 |
   | `OAUTH_CLIENT_ID` | A private identifier such as `tiimo-private` |
   | `OAUTH_CLIENT_SECRET` | A separately generated random secret |
   | `MCP_LOGIN_PASSWORD` | Another independently generated random secret |
   | `OAUTH_REDIRECT_URIS` | Exact callback URI shown by ChatGPT; default is `https://chatgpt.com/connector_platform_oauth_redirect` |

   Generate **each** secret separately with `openssl rand -hex 32`. Never reuse your Tiimo password. Railway supplies `PORT`.
5. Deploy. `https://YOUR-DOMAIN/healthz` should return `{"status":"ok"}`. `/mcp` should return **401** without OAuth; this is expected. The health check reports process health, not Tiimo credential validity.
6. In ChatGPT, add a custom MCP server with the **Streamable HTTP** URL `https://YOUR-DOMAIN/mcp`, choose **OAuth**, and provide the static `OAUTH_CLIENT_ID` and `OAUTH_CLIENT_SECRET` in the connection settings. Copy the exact redirect URI shown there into the Railway allowlist if it differs. Multiple exact callbacks can be comma-separated; wildcards are rejected.
7. Link the account. The server's login page asks for `MCP_LOGIN_PASSWORD`, not your Tiimo password.
8. Ask it to run `list_profiles`, `list_task_lists`, and `list_activities` for a small date range. Configure the profile ID if needed.
9. Enable writes only when you are ready to test them. Reconnect or refresh the client's tool list after changing read-only mode. Create one disposable task, inspect its estimate/checklist in Tiimo, edit it, complete it, and delete it. Test a second disposable task with `schedule_task`, check the timeline and original list, then delete the activity.

The server includes a small personal OAuth authorization-code flow with S256 PKCE, an exact redirect allowlist, issuer identification, resource-bound opaque tokens, one-hour access tokens, and rotating seven-day refresh tokens. Static client registration avoids an open registration endpoint. Access and refresh token hashes are stored in SQLite at **`/data/auth/auth.db`**. With the `/data` volume attached, unexpired credentials survive restarts and redeploys. Raw bearer tokens, your Tiimo token, login password, and client secret are not stored in the database. Refresh-token consumption and replacement commit together; failed writes roll back, and replay is rejected. Expired rows are pruned during issuance and startup. SQLite uses WAL mode, full synchronization, one connection, and a small local file; it is not a separate database service.

Short-lived pending authorization requests and authorization codes stay in memory, so an interrupted initial login may need to be retried after a restart. Existing connections remain valid. Changing the public URL, OAuth client ID, client secret, or login password deliberately revokes all stored credentials. Changing the upstream Tiimo token alone does not revoke MCP credentials. Removing/restoring an older volume can lose/revert credential state; use SQLite-aware backups and treat backups as sensitive. For this deployment, keep **one replica**. If SQLite cannot open or commit, authentication fails closed rather than falling back to temporary storage.

ChatGPT MCP setup and OAuth details:
- https://developers.openai.com/api/docs/guides/custom-mcp-server
- https://developers.openai.com/plugins/build/auth

## Local use

For a local stdio client:

```sh
TIIMO_TOKEN_FILE=/absolute/path/tiimo.token ./bin/tiimo-mcp -transport=stdio
```

MCP client configuration example:

```json
{
  "mcpServers": {
    "tiimo": {
      "command": "/absolute/path/tiimo-mcp/bin/tiimo-mcp",
      "args": ["-transport=stdio"],
      "env": {
        "TIIMO_TOKEN_FILE": "/absolute/path/tiimo.token",
        "TIIMO_PROFILE_ID": "YOUR_PROFILE_UUID"
      }
    }
  }
}
```

With no `PUBLIC_URL` or `RAILWAY_PUBLIC_DOMAIN`, HTTP binds only to `127.0.0.1:8080`, rejects foreign Host/Origin headers, and needs no MCP OAuth. Do not place this unauthenticated localhost mode behind a public tunnel. Public mode requires all OAuth credentials before it starts. The binary reads environment variables; it does **not** automatically load `.env` files. For local authenticated HTTP, set `AUTH_DB_PATH` to a file in a private local directory. Public HTTP defaults to `/data/auth/auth.db`; stdio and unauthenticated localhost mode do not open a token database.

## Limits and failure behavior

- Rescheduling/deleting recurring activities, timed routines, and external calendar events is excluded. This avoids implicit series-wide changes and external writes.
- Task edits use a read → preserve → write sequence, protected against concurrent edits through this server. Tiimo does not advertise revision preconditions here; a concurrent edit in the Tiimo app can still race with an MCP edit.
- No mutation is automatically retried. After an uncertain write or failed conversion, read Tiimo before retrying so you do not create duplicates.
- API errors exclude upstream bodies and credentials. API redirects are not followed. Requests have timeouts and response limits.
- The runtime has no Node process, separate database service, telemetry, or LLM calls. Hosting cost depends on your Railway plan and measured resource usage; no cost estimate is promised.

## Code layout

- `cmd/tiimo-mcp`: flags, environment, HTTP, stdio, and read-only doctor.
- `internal/tiimo`: bearer-authenticated API client, contract snapshot, and response-to-write projection.
- `internal/tools`: typed MCP tools and integration tests.
- `internal/oauth`: private single-owner OAuth flow, SQLite token storage, and tests.
- `docker/entrypoint.sh`: volume ownership, private permissions, and privilege drop.
- `.github/workflows/test.yml`, `Dockerfile`, `railway.toml`: repository checks and deployment.

API source: https://api.tiimoapp.com/index.html and https://api.tiimoapp.com/swagger/TiimoAPI/swagger.json
