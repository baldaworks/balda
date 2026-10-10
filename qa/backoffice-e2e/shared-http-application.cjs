const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const fs = require('node:fs');
const { chromium } = require('playwright');

const origin = process.argv[2];
const phase = process.argv[3];
const secretPath = process.argv[4];
assert.equal(new URL(origin).hostname, '127.0.0.1');
assert.ok(phase === 'initial' || phase === 'restart');
const browserRoot = `${origin}/balda/backoffice`;

async function post(path, body, headers = {}) {
  return fetch(`${origin}${path}`, { method: 'POST', body, headers });
}

async function login(page) {
  await page.goto(`${browserRoot}/login`);
  await page.getByLabel(/^Username(?: \(required\))?$/).fill('superuser');
  await page.getByLabel(/^Password(?: \(required\))?$/).fill('correct horse battery staple');
  await Promise.all([
    page.waitForURL(`${browserRoot}/overview`),
    page.getByRole('button', { name: 'Sign in', exact: true }).click(),
  ]);
}

async function signedSlackPost() {
  const body = JSON.stringify({ type: 'url_verification', challenge: 'shared-listener-challenge' });
  const timestamp = Math.floor(Date.now() / 1000).toString();
  const signature = `v0=${crypto.createHmac('sha256', 'slack-signing-secret')
    .update(`v0:${timestamp}:${body}`).digest('hex')}`;
  const response = await post('/balda/gateway/slack/events', body, {
    'X-Slack-Request-Timestamp': timestamp, 'X-Slack-Signature': signature,
  });
  assert.equal(response.status, 200);
  assert.equal(await response.text(), 'shared-listener-challenge');
}

(async () => {
  const browser = await chromium.launch({ headless: true });
  try {
    const context = await browser.newContext({ javaScriptEnabled: true });
    const page = await context.newPage();
    const unauthenticated = await context.request.get(`${browserRoot}/webhooks`, { maxRedirects: 0 });
    assert.notEqual(unauthenticated.status(), 200, 'management requires a browser session');
    await login(page);

    const configured = await page.goto(`${browserRoot}/webhooks/configured`);
    assert.equal(configured.status(), 200);
    await page.getByText(`${origin}/legacy/configured`, { exact: true }).first().waitFor();
    const legacy = await page.goto(`${browserRoot}/webhooks/legacy`);
    assert.equal(legacy.status(), 200);
    assert.equal(await page.getByLabel('Webhook URL').inputValue(), `${origin}/legacy/orders`);

    const secret = phase === 'initial' ? null : fs.readFileSync(secretPath, 'utf8');
    if (phase === 'initial') {
      await page.goto(`${browserRoot}/webhooks?new=1`);
      await page.getByLabel('Route name').fill('orders');
      await page.getByLabel('Prompt template').fill('New: {{.RawBody}}');
      await page.getByRole('button', { name: 'Create webhook' }).click();
      await page.getByRole('heading', { name: 'Webhook secret · shown once' }).waitFor();
      const createdSecret = await page.getByLabel('Generated secret').inputValue();
      assert.ok(createdSecret.length >= 32);
      fs.writeFileSync(secretPath, createdSecret, { mode: 0o600, flag: 'wx' });
      await page.goto(`${browserRoot}/webhooks/orders`);
      assert.equal(await page.getByLabel('Webhook URL').inputValue(), `${origin}/balda/webhooks/orders`);
      assert.equal((await post('/balda/webhooks/orders', 'bad', { 'X-Balda-Webhook-Secret': 'wrong' })).status, 401);
      assert.equal((await post('/balda/webhooks/orders', 'first input', {
        'X-Balda-Webhook-Secret': createdSecret, 'X-Request-Id': 'shared-first',
      })).status, 202);
      await page.reload();
      await page.getByRole('region', { name: /Webhook request history/ }).locator('tbody tr a').first().click();
      await page.getByText('Processed: New: first input', { exact: true }).first().waitFor();
      await page.getByLabel('Prompt template').fill('Edited: {{.RawBody}}');
      await Promise.all([
        page.waitForResponse(response => response.url().endsWith('/webhooks/orders') && response.request().method() === 'POST'),
        page.getByRole('button', { name: 'Save webhook' }).click(),
      ]);
      await page.reload();
      assert.equal(await page.getByLabel('Prompt template').inputValue(), 'Edited: {{.RawBody}}');
      assert.equal((await post('/balda/webhooks/orders', 'edited input', {
        'X-Balda-Webhook-Secret': createdSecret, 'X-Request-Id': 'shared-edited',
      })).status, 202);
      await page.reload();
      await page.getByRole('region', { name: /Webhook request history/ }).locator('tbody tr a').first().click();
      await page.getByText('Processed: Edited: edited input', { exact: true }).first().waitFor();
      const csrfDenied = await page.evaluate(async path => (await fetch(path, {
        method: 'POST', credentials: 'same-origin',
        headers: { 'Content-Type': 'application/x-www-form-urlencoded' }, body: 'prompt_template=blocked',
      })).status, '/balda/backoffice/webhooks/orders');
      assert.equal(csrfDenied, 403);
      await page.getByLabel(/Confirm disabling this webhook route/).check();
      await page.getByRole('button', { name: 'Disable webhook' }).click();
      await page.getByRole('button', { name: 'Enable webhook' }).waitFor();
      assert.equal((await post('/balda/webhooks/orders', 'disabled', {
        'X-Balda-Webhook-Secret': createdSecret,
      })).status, 404);
      await page.getByLabel(/Confirm enabling this webhook route/).check();
      await page.getByRole('button', { name: 'Enable webhook' }).click();
      await page.getByRole('button', { name: 'Disable webhook' }).waitFor();
      assert.equal((await post('/balda/webhooks/orders', 'reenabled', {
        'X-Balda-Webhook-Secret': createdSecret, 'X-Request-Id': 'shared-reenabled',
      })).status, 202);
    } else {
      const detail = await page.goto(`${browserRoot}/webhooks/orders`);
      assert.equal(detail.status(), 200);
      assert.equal(await page.getByLabel('Webhook URL').inputValue(), `${origin}/balda/webhooks/orders`);
      assert.equal((await post('/balda/webhooks/orders', 'after restart', {
        'X-Balda-Webhook-Secret': secret, 'X-Request-Id': 'shared-after-restart',
      })).status, 202);
      await page.reload();
      await page.getByRole('region', { name: /Webhook request history/ }).locator('tbody tr a').first().click();
      await page.getByText('Processed: Edited: after restart', { exact: true }).first().waitFor();
    }

    assert.equal((await post('/legacy/orders', 'legacy input', {
      'X-Balda-Webhook-Secret': 'legacy-secret', 'X-Request-Id': `legacy-${phase}`,
    })).status, 202);
    assert.equal((await post('/legacy/configured', 'configured input', {
      'X-Configured-Secret': 'configured-secret', 'X-Request-Id': `configured-${phase}`,
    })).status, 202);
    for (const [name, output] of [
      ['legacy', 'Processed: Legacy: legacy input'],
      ['configured', 'Processed: Configured: configured input'],
    ]) {
      await page.goto(`${browserRoot}/webhooks/${name}`);
      await page.getByRole('region', { name: /Webhook request history/ }).locator('tbody tr a').first().click();
      await page.getByText(output, { exact: true }).first().waitFor();
    }
    assert.equal((await post('/balda/webhooks/unknown', 'unknown')).status, 404);
    assert.equal((await post('/balda/gateway/webhooks/orders', 'wrong area')).status, 404);
    assert.equal((await post('/balda/gateway/slack/events', '{}', {
      'X-Slack-Request-Timestamp': Math.floor(Date.now() / 1000).toString(), 'X-Slack-Signature': 'v0=invalid',
    })).status, 401);
    await signedSlackPost();
    const zulipPayload = token => JSON.stringify({ token, data: 'hello',
      message: { id: 100, sender_id: 7, sender_email: 'alice@example.com', type: 'private', content: 'hello' } });
    assert.equal((await post('/balda/gateway/zulip/webhook', zulipPayload('wrong'))).status, 401);
    assert.equal((await post('/balda/gateway/zulip/webhook', zulipPayload('zulip-secret'))).status, 200);
    const mattermostPayload = token => new URLSearchParams({ token, command: '/locator',
      channel_id: 'channel-1', user_id: 'user-1', trigger_id: `shared-${phase}` }).toString();
    assert.equal((await post('/balda/gateway/mattermost/commands', mattermostPayload('wrong'))).status, 401);
    assert.equal((await post('/balda/gateway/mattermost/commands', mattermostPayload('mattermost-secret'))).status, 200);
    assert.equal((await post('/balda/gateway/telegram/webhook', '{"update_id":1}')).status, 401);
    assert.equal((await post('/balda/gateway/telegram/webhook', '{"update_id":1}', {
      'X-Telegram-Bot-Api-Secret-Token': 'telegram-secret',
    })).status, 200);
    await context.close();
    console.log(`Shared HTTP ${phase} application E2E passed on one local listener`);
  } finally {
    await browser.close();
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
