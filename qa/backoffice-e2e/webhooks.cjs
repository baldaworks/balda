const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { chromium } = require('playwright');

const baseURL = process.argv[2];
const webhookURL = process.argv[3];
assert.equal(new URL(baseURL).hostname, '127.0.0.1');
assert.equal(new URL(webhookURL).hostname, '127.0.0.1');
const screenshotDir = process.env.BALDA_WEBHOOKS_RUNTIME_SCREENSHOTS;
if (screenshotDir) fs.mkdirSync(screenshotDir, { recursive: true });

async function capture(page, name, viewport) {
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  assert.ok(overflow <= 1, `${name} overflows by ${overflow}px`);
  if (!screenshotDir) return;
  const prefix = new URL(baseURL).pathname === '/' ? 'root' : 'base';
  await page.screenshot({ path: path.join(screenshotDir, `${prefix}-${name}-${viewport}.png`), fullPage: true });
}

async function signIn(page, username) {
  await page.goto(`${baseURL}/login`);
  await page.getByLabel(/^Username(?: \(required\))?$/).fill(username);
  await page.getByLabel(/^Password(?: \(required\))?$/).fill('correct horse battery staple');
  await Promise.all([page.waitForURL(`${baseURL}/overview`), page.getByRole('button', { name: 'Sign in', exact: true }).click()]);
}

async function externalPost(pathname, secret, body, requestID) {
  const headers = { 'X-Request-Id': requestID, 'Content-Type': 'text/plain' };
  if (secret) headers['X-Balda-Webhook-Secret'] = secret;
  const response = await fetch(`${webhookURL}${pathname}`, { method: 'POST', headers, body });
  return { status: response.status, body: await response.json() };
}

(async () => {
  const browser = await chromium.launch({ headless: true });
  try {
    if (process.argv[4] === 'restart') {
      const context = await browser.newContext();
      const page = await context.newPage();
      await signIn(page, 'administrator');
      const inventory = await page.goto(`${baseURL}/webhooks`);
      assert.equal(inventory.status(), 200);
      await page.getByRole('link', { name: 'configured' }).waitFor();
      for (const name of ['browser-desktop', 'browser-mobile']) {
        const detail = await page.goto(`${baseURL}/webhooks/${name}`);
        assert.equal(detail.status(), 200);
        await page.getByRole('heading', { name: 'Archived webhook' }).waitFor();
        assert.ok((await page.getByRole('region', { name: /Webhook request history/ }).locator('tbody tr').count()) > 0);
        assert.equal(await page.getByRole('button', { name: 'Save webhook' }).count(), 0);
      }
      const persistent = await page.goto(`${baseURL}/webhooks/persistent`);
      assert.equal(persistent.status(), 200);
      await page.getByRole('heading', { name: 'Edit webhook route' }).waitFor();
      assert.equal(await page.getByLabel('Generated secret').count(), 0);
      assert.equal(await page.getByLabel('Path').inputValue(), '/hooks/persistent');
      const admitted = await externalPost('/hooks/persistent', process.env.BALDA_WEBHOOK_RESTART_SECRET,
        'after restart', 'persistent-after-restart');
      assert.equal(admitted.status, 202);
      assert.equal(admitted.body.accepted, true);
      await page.reload();
      await page.getByRole('region', { name: /Webhook request history/ }).locator('tbody tr a').first().click();
      await page.getByRole('heading', { name: 'Request detail' }).waitFor();
      await page.getByText('after restart', { exact: true }).waitFor();
      await page.getByText('Processed: Persistent: after restart', { exact: true }).waitFor();
      await capture(page, 'persistent-after-restart', 'desktop');
      await context.close();
      console.log('Webhooks live route admits and history remains readable after SQLite/application restart');
      return;
    }
    for (const viewport of [{ width: 1440, height: 900, name: 'desktop' }, { width: 390, height: 844, name: 'mobile' }]) {
      const context = await browser.newContext({ viewport });
      const page = await context.newPage();
      const errors = [];
      page.on('pageerror', error => errors.push(error.message));
      await signIn(page, 'administrator');

      let response = await page.goto(`${baseURL}/webhooks`);
      assert.equal(response.status(), 200);
      await page.getByRole('heading', { name: 'Webhooks', exact: true }).waitFor();
      await page.getByRole('columnheader', { name: 'Report to' }).waitFor();
      await page.getByRole('link', { name: 'configured' }).waitFor();
      await capture(page, 'inventory', viewport.name);
      await page.getByRole('link', { name: 'configured' }).click();
      await page.getByRole('heading', { name: 'Configuration webhook' }).waitFor();
      assert.equal(await page.getByRole('button', { name: 'Save webhook' }).count(), 0);
      assert.equal(await page.getByRole('button', { name: 'Rotate secret' }).count(), 0);
      await capture(page, 'configured', viewport.name);

      let result = await externalPost('/hooks/configured', '', 'denied', `configured-denied-${viewport.name}`);
      assert.equal(result.status, 401);
      result = await externalPost('/hooks/configured', 'wrong', 'denied', `configured-wrong-${viewport.name}`);
      assert.equal(result.status, 401);
      const configuredResponse = await fetch(`${webhookURL}/hooks/configured`, { method: 'POST',
        headers: { 'X-Configured-Secret': 'synthetic-config-secret', 'X-Request-Id': `configured-${viewport.name}` }, body: 'config input' });
      assert.equal(configuredResponse.status, 202);
      await page.reload();
      await page.getByRole('region', { name: /Webhook request history/ }).getByText('External').first().waitFor();
      await page.getByLabel('Request body').fill('config test input');
      await page.getByRole('button', { name: 'Send test POST' }).click();
      await page.getByRole('heading', { name: 'Request detail' }).waitFor();
      await page.getByText('config test input', { exact: true }).waitFor();
      await page.getByText('Processed: Configured: config test input', { exact: true }).waitFor();
      await page.getByRole('region', { name: /Webhook request history/ }).getByText('Test POST').first().waitFor();
      assert.equal(await page.getByLabel('Generated secret').count(), 0);
      await capture(page, 'configured-test-history', viewport.name);

      const routeName = `browser-${viewport.name}`;
      await page.goto(`${baseURL}/webhooks?new=1`);
      await page.getByLabel('Route name').fill(routeName);
      await page.getByLabel('Path').fill(`/hooks/${routeName}`);
      await page.getByLabel('Prompt template').fill('Browser: {{.RawBody}}');
      if (viewport.name === 'desktop') await page.getByLabel('Report to (optional)').fill('telegram:9001:0');
      await capture(page, 'create', viewport.name);
      await page.getByRole('button', { name: 'Create webhook' }).click();
      await page.getByRole('heading', { name: 'Webhook secret · shown once' }).waitFor();
      const firstSecret = await page.getByLabel('Generated secret').inputValue();
      assert.ok(firstSecret.length >= 32);
      assert.ok(!page.url().includes(firstSecret));
      await page.goto(`${baseURL}/webhooks/${routeName}`);
      assert.equal(await page.getByLabel('Generated secret').count(), 0);
      await page.getByRole('heading', { name: 'Edit webhook route' }).waitFor();
      await capture(page, 'edit', viewport.name);

      result = await externalPost(`/hooks/${routeName}`, '', 'unauthorized', `${routeName}-denied`);
      assert.equal(result.status, 401);
      result = await externalPost(`/hooks/${routeName}`, firstSecret, 'first input', `${routeName}-first`);
      assert.equal(result.status, 202);
      assert.equal(result.body.accepted, true);
      const firstJobID = result.body.job_id;
      const duplicate = await externalPost(`/hooks/${routeName}`, firstSecret, 'changed duplicate input', `${routeName}-first`);
      assert.equal(duplicate.status, 202);
      assert.equal(duplicate.body.duplicate, true);
      assert.equal(duplicate.body.job_id, firstJobID);
      await page.reload();
      const history = page.getByRole('region', { name: /Webhook request history/ });
      assert.equal(await history.locator('tbody tr').count(), 1, 'rejected and duplicate POST must not create history');
      await history.locator('tbody tr a').first().click();
      await page.getByRole('heading', { name: 'Request detail' }).waitFor();
      await page.getByText('first input', { exact: true }).waitFor();
      await page.getByText('Processed: Browser: first input', { exact: true }).first().waitFor();
      if (viewport.name === 'desktop') {
        const detail = page.locator('section').filter({ has: page.getByRole('heading', { name: 'Request detail' }) });
        await detail.getByText('telegram:9001:0', { exact: true }).waitFor();
        await detail.getByText('sent', { exact: true }).waitFor();
        await detail.getByRole('heading', { name: 'Delivery payload' }).waitFor();
      }
      assert.ok(!(await page.locator('main').textContent()).includes(firstSecret));
      await capture(page, 'external-history', viewport.name);

      await page.getByLabel('Request body').fill('test input');
      await page.getByRole('button', { name: 'Send test POST' }).click();
      await page.getByRole('heading', { name: 'Request detail' }).waitFor();
      await page.getByText('test input', { exact: true }).waitFor();
      await page.getByText('Processed: Browser: test input', { exact: true }).first().waitFor();
      await page.getByRole('region', { name: /Webhook request history/ }).getByText('Test').waitFor();
      await capture(page, 'test-history', viewport.name);

      await page.getByLabel('Prompt template').fill('Edited: {{.RawBody}}');
      await Promise.all([
        page.waitForResponse(response => response.url().endsWith(`/webhooks/${routeName}`) && response.request().method() === 'POST'),
        page.getByRole('button', { name: 'Save webhook' }).click(),
      ]);
      await page.reload();
      assert.equal(await page.getByLabel('Prompt template').inputValue(), 'Edited: {{.RawBody}}');
      result = await externalPost(`/hooks/${routeName}`, firstSecret, 'edited input', `${routeName}-edited`);
      assert.equal(result.status, 202);
      await page.reload();
      await page.getByRole('region', { name: /Webhook request history/ }).locator('tbody tr a').first().click();
      await page.getByRole('heading', { name: 'Request detail' }).waitFor();
      await page.getByText('Processed: Edited: edited input', { exact: true }).first().waitFor();

      await page.getByLabel(/Confirm replacing the current secret/).check();
      await page.getByRole('button', { name: 'Rotate secret' }).click();
      await page.getByRole('heading', { name: 'Webhook secret · shown once' }).waitFor();
      const rotatedSecret = await page.getByLabel('Generated secret').inputValue();
      assert.notEqual(rotatedSecret, firstSecret);
      result = await externalPost(`/hooks/${routeName}`, firstSecret, 'old secret', `${routeName}-old-secret`);
      assert.equal(result.status, 401);
      result = await externalPost(`/hooks/${routeName}`, rotatedSecret, 'new secret', `${routeName}-new-secret`);
      assert.equal(result.status, 202);
      await page.goto(`${baseURL}/webhooks/${routeName}`);
      assert.equal(await page.getByLabel('Generated secret').count(), 0);

      await page.getByLabel(/Confirm disabling this webhook route/).check();
      await page.getByRole('button', { name: 'Disable webhook' }).click();
      await page.getByRole('button', { name: 'Enable webhook' }).waitFor();
      result = await externalPost(`/hooks/${routeName}`, rotatedSecret, 'disabled', `${routeName}-disabled`);
      assert.equal(result.status, 404);
      await page.getByLabel(/Confirm enabling this webhook route/).check();
      await page.getByRole('button', { name: 'Enable webhook' }).click();
      await page.getByRole('button', { name: 'Disable webhook' }).waitFor();
      result = await externalPost(`/hooks/${routeName}`, rotatedSecret, 'reenabled', `${routeName}-reenabled`);
      assert.equal(result.status, 202);
      if (viewport.name === 'desktop') {
        for (let i = 0; i < 21; i++) {
          result = await externalPost(`/hooks/${routeName}`, rotatedSecret, `page input ${i}`, `${routeName}-page-${i}`);
          assert.equal(result.status, 202);
        }
        await page.reload();
        assert.equal(await page.getByRole('region', { name: /Webhook request history/ }).locator('tbody tr').count(), 20);
        await page.getByRole('link', { name: 'Older requests' }).click();
        await page.getByRole('region', { name: /Webhook request history/ }).locator('tbody tr a').first().waitFor();
        assert.ok((await page.getByRole('region', { name: /Webhook request history/ }).locator('tbody tr').count()) > 0);
        await page.goto(`${baseURL}/webhooks/${routeName}`);
      }

      // A same-origin administrator session without the CSRF field cannot mutate.
      const blocked = await page.evaluate(async currentPath => {
        const response = await fetch(currentPath, { method: 'POST', credentials: 'same-origin',
          headers: { 'Content-Type': 'application/x-www-form-urlencoded' }, body: 'path=%2Fhooks%2Fblocked' });
        return response.status;
      }, new URL(page.url()).pathname);
      assert.equal(blocked, 403);

      await page.getByLabel(/Confirm deleting this route/).check();
      const [deleteResponse] = await Promise.all([
        page.waitForResponse(response => response.url().endsWith(`/webhooks/${routeName}/delete`) && response.request().method() === 'POST'),
        page.getByRole('button', { name: 'Delete webhook' }).click(),
      ]);
      assert.equal(deleteResponse.status(), 204);
      await page.goto(`${baseURL}/webhooks/${routeName}`);
      await page.getByRole('heading', { name: 'Archived webhook' }).waitFor();
      await page.getByRole('region', { name: /Webhook request history/ }).locator('tbody tr a').first().waitFor();
      assert.equal(await page.getByRole('button', { name: 'Send test POST' }).count(), 0);
      result = await externalPost(`/hooks/${routeName}`, rotatedSecret, 'archived', `${routeName}-archived`);
      assert.equal(result.status, 404);
      await capture(page, 'archived', viewport.name);
      assert.deepEqual(errors, [], `${viewport.name} browser console errors`);
      await context.close();
    }
    const persistentContext = await browser.newContext();
    const persistentPage = await persistentContext.newPage();
    await signIn(persistentPage, 'administrator');
    await persistentPage.goto(`${baseURL}/webhooks?new=1`);
    await persistentPage.getByLabel('Route name').fill('persistent');
    await persistentPage.getByLabel('Path').fill('/hooks/persistent');
    await persistentPage.getByLabel('Prompt template').fill('Persistent: {{.RawBody}}');
    await persistentPage.getByRole('button', { name: 'Create webhook' }).click();
    await persistentPage.getByRole('heading', { name: 'Webhook secret · shown once' }).waitFor();
    const persistentSecret = await persistentPage.getByLabel('Generated secret').inputValue();
    assert.ok(persistentSecret.length >= 32);
    fs.writeFileSync(process.argv[5], persistentSecret, { mode: 0o600, flag: 'wx' });
    await persistentPage.goto(`${baseURL}/webhooks/persistent`);
    assert.equal(await persistentPage.getByLabel('Generated secret').count(), 0);
    await capture(persistentPage, 'persistent-before-restart', 'desktop');
    await persistentContext.close();
    const operatorContext = await browser.newContext();
    const operatorPage = await operatorContext.newPage();
    await signIn(operatorPage, 'operator');
    const denied = await operatorPage.goto(`${baseURL}/webhooks`);
    assert.equal(denied.status(), 403);
    await operatorContext.close();
    console.log('Authenticated Webhooks runtime E2E passed at desktop and mobile widths');
  } finally {
    await browser.close();
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
