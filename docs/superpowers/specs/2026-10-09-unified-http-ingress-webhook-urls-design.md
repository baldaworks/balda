# Public URL layout for Balda Assistant

Balda Assistant uses one public origin and one deployment path prefix. Three
application areas sit beneath that prefix: `/backoffice` for the browser UI,
`/webhooks` for generic inbound webhooks, and `/gateway` for chat transport
callbacks. These are sibling paths; none is nested inside another.

## Example configuration on asus

```yaml
balda:
  http:
    base_url: https://lab.metalagman.dev
    base_path: /balda
```

`base_url` is the public origin: scheme and host, without a trailing slash or
path. `base_path` is the shared public prefix: an absolute path with a leading
slash and no trailing slash. The application joins them once, then appends the
area path. The example yields:

| Area | Public URL |
| --- | --- |
| Backoffice | `https://lab.metalagman.dev/balda/backoffice/` |
| Webhook management | `https://lab.metalagman.dev/balda/backoffice/webhooks` |
| Incoming managed webhook `orders` | `https://lab.metalagman.dev/balda/webhooks/orders` |
| Slack Events callback | `https://lab.metalagman.dev/balda/gateway/slack/events` |
| Slack Commands callback | `https://lab.metalagman.dev/balda/gateway/slack/commands` |

The Backoffice webhook page displays the complete incoming URL for each route.
For a newly managed route, its name determines the `/webhooks/<name>` path;
there is no second editable Path field. Existing route paths are not rewritten
merely by displaying this scheme.

This is the proposed configuration contract and URL example, not the current
Balda configuration. The current application has
`balda.backoffice.public_url` and `balda.backoffice.base_path`; a shared
`balda.http.base_url` / `balda.http.base_path` requires a separate code change.
The current ingress, reverse proxy, and asus deployment are outside this
design example's scope.
