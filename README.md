# sentrygram

Relays [Sentry](https://sentry.io) issue alerts to a Telegram chat. Sentry's internal integrations send webhooks; sentrygram posts one message per new issue (`created`) and per issue that comes back (`unresolved`), and drops every other event (`error.created`, assigned, resolved, `seer.*`). So a bad deploy is one message per issue, not one per event.

Standard library only; one static binary in a distroless image, `ghcr.io/rahacloud/sentrygram`.

## Configuration

| Variable | Meaning |
| --- | --- |
| `TELEGRAM_BOT_TOKEN` | the bot's token (required) |
| `TELEGRAM_CHAT_ID` | the chat to post to, e.g. `-5464692517` (required); the bot must be a member |
| `RELAY_PATH_TOKEN` | when set, the last path segment every webhook must carry; anything else gets a 404 |
| `SENTRY_CLIENT_SECRET` | when set, checks `Sentry-Hook-Signature` (HMAC-SHA256 of the body with the integration's client secret) |
| `SENTRY_URL` | Sentry's address, for links when a payload carries none |
| `NOISE_CULPRITS` | comma-separated culprit prefixes to drop, e.g. `django.core.management.commands.shell` |
| `ADDR` | listen address, default `:8080` |

`GET` answers 200, for health checks.

## Wiring it to Sentry

1. Settings › Developer Settings › Custom Integrations › New internal integration; webhook URL `https://<host>/hooks/telegram/<RELAY_PATH_TOKEN>`, and the **Issue** webhook resource.
2. Self-hosted Sentry (25.9 and later) refuses webhooks to private addresses (`send_and_save_webhook_request.restricted_ip` in the taskworker log), so the URL must resolve to a public address: put sentrygram behind the reverse proxy, and keep `RELAY_PATH_TOKEN` or `SENTRY_CLIENT_SECRET` set.
3. Sentry's own `internal` project fires no integration webhooks; test with another project.

## Development

```sh
just test
just lint
just image
```

Used by Hatchup's self-hosted Sentry (rahacloud/hatchup-infrastructure, `playbooks/templates/sentry/`).
