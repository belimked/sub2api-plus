# aliyun-moderation

An OpenAI-compatible `POST /v1/moderations` backed by Aliyun AI Guardrails
(AI 安全护栏, `TextModerationPlus`, API version `2022-03-02`). Point
sub2api-plus Content Moderation at it to audit user input with Aliyun instead
of OpenAI. It is a stateless sidecar shipped in the same image
(`/app/aliyun-moderation`); it has no database access.

Content Moderation keeps owning ingress ordering, extraction, hash caching,
observe/blocking mode, thresholds, logs and bans. This adapter only turns one
moderation call into Aliyun calls and maps the verdict back.

## Configuration (environment variables)

| Variable | Required | Default | Notes |
|---|---|---|---|
| `MODERATION_API_KEY` | yes | — | bearer token; set the same value as the Content Moderation API key |
| `ALIYUN_ACCESS_KEY_ID` | yes | — | RAM user limited to `AliyunYundunGreenWebFullAccess` |
| `ALIYUN_ACCESS_KEY_SECRET` | yes | — | |
| `ALIYUN_GREEN_REGION` | no | `cn-shanghai` | overseas accounts use `ap-southeast-1` |
| `ALIYUN_GREEN_ENDPOINT` | no | `https://green-cip.<region>.aliyuncs.com` | |
| `ALIYUN_GREEN_SERVICE` | no | `query_security_check_pro` | the input-check service enabled in the Aliyun console (overseas: `query_security_check_cb`) |
| `ALIYUN_GREEN_MIN_RISK_LEVEL` | no | `high` | lowest content `RiskLevel` reported as flagged: `low`, `medium`, `high` |
| `ALIYUN_GREEN_ATTACK_MIN_LEVEL` | no | `high` | same for prompt-attack `AttackLevel`; `off` ignores attack results |
| `ALIYUN_GREEN_TIMEOUT` | no | `3s` | per Aliyun call |
| `ALIYUN_GREEN_MAX_CHUNKS` | no | `3` | Aliyun accepts 2000 characters per call; text beyond `3 × 2000` is not checked |
| `MODERATION_LISTEN` | no | `:8090` | |

## Behavior

- Input may be a string, a string array or an OpenAI content-part array; only
  text parts are checked. Empty text returns an unflagged result without
  calling Aliyun.
- Text is split into 2000-character chunks, checked in parallel; the worst
  verdict wins.
- A content hit at or above `ALIYUN_GREEN_MIN_RISK_LEVEL` sets its mapped
  OpenAI category to `1.0` (weapons/violence → `violence`, extremism →
  `illicit/violent`, pornography → `sexual`, minors → `sexual/minors`,
  discrimination → `hate`, profanity → `harassment`, everything else such as
  politics, religion, contraband, ads or custom word lists → `illicit`). A
  prompt attack at or above `ALIYUN_GREEN_ATTACK_MIN_LEVEL` sets `illicit`.
  Because the score is `1.0`, any Content Moderation threshold is crossed;
  tune sensitivity with the two levels and the Aliyun console rules instead.
- Every Aliyun label is also returned as `aliyun/<label>` or
  `aliyun/attack/<label>` with `confidence / 100`. Content Moderation records
  these in its logs but never evaluates them against thresholds.
- Aliyun errors or timeouts return `502`, which Content Moderation treats as
  an audit-dependency failure (pass-through, logged).
- Logs contain labels, chunk count, character count and latency, never the
  text.

## Deployment

Compose service next to the server, on the same network:

```yaml
aliyun-moderation:
  image: ghcr.io/belimked/sub2api-plus:<tag>
  command: ["/app/aliyun-moderation"]
  restart: unless-stopped
  env_file: [aliyun-moderation.env]
  networks: [sub2api-network]
  healthcheck:
    disable: true
```

Then in the admin console, Content Moderation: base URL
`http://aliyun-moderation:8090`, API key = `MODERATION_API_KEY`. Start with
mode `observe` (asynchronous, no request latency) and switch to `pre_block` after
reviewing hits.
