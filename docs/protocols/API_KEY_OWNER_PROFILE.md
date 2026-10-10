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
