# Public URL layout for Balda Assistant

Balda Assistant uses one public origin and one deployment path prefix. Three
application areas sit beneath that prefix: `/backoffice` for the browser UI,
`/webhooks` for generic inbound webhooks, and `/gateway` for chat transport
callbacks. These are sibling paths; none is nested inside another.

## Example configuration

```yaml
balda:
  http:
    base_url: https://balda.example.com
    base_path: /balda
```

`base_url` is the public origin: scheme and host, without a trailing slash or
path. `base_path` is the shared public prefix: an absolute path with a leading
slash and no trailing slash. The application joins them once, then appends the
area path. The example yields:

| Area | Public URL |
| --- | --- |
| Backoffice | `https://balda.example.com/balda/backoffice/` |
| Webhook management | `https://balda.example.com/balda/backoffice/webhooks` |
| Incoming managed webhook `orders` | `https://balda.example.com/balda/webhooks/orders` |
| Slack Events callback | `https://balda.example.com/balda/gateway/slack/events` |
| Slack Commands callback | `https://balda.example.com/balda/gateway/slack/commands` |

**Generic webhook rules superseded by Story `balda-z4p8`:** the original design
retained existing custom paths and derived paths only for newly managed routes.
Every config and managed route now derives `<base_path>/webhooks/<immutable-name>`;
old custom URLs stop working. The Backoffice page shows a complete public URL
only when `balda.http.base_url` is explicit; otherwise it shows the canonical
path and an explanation. There is no editable or persisted per-route path.

This design's application configuration and URL layout are implemented by the
public HTTP URL Story; see the current
[configuration reference](../../reference/configuration.md#shared-http-listener-and-public-urls).
Reverse-proxy setup and external callback registrations are deployment
concerns outside this application URL design.
