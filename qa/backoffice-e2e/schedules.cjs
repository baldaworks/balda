const { submit, cancelConfirmation } = require('./actions.cjs');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { chromium } = require('playwright');

const baseURL = process.argv[2];
assert.equal(new URL(baseURL).hostname, '127.0.0.1', 'browser gate requires an isolated loopback server');
const screenshotDir = process.env.BALDA_SCHEDULES_SCREENSHOTS;
if (screenshotDir) fs.mkdirSync(screenshotDir, { recursive: true });

async function screenshot(page, name, viewport) {
  if (!screenshotDir) return;
  const prefix = new URL(baseURL).pathname === '/' ? 'root' : 'base';
  await page.screenshot({ path: path.join(screenshotDir, `${prefix}-${name}-${viewport}.png`), fullPage: true });
}

async function assertNoDocumentOverflow(page, name) {
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  assert.ok(overflow <= 1, `${name} overflows the document horizontally by ${overflow}px`);
}

async function saveDestination(page) {
  const form = page.locator('form').filter({ has: page.getByRole('button', { name: 'Save destination', exact: true }) });
  const version = await form.locator('input[name=expected_version]').inputValue();
  const response = page.waitForResponse(r => r.request().method() === 'POST' && new URL(r.url()).pathname === new URL(page.url()).pathname);
  await page.getByRole('button', { name: 'Save destination', exact: true }).click();
  assert.equal((await response).status(), 204, 'alias save accepted');
  await page.waitForFunction(old => { const field = document.querySelector('form input[name=expected_version]'); return field && field.value !== old; }, version);
}

async function runSchedule(page) {
  const selector = '[aria-label^="Schedule run history"] tbody tr a';
  const before = await page.evaluate(selector => document.querySelector(selector)?.getAttribute('href') || null, selector);
  await submit(page, page.getByRole('button', { name: 'Run now', exact: true }));
  await page.waitForFunction(({ selector, before }) => {
    const href = document.querySelector(selector)?.getAttribute('href');
    return href && href !== before;
  }, { selector, before });
}

async function textLineCount(locator) {
  return locator.evaluate(element => {
    const text = [...element.childNodes].find(node => node.nodeType === Node.TEXT_NODE);
    const range = document.createRange();
    range.selectNodeContents(text);
    return range.getClientRects().length;
  });
}

(async () => {
  const browser = await chromium.launch({ headless: true });
  try {
    for (const viewport of [{ width: 1440, height: 900, name: 'desktop' }, { width: 390, height: 844, name: 'mobile' }]) {
      const context = await browser.newContext({ viewport });
      const page = await context.newPage();
      const errors = [];
      page.on('pageerror', error => errors.push(error.message));
      await page.goto(`${baseURL}/login`);
      await page.getByLabel(/^Username(?: \(required\))?$/).fill('administrator');
      await page.getByLabel(/^Password(?: \(required\))?$/).fill('correct horse battery staple');
      await Promise.all([page.waitForURL(`${baseURL}/overview`), page.getByRole('button', { name: 'Sign in', exact: true }).click()]);

      const inventory = await page.goto(`${baseURL}/schedules`);
      assert.equal(inventory.status(), 200);
      assert.equal(await page.locator('h1').textContent(), 'Schedules');
      await page.getByRole('columnheader', { name: 'Report to', exact: true }).waitFor();
      const managedLink = page.getByRole('link', { name: 'daily-summary' });
      const configuredLink = page.getByRole('link', { name: 'config:morning' });
      await managedLink.waitFor();
      await configuredLink.waitFor();
      const managedRow = page.locator('.schedule-inventory-table tbody tr').filter({ has: managedLink });
      assert.equal((await managedRow.locator('td').nth(2).textContent()).trim(), 'telegram:9001:0');
      await assertNoDocumentOverflow(page, `${viewport.name} inventory`);
      await screenshot(page, 'inventory', viewport.name);
      if (viewport.name === 'mobile') {
        const source = page.locator('table tbody tr').first().locator('td').nth(1);
        assert.equal(await textLineCount(source), 1, 'source label wraps on a narrow inventory');
        assert.equal(await textLineCount(configuredLink), 1, 'short schedule ID wraps on a narrow inventory');
        const table = page.getByRole('region', { name: /Schedules table/ });
        assert.ok(await table.evaluate(element => element.scrollWidth > element.clientWidth), 'mobile inventory table does not scroll');
        await table.evaluate(element => { element.scrollLeft = element.scrollWidth - element.clientWidth; });
        await screenshot(page, 'inventory-right', viewport.name);
      }

      await Promise.all([page.waitForURL(`${baseURL}/schedules/daily-summary`), managedLink.click()]);
      await page.getByRole('heading', { name: 'Edit schedule' }).waitFor();
      assert.equal(await page.getByLabel('Report to (optional)').inputValue(), 'telegram:9001:0');
      await page.waitForFunction(() => document.querySelector('.app-sidebar [data-nav-link][href$="/schedules"]')?.getAttribute('aria-current') === 'page');
      await runSchedule(page);
      await page.getByRole('region', { name: /Schedule run history/ }).locator('tbody tr').first().getByText('Queued', { exact: true }).waitFor();
      await page.getByRole('region', { name: /Schedule run history/ }).locator('tbody tr a').first().click();
      await page.getByRole('heading', { name: 'Run detail · Queued' }).waitFor();
      await page.getByText("Summarize yesterday's work", { exact: true }).last().waitFor();
      await page.getByText('No output recorded yet.').waitFor();
      await assertNoDocumentOverflow(page, `${viewport.name} managed detail`);
      await screenshot(page, 'managed', viewport.name);
      if (viewport.name === 'mobile') {
        const table = page.getByRole('region', { name: /Schedule run history/ });
        const trigger = table.locator('tbody td').nth(1);
        assert.equal(await textLineCount(trigger), 1, 'run trigger wraps on a narrow history');
        assert.ok(await table.evaluate(element => element.scrollWidth > element.clientWidth), 'mobile history table does not scroll');
        await table.evaluate(element => { element.scrollLeft = element.scrollWidth - element.clientWidth; });
        await screenshot(page, 'managed-history-right', viewport.name);
      }

      await Promise.all([page.waitForURL(`${baseURL}/schedules`), page.getByRole('link', { name: 'All schedules' }).click()]);
      if (viewport.name === 'desktop') {
        const aliasInventory = await page.goto(`${baseURL}/aliases`);
        assert.equal(aliasInventory.status(), 200);
        await page.getByRole('link', { name: 'Add alias' }).click();
        await page.getByLabel('Alias name').fill('main_chat');
        await page.getByLabel('Public locator').fill('telegram:9001:0');
        await Promise.all([page.waitForURL(`${baseURL}/aliases/main_chat`), page.getByRole('button', { name: 'Create alias' }).click()]);
        await page.goto(`${baseURL}/schedules`);
      }
      await Promise.all([page.waitForURL(`${baseURL}/schedules?new=1`), page.getByRole('link', { name: 'Add schedule' }).click()]);
      assert.equal(await page.locator('h1').textContent(), 'Add schedule');
      assert.equal(await page.getByLabel('Report to (optional)').inputValue(), '');
      const newID = `weekly-review-${viewport.name}`;
      await page.getByLabel('Schedule ID').fill(newID);
      await page.getByLabel('Cron (UTC)').fill('0 9 * * 1');
      if (viewport.name === 'desktop') {
        await page.getByLabel('Report to (optional)').fill('main_chat');
      }
      await page.getByLabel('Content').fill('Prepare weekly review');
      await assertNoDocumentOverflow(page, `${viewport.name} creation form`);
      await screenshot(page, 'create', viewport.name);
      await Promise.all([page.waitForURL(`${baseURL}/schedules/${newID}`), page.getByRole('button', { name: 'Create schedule' }).click()]);
      await page.getByRole('heading', { name: 'Edit schedule' }).waitFor();
      if (viewport.name === 'desktop') {
        assert.equal(await page.getByLabel('Report to (optional)').inputValue(), 'main_chat');
        await runSchedule(page);
        await page.getByRole('region', { name: /Schedule run history/ }).locator('tbody tr a').first().click();
        await page.getByText('telegram:9001:0', { exact: true }).waitFor();
        const firstRunURL = page.url();
        assert.ok(new URL(firstRunURL).searchParams.has('run_id'), 'first run has a durable detail URL');
        await page.goto(`${baseURL}/aliases/main_chat`);
        await page.getByLabel('Public locator').fill('telegram:9002:0');
        await saveDestination(page);
        assert.equal(await page.getByLabel('Public locator').inputValue(), 'telegram:9002:0');
        await page.goto(`${baseURL}/schedules/${newID}`);
        assert.equal(await page.getByLabel('Report to (optional)').inputValue(), 'main_chat');
        await runSchedule(page);
        await page.getByRole('region', { name: /Schedule run history/ }).locator('tbody tr a').first().click();
        await page.getByText('telegram:9002:0', { exact: true }).waitFor();
        await page.goto(firstRunURL);
        await page.getByText('telegram:9001:0', { exact: true }).waitFor();
      }
      await screenshot(page, 'created', viewport.name);
      await submit(page, page.getByRole('button', { name: 'Disable schedule', exact: true }));
      await page.getByRole('button', { name: 'Enable schedule', exact: true }).waitFor();
      let manualRequests = 0;
      const observeManual = request => {
        if (request.method() === 'POST' && new URL(request.url()).pathname.endsWith(`/schedules/${newID}/runs`)) manualRequests++;
      };
      page.on('request', observeManual);
      const run = page.getByRole('button', { name: 'Run now', exact: true });
      await cancelConfirmation(page, run);
      assert.equal(manualRequests, 0, 'cancelled disabled run sends no mutation');
      const acceptedRun = page.waitForResponse(response => response.request().method() === 'POST' && new URL(response.url()).pathname.endsWith(`/schedules/${newID}/runs`));
      await runSchedule(page);
      assert.equal((await acceptedRun).status(), 204);
      assert.equal(manualRequests, 1, 'confirmed disabled schedule queues once');
      page.off('request', observeManual);
      await submit(page, page.getByRole('button', { name: 'Enable schedule', exact: true }));
      await page.getByRole('button', { name: 'Disable schedule', exact: true }).waitFor();


      await Promise.all([page.waitForURL(`${baseURL}/schedules`), page.getByRole('link', { name: 'All schedules' }).click()]);
      const createdRow = page.locator('.schedule-inventory-table tbody tr').filter({ has: page.getByRole('link', { name: newID }) });
      assert.equal((await createdRow.locator('td').nth(2).textContent()).trim(), viewport.name === 'desktop' ? 'main_chat' : 'None');
      await Promise.all([page.waitForURL(`${baseURL}/schedules/config:morning`), page.getByRole('link', { name: 'config:morning' }).click()]);
      await page.getByRole('heading', { name: 'Configuration schedule' }).waitFor();
      assert.equal(await page.getByRole('button', { name: 'Save schedule' }).count(), 0);
      await runSchedule(page);
      await page.getByRole('region', { name: /Schedule run history/ }).locator('tbody tr').first().getByText('Queued', { exact: true }).waitFor();
      await assertNoDocumentOverflow(page, `${viewport.name} configured detail`);
      await screenshot(page, 'configured', viewport.name);

      assert.deepEqual(errors, [], `${viewport.name} browser errors`);
      await context.close();
    }
    console.log('Authenticated Schedules create, manual-run and history flow passed at desktop and mobile widths');
  } finally {
    await browser.close();
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
