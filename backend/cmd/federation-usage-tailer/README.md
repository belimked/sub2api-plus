# federation-usage-tailer

Tails this deployment's own `usage_log` (append-only) and pushes cost
deductions to a mainland sub2api-plus deployment's admin API. Deploy this on
the **overseas** side, connected to the overseas database — it's the mirror
image of `backend/cmd/federation-pusher`, which runs on the mainland side.

Full architecture: `sub2api-federation-design.md` (repo root, §4.3) and
`openspec/changes/federation-usage-tailer/`.

## Configuration (environment variables)

Reuses the local server's `DATABASE_*`/`TIMEZONE` settings to read this
deployment's own `usage_log`. Federation-specific settings:

| Variable | Required | Default | Notes |
|---|---|---|---|
| `FEDERATION_MAINLAND_BASE_URL` | yes | — | the mainland deployment's admin API |
| `FEDERATION_ADMIN_EMAIL` | yes | — | admin login on the mainland deployment |
| `FEDERATION_ADMIN_PASSWORD` | yes | — | |
| `FEDERATION_POLL_INTERVAL` | no | `5s` | Go duration syntax |
| `FEDERATION_BATCH_SIZE` | no | `50` | usage rows read per poll |
| `FEDERATION_HTTP_TIMEOUT` | no | `20s` | per-request timeout to the mainland API |
| `FEDERATION_CURSOR_SCOPE` | no | `overseas_to_mainland` | lets more than one tailer/target pair coexist |

## Behavior

- Reads `usage_log` in strict `id` order from `last_delivered_id + 1`.
- The first run creates the cursor at the **current** `max(usage_log.id)`:
  usage recorded before the tailer started was already paid from this
  deployment's own balance and is never replayed to mainland.
- Resolves the mainland user id **by email** (never by id — the two
  databases' ids are independent) and caches the mapping per process run.
- Sends `Idempotency-Key: federation-usage-<usage_log.id>` on every balance
  call.
- **Never skips a failed row.** If a row fails to bill (including the
  mainland side hard-rejecting because the subtraction would go negative —
  `ErrBalanceNegative`, confirmed during the `federation-outbox-sync` POC),
  the cursor stops advancing at that row and every later row waits behind
  it, retrying with the same exponential backoff (capped at 5 minutes) the
  pusher uses. This trades throughput for correctness: skipping would mean
  that usage's cost is never billed to anyone.
- Rows with zero/negative cost, or whose local user record is gone (e.g.
  soft-deleted), are treated as delivered without a network call — nothing
  to bill, so skipping them loses no money.
- Rows of a **local-only user** — one whose email mainland definitively
  doesn't have — are skipped and logged: such a user is billed only by this
  deployment's own balance, so native overseas users and federated mirrors
  can share one instance. A failed lookup (network error, mainland 5xx) is
  not a miss and still blocks the cursor. Misses aren't cached, so a user
  later created on mainland is picked up without a restart.

## Running as a compose service

The server image also contains `/app/federation-usage-tailer`. Add a service next to
`sub2api` in the deployment's `docker-compose.override.yml`, reusing the
same image and the same `DATABASE_*`/`TZ` values as the `sub2api` service,
with the federation credentials in a separate `0600` env file:

```yaml
services:
  federation-usage-tailer:
    image: <same image as sub2api>
    command: ["/app/federation-usage-tailer"]
    restart: unless-stopped
    env_file: [federation-tailer.env]
    environment:
      - DATABASE_HOST=...        # copy from the sub2api service
      - DATABASE_MAX_OPEN_CONNS=5
    volumes:
      - sub2api_data:/app/data:ro
    healthcheck:
      disable: true              # the image healthcheck probes :8080
```

## Known limitation

A permanently stuck cursor (e.g. a user's mainland account can never absorb
the debt) has no alerting wired up — `last_error`/`next_retry_at` on the
`federation_usage_cursors` row make the stuck state visible, but nothing
pages anyone. See `openspec/changes/federation-usage-tailer/proposal.md`
Non-goals.
