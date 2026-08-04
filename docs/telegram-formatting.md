# Telegram Message Formatting

Balda sends formatted assistant output according to
`balda.telegram.formatting_mode`. Telegram supports exactly three values:

- `rich_markdown` (default): natural Markdown sent through Telegram rich messages.
- `rich_html`: sanitized Telegram Rich HTML sent through Telegram rich messages.
- `none`: literal plain text sent without `parse_mode`.

An unsupported value fails configuration validation. Choose `rich_markdown` or
`rich_html` when migrating an older formatted configuration, or `none` when
presentation markup must remain literal.

This setting is Telegram-specific. Slack uses its native `mrkdwn` route and
Zulip uses its native Markdown route.

## Configuration and migration

Configure the mode in YAML or with its environment override:

```yaml
balda:
  telegram:
    formatting_mode: rich_markdown
```

```bash
BALDA_TELEGRAM_FORMATTING_MODE=rich_markdown
```

The supported values are a hard-cut contract. Replace an older `markdownv2`
value with `rich_markdown` (or `none`) and an older `html` value with
`rich_html` (or `none`) before deploying this version. There is no transitional
decoder for old configuration values or already-enqueued delivery payloads.
For an upgrade with pending work, stop old ingress and drain the existing
actor-command backlog before starting the new version. Unsupported values and
incomplete format registries fail during startup, before provider ingress is
ready.

Balda persists only the transport capability on turn and delivery commands.
At startup, one immutable registry resolves that capability to both the agent
prompt instructions and the process-local formatter. This keeps generated and
delivered formatting on the same route without persisting formatter internals.

## Rich Markdown

Use `rich_markdown` when agents should write natural Markdown. Balda preserves
the model output and sends it as one Telegram rich-message payload, including
standalone `---` separators and fenced code blocks.

Example model output:

~~~markdown
**Build:** success

- Run `balda start`
- Check logs

```bash
go test ./...
```
~~~

If Telegram explicitly rejects the rich formatting, Balda makes at most one
parse-mode-free plain send. Ambiguous transport errors, timeouts, ordinary
request errors, authentication errors, rate limits, and provider failures do
not trigger a presentation fallback.

## Rich HTML

Use `rich_html` when agents should write Telegram Rich HTML directly. Before
delivery, Balda preserves supported tags, drops unsupported attributes from
supported tags, and escapes unsafe raw markup.

Supported tags and attributes include:

- `<b>`, `<strong>`, `<i>`, `<em>`, `<u>`, `<ins>`, `<s>`, `<strike>`, `<del>`
- `<tg-spoiler>` and `<span class="tg-spoiler">`
- `<a href="...">`
- `<code>`
- `<pre><code class="language-...">...</code></pre>`
- `<blockquote>` and `<blockquote expandable>`
- `<tg-emoji emoji-id="...">`
- `<tg-time unix="..." format="...">`; `format` is optional

Balda preserves `&lt;`, `&gt;`, `&amp;`, `&quot;`, decimal numeric entities,
and hex numeric entities. Arbitrary HTML such as `<div>` and `<script>`, event
handlers, styles, and `<tg-time datetime="...">` are not supported.

For an explicit rich-format rejection, Balda derives one deterministic plain
fallback: tags are removed, entities are decoded, and no `parse_mode` is sent.
The fallback therefore cannot expose active or literal unsafe HTML markup.

Example model output:

```html
<b>Build:</b> success.
Run <code>balda start</code>.

<pre><code class="language-bash">go test ./...</code></pre>
```

## Plain Text

Use `none` when the response must be delivered as literal text. Balda does not
interpret or escape Markdown or HTML and omits Telegram `parse_mode`.
