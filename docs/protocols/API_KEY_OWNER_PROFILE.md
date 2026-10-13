# API Key Owner Profile

`GET /v1/me` returns the owner profile of the API key used to call it. It is a
local, read-only gateway endpoint: no upstream account is selected, nothing is
billed, and no concurrency slot is acquired.

## Authentication

Same as other `/v1` gateway endpoints: `Authorization: Bearer <api-key>`,
`x-api-key`, or the other API key carriers accepted by the gateway API key
middleware. The key must be assigned to a group (ungrouped keys are rejected by
the same group-assignment gate as `/v1/usage`).

## Response

```json
{
  "user_id": 42,
  "username": "owner",
  "email": "owner@example.com",
  "role": "user",
  "status": "active",
  "balance": 12.5,
  "concurrency": 3,
  "api_key": {
    "id": 9,
    "name": "laptop",
    "status": "active",
    "group_id": 7,
    "group_name": "Pro"
  }
}
```

- User fields are read from the database on each call, so `balance` is current.
- `api_key.group_name` is omitted when the key has no loaded group.
- The response never includes the API key secret or credential material.

## Errors

| Status | `error.type` | Cause |
| --- | --- | --- |
| 401 | `authentication_error` | Missing or invalid API key |
| 500 | `api_error` | Owner user could not be loaded |

For key quota, rate-limit, subscription, and usage statistics use `GET /v1/usage`.

## Owner API Key List

`GET /v1/me/keys` lists every API key owned by the caller's user, including
disabled, expired, and quota-exhausted keys. Key secrets are never returned;
each key is identified by `key_hash`, the lowercase hex SHA-256 of the key.
It is local and read-only like `GET /v1/me`.

The endpoint is disabled (404) unless `gateway.owner_keys_token_secret`
(`GATEWAY_OWNER_KEYS_TOKEN_SECRET`) is set.

### Request

```
GET /v1/me/keys?timestamp=<unix seconds>&email_hash=<hex>
Authorization: Bearer <any API key of the user>
X-Auth-Token: <token>
```

- `timestamp`: Unix seconds; must be within 5 minutes of server time.
- `X-Auth-Token`: `base64(sha256(secret + timestamp))`, standard base64 of the
  raw 32-byte digest, where `timestamp` is the exact query string value.
- `email_hash`: hex SHA-256 of the owner email after trimming whitespace and
  lowercasing. Comparison is case-insensitive.

### Response

```json
{
  "user_id": 42,
  "keys": [
    {
      "id": 9,
      "name": "laptop",
      "status": "active",
      "group_id": 7,
      "group_name": "Pro",
      "key_hash": "<sha256 hex of the key>",
      "created_at": "2026-01-01T00:00:00Z",
      "last_used_at": "2026-01-02T00:00:00Z",
      "expires_at": null
    }
  ]
}
```

`group_name` is omitted when the key has no group.

### Errors

| Status | `error.type` | Cause |
| --- | --- | --- |
| 400 | `invalid_request_error` | `timestamp` is not an integer, or `email_hash` is missing |
| 401 | `authentication_error` | Invalid API key, expired `timestamp`, or invalid `X-Auth-Token` |
| 403 | `permission_error` | `email_hash` does not match the owner email |
| 404 | `not_found_error` | Endpoint disabled (no secret configured) |
| 500 | `api_error` | Owner user or keys could not be loaded |
