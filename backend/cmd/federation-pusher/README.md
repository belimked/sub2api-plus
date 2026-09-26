# federation-pusher

Polls `federation_outbox_events` (written by the `FederationOutboxMixin` ent
hook, see `backend/ent/schema/mixins/federation_outbox.go`) for pending
`user.upsert` events and delivers them to an overseas sub2api-plus
deployment's admin API. Deploy as an independent process next to the
mainland server, pointed at the same database — it does not run inside the
main server binary.

Full architecture and rationale: `sub2api-federation-design.md` (repo root)
and `openspec/changes/federation-outbox-sync/`.

## Configuration (environment variables)

Reuses the main server's `DATABASE_*`/`TIMEZONE` settings to connect to the
mainland database. Federation-specific settings:

| Variable | Required | Default | Notes |
|---|---|---|---|
| `FEDERATION_OVERSEAS_BASE_URL` | yes | — | e.g. `https://api.example.com` |
| `FEDERATION_ADMIN_EMAIL` | yes | — | admin login on the overseas deployment |
| `FEDERATION_ADMIN_PASSWORD` | yes | — | |
| `FEDERATION_POLL_INTERVAL` | no | `5s` | Go duration syntax |
| `FEDERATION_BATCH_SIZE` | no | `20` | rows claimed per poll |
| `FEDERATION_MAX_ATTEMPTS` | no | `8` | row marked `failed` after this many tries |
| `FEDERATION_HTTP_TIMEOUT` | no | `20s` | per-request timeout to the overseas API |
| `FEDERATION_DELIVERED_RETENTION` | no | `168h` | delivered rows older than this are deleted, checked at most hourly; `0` disables |

## Behavior

- Upserts by **email**, never by id — `user.id` is a per-database
  auto-increment and the two databases are independent.
- Sends `Idempotency-Key: federation-outbox-<row id>` on every write to the
  overseas API.
- Retryable failures (network errors, 429, 5xx) go back to `pending` with an
  exponential backoff (`next_retry_at`, capped at 5 minutes) and increment
  `attempts`; after `FEDERATION_MAX_ATTEMPTS` they become `failed` too.
- Terminal failures (4xx other than 429) are marked `failed` immediately and
  are never retried automatically — `last_error` on the row explains why. A
  human needs to fix the underlying issue and reset `status` back to
  `pending` to retry.
- Retention: at most once an hour the pusher deletes `delivered` rows whose
  `updated_at` is older than `FEDERATION_DELIVERED_RETENTION`. `failed` rows
  are never purged — they wait for manual review.
- The main server writes outbox rows only when `federation.outbox_enabled`
  (`FEDERATION_OUTBOX_ENABLED=true`) is set; enable it on the mainland node
  that runs this pusher.

## Running as a compose service

The server image also contains `/app/federation-pusher`. Add a service next to
`sub2api` in the deployment's `docker-compose.override.yml`, reusing the
same image and the same `DATABASE_*`/`TZ` values as the `sub2api` service,
with the federation credentials in a separate `0600` env file:

```yaml
services:
  federation-pusher:
    image: <same image as sub2api>
    command: ["/app/federation-pusher"]
    restart: unless-stopped
    env_file: [federation-pusher.env]
    environment:
      - DATABASE_HOST=...        # copy from the sub2api service
      - DATABASE_MAX_OPEN_CONNS=5
    volumes:
      - sub2api_data:/app/data:ro
    healthcheck:
      disable: true              # the image healthcheck probes :8080
```

## Password sync

`user.upsert` rows carry the user's bcrypt `password_hash` (emitted on
create, password change and email change). The pusher creates a missing
overseas user with a throwaway password, then sets the hash verbatim through
`POST /api/v1/admin/users/:id/federation-password-hash`, so the mainland
password works overseas. The overseas server must set
`federation.accept_password_hash` (`FEDERATION_ACCEPT_PASSWORD_HASH=true`);
otherwise the endpoint returns 404 and the row fails. The hash is never
logged or written to `last_error`.

Admin accounts are never federated: the mainland emits nothing for role
`admin`, and an overseas user with role `admin` is never modified (the row
fails terminally), so a shared admin email cannot overwrite the overseas
admin. Deleting a mainland user emits no event; remove the overseas mirror by
hand.

## Backfill

Users created before the outbox was enabled have no rows. Queue one
`user.upsert` + `balance.snapshot` per live non-admin user, then let the
running pusher deliver them:

```sh
docker compose exec federation-pusher /app/federation-pusher backfill-users --dry-run
docker compose exec federation-pusher /app/federation-pusher backfill-users
```

`--dry-run` only reports the count. Check `failed` rows afterwards: an email
that is an admin overseas fails by design.
