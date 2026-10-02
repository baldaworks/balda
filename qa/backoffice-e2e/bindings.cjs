const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const { chromium } = require('playwright');
const [baseURL, slackURL] = process.argv.slice(2);
for (const url of [baseURL, slackURL]) assert.equal(new URL(url).hostname, '127.0.0.1', 'isolated fixture only');

async function consume(channel, payload, sender) {
  if (channel === 'slackagent') {
    const body = new URLSearchParams({ command: '/balda', text: `start ${payload}`, team_id: 'TQA', channel_id: `D${sender}`, user_id: `U${sender}` }).toString();
    const timestamp = Math.floor(Date.now() / 1000).toString();
    const signature = 'v0=' + crypto.createHmac('sha256', 'browser-fixture-signing-secret').update(`v0:${timestamp}:${body}`).digest('hex');
    const response = await fetch(`${slackURL}/slack/commands`, { method: 'POST', headers: { 'content-type': 'application/x-www-form-urlencoded', 'x-slack-request-timestamp': timestamp, 'x-slack-signature': signature }, body });
    assert.equal(response.status, 200, 'signed Slack receiver response');
    const text = await response.text();
    assert.equal(text.includes(payload), false, 'receiver must not echo invitation');
    return text.includes('Account connected.');
  }
  const response = await fetch(`${baseURL}/__fixture/consume`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ Channel: channel, Payload: payload, Sender: String(sender) }) });
  return response.status === 204;
}

(async () => {
  const browser = await chromium.launch({ headless: true });
  try {
    let caseID = 0;
    for (const width of [390, 768, 1024, 1440]) {
      for (const javaScriptEnabled of [true, false]) {
        const context = await browser.newContext({ javaScriptEnabled, viewport: { width, height: 900 }, permissions: ['clipboard-read', 'clipboard-write'] });
        const page = await context.newPage();
        const errors = [];
        const secrets = [];
        page.on('pageerror', error => errors.push(error.message));
        page.on('request', request => assert.equal(secrets.some(secret => request.url().includes(secret)), false, 'invitation cannot enter navigation URLs'));
        await page.goto(`${baseURL}/login`);
        await page.getByLabel(/^Username/).fill('administrator');
        await page.getByLabel(/^Password/).fill('correct horse battery staple');
        await Promise.all([page.waitForURL(`${baseURL}/overview`), page.getByRole('button', { name: 'Sign in', exact: true }).click()]);
        const target = javaScriptEnabled ? 'operator' : 'secondary';
        const detailURL = `${baseURL}/access/users/${target}`;
        for (const channel of ['telegram', 'slackagent', 'zulip', 'mattermost']) {
          const sender = ++caseID;
          await page.goto(detailURL);
          assert.equal(await page.locator('[data-binding-channel]').count(), 4);
          assert.equal(await page.locator('.viewer strong').textContent(), 'administrator');
          const panel = () => page.locator(`[data-binding-channel="${channel}"]`);
          async function issue(replace = false) {
            if (replace) { await panel().getByText('Replace invitation', { exact: true }).click(); await panel().locator('input[name=replace]').check(); }
            const [response] = await Promise.all([page.waitForResponse(response => response.url() === `${detailURL}/invitations/${channel}` && response.request().method() === 'POST'), panel().getByRole('button', { name: replace ? 'Generate replacement' : 'Generate invitation', exact: true }).click()]);
            assert.equal(response.status(), 200);
            assert.equal(response.headers()['cache-control'], 'no-store');
            const field = page.locator(`#binding-message-${channel}`);
            await field.waitFor();
            const value = await field.inputValue();
            assert.match(value, /^bind_[A-Za-z0-9_-]{32}$/);
            secrets.push(value);
            return value;
          }
          if (channel === 'telegram') {
            await panel().locator('input[name=expected_version]').evaluate(element => { element.value = '999999'; });
            const [stale] = await Promise.all([page.waitForResponse(response => response.url() === `${detailURL}/invitations/${channel}` && response.request().method() === 'POST'), panel().getByRole('button', { name: 'Generate invitation', exact: true }).click()]);
            assert.equal(stale.status(), 409, 'stale target retains conflict status');
            await panel().getByRole('alert').waitFor();
            await page.goto(detailURL);
          }
          let payload = await issue();
          if (javaScriptEnabled) {
            await panel().getByRole('button', { name: 'Copy message', exact: true }).click();
            await panel().getByText('Copied.', { exact: true }).waitFor();
            assert.equal(await page.evaluate(() => navigator.clipboard.readText()), payload);
          } else assert.equal(await page.locator(`#binding-message-${channel}`).getAttribute('readonly'), '', 'manual copy field');
          if (channel === 'mattermost') {
            assert.equal(await page.locator('#binding-command-mattermost').count(), 0, 'disabled slash receiver has DM fallback only');
            assert.equal((await page.locator('#binding-dm-mattermost').inputValue()).startsWith('/msg @fixture_bot '), true);
          }
          await page.goto(`${baseURL}/overview`);
          const nativeLoad = javaScriptEnabled ? null : page.waitForEvent('load');
          let nativeCacheMiss = false;
          try { await page.goBack(); } catch (error) {
            if (javaScriptEnabled || !error.message.includes('ERR_CACHE_MISS')) throw error;
            nativeCacheMiss = true;
          }
          if (nativeLoad) await nativeLoad;
          if (nativeCacheMiss) {
            // Native no-store POST history cannot recover the secret. Reopening
            // its URL as GET redirects to metadata-only user detail.
            await page.goto(`${detailURL}/invitations/${channel}`);
          }
          await page.waitForSelector(`[data-binding-channel="${channel}"]`);
          assert.equal(await page.locator('[data-binding-reveal]').count(), 0, 'history recovery must contain metadata only');
          assert.equal(secrets.some(secret => page.url().includes(secret)), false);
          const old = payload;
          payload = await issue(true);
          assert.equal(await consume(channel, old, sender), false, 'replaced credential rejected');
          if (channel === 'telegram') {
            await panel().locator('summary').filter({ hasText: 'Cancel invitation' }).click();
            await panel().locator('input[name=confirm_cancel]').check();
            await panel().getByRole('button', { name: 'Cancel invitation', exact: true }).click();
            await page.waitForFunction(() => !document.querySelector('[data-binding-channel="telegram"] input[name=replace]'));
            assert.equal(await consume(channel, payload, sender), false, 'cancelled credential rejected');
            payload = await issue();
          }
          assert.equal(await consume(channel, payload, sender), true, 'current verified sender connected');
          assert.equal(await consume(channel, payload, sender), false, 'replay denied');
          await page.getByRole('link', { name: 'Refresh bindings', exact: true }).click();
          await page.waitForFunction(channel => !document.querySelector(`[data-binding-channel="${channel}"] [data-binding-reveal]`), channel);
          await page.goto(detailURL); // Native recovery additionally verifies metadata-only GET.
          assert.equal(await panel().getByText('Waiting for confirmation', { exact: true }).count(), 0);
          assert.equal(await page.locator('[data-binding-reveal]').count(), 0);
          assert.equal(await page.locator('h1').evaluate(element => getComputedStyle(element).color), 'rgb(222, 226, 230)');
          assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
          if (javaScriptEnabled) {
            const storage = await page.evaluate(() => JSON.stringify({ local: { ...localStorage }, session: { ...sessionStorage } }));
            assert.equal(secrets.some(secret => storage.includes(secret)), false, 'credential excluded from storage/history cache');
          }
        }
        assert.deepEqual(errors, [], 'browser script errors');
        await context.close();
      }
    }
    console.log('Binding browser: four configured channels at 390/768/1024/1440px with JS and no-JS; selected operator/non-primary admin, issue/copy/history/replace/cancel/consume/replay/refresh passed; Slack uses signed concrete receiver, other channels use trusted proof fixtures.');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
