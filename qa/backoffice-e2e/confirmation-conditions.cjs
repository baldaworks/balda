const assert = require('node:assert/strict');
const { chromium } = require('playwright');
(async () => {
  const browser = await chromium.launch({ headless: true });
  try {
    const page = await browser.newPage();
    const origin = process.argv[2], base = process.argv[3], requests = [];
    await page.route('**/*', async route => {
      if (route.request().method() !== 'POST') return route.continue();
      requests.push(new URLSearchParams(route.request().postData()));
      await route.fulfill({ status: 204 });
    });
    for (const [role, status, confirm] of [
      ['operator', 'active', false], ['administrator', 'active', true], ['operator', 'disabled', true],
    ]) {
      await page.goto(`${origin}${base}/qa/ui/access`);
      await page.getByText('Edit profile and access', { exact: true }).click();
      const form = page.locator('form').filter({ has: page.getByRole('button', { name: 'Save user', exact: true }) });
      await form.locator('[name=role]').selectOption(role);
      await form.locator('[name=status]').selectOption(status);
      await form.getByRole('button', { name: 'Save user', exact: true }).click();
      const modal = page.getByRole('dialog');
      assert.equal(await modal.isVisible(), confirm, `${role}/${status} exact capability consent`);
      if (confirm) await modal.locator('[data-confirm-accept]').click();
      await page.waitForTimeout(50);
      assert.equal(requests.length, 1);
      assert.equal(requests[0].get('confirm_bot_impact'), confirm ? 'yes' : null);
      requests.length = 0;
    }
    for (const [route, role, status] of [['bindings-disabled', 'administrator', 'disabled'], ['access-primary', 'operator', 'active']]) {
      await page.goto(`${origin}${base}/qa/ui/${route}`);
      await page.getByText('Edit profile and access', { exact: true }).click();
      await page.locator('#role').selectOption(role);
      await page.locator('#status').selectOption(status);
      await page.getByRole('button', { name: 'Save user', exact: true }).click();
      await page.waitForTimeout(50);
      assert.equal(await page.getByRole('dialog').isVisible(), false, 'unchanged none capability stays unconfirmed');
      assert.equal(requests.at(-1).get('confirm_bot_impact'), null);
      requests.length = 0;
    }
    async function checkAction(route, button, expected, prepare, field, value = 'yes') {
      await page.goto(`${origin}${base}/qa/ui/${route}`);
      if (prepare) await prepare();
      const response = page.waitForResponse(r => r.request().method() === 'POST');
      await page.getByRole('button', { name: button, exact: true }).click();
      const dialog = page.getByRole('dialog');
      assert.equal(await dialog.isVisible(), expected, `${route} ${button} condition`);
      if (expected) {
        await dialog.getByRole('button', { name: 'Cancel', exact: true }).click();
        assert.equal(requests.length, 0, 'cancel has no request');
        await page.getByRole('button', { name: button, exact: true }).click();
        await dialog.locator('[data-confirm-accept]').click();
      }
      await response;
      assert.equal(requests.length, 1);
      assert.equal(requests[0].get(field), expected ? value : null);
      if (route === 'account-2fa-enabled') assert.equal(requests[0].get('password'), 'synthetic current password', 'native MFA preserves entered password');
      requests.length = 0;
    }
    for (const [route, expected] of [['access', false], ['access-primary', true]]) {
      await checkAction(route, 'Reset credential', expected, async () => {
        await page.getByText('Reset temporary password', { exact: true }).click();
        await page.getByLabel('New temporary password').fill('synthetic temporary password');
      }, 'confirm_current');
    }
    for (const [route, expected] of [['webhooks-managed', false], ['webhooks-disabled', true]]) {
      await checkAction(route, 'Send test POST', expected, null, 'confirm_disabled');
    }
    for (const button of ['Replace passkey', 'Disable 2FA']) {
      await checkAction('account-2fa-enabled', button, true, async () => {
        await page.locator(button === 'Replace passkey' ? '#replace-mfa-password' : '#disable-mfa-password').fill('synthetic current password');
      }, 'confirm', 'on');
    }
    for (const current of [false, true]) {
      await page.goto(`${origin}${base}/qa/ui/account`);
      const form = page.locator(`form[action$="/${current ? 'family-demo' : 'family-other'}/revoke"]`);
      const response = page.waitForResponse(r => r.request().method() === 'POST');
      await form.getByRole('button', { name: 'End session', exact: true }).click();
      assert.equal(await page.getByRole('dialog').isVisible(), current);
      if (current) await page.getByRole('dialog').locator('[data-confirm-accept]').click();
      await response;
      assert.equal(requests[0].get('confirm_current'), current ? 'yes' : null);
      requests.length = 0;
    }
    const fs = require('node:fs'), path = require('node:path');
    const directory = process.env.BALDA_CONFIRMATION_MIGRATION_SCREENSHOTS || '/tmp/balda-jta1-migration-screens';
    fs.mkdirSync(directory, { recursive: true });
    for (const width of [1440, 390]) {
      await page.setViewportSize({ width, height: width === 390 ? 844 : 900 });
      for (const [route, button] of [['plugins/installed/demo', 'Disable plugin'], ['skills/managed/managed-synthetic', 'Remove skill'], ['providers/managed', 'Disable provider'], ['hooks/guard', 'Delete hook'], ['memory-delete', 'Delete fact']]) {
        await page.goto(`${origin}${base}/qa/ui/${route}`);
        await page.getByRole('button', { name: button, exact: true }).click();
        const dialog = page.getByRole('dialog');
        await dialog.waitFor({ state: 'visible' });
        assert.ok(await dialog.evaluate(e => { const r = e.querySelector('.modal-dialog').getBoundingClientRect(); return r.left >= 0 && r.right <= innerWidth; }), 'dialog fits');
        await page.screenshot({ path: path.join(directory, `${base ? 'prefixed' : 'root'}-${width}-${route.replaceAll('/', '-')}.png`) });
        await dialog.getByRole('button', { name: 'Cancel', exact: true }).click();
      }
    }
    console.log('Access capability, own credential/family, disabled webhook, native MFA and scoped dialog layout checks passed');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
