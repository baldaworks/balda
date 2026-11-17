const { cancelConfirmation } = require('./actions.cjs');
const assert = require('node:assert/strict');
const { chromium } = require('playwright');

const origin = process.argv[2];
const base = process.argv[3] || '';
const prefix = `${base}/qa/ui`;
const viewports = [
  { width: 1440, height: 900 },
  { width: 1024, height: 768 },
  { width: 768, height: 1024 },
  { width: 390, height: 844 },
];
const states = new Map([
  ['schedules', 200], ['schedules-empty', 200], ['schedules/new', 200],
  ['schedules/daily-summary', 200], ['schedules-disabled', 200], ['schedules-deleted', 200],
  ['schedules-all', 200], ['schedules/deleted-summary', 200],
  ['schedules-config', 200], ['schedules-history', 200], ['schedules-long', 200],
  ['schedules-invalid', 400], ['schedules-conflict', 409], ['schedules-unavailable', 503],
  ['aliases', 200], ['aliases-empty', 200], ['aliases/new', 200],
  ['aliases/main_chat', 200], ['aliases-long', 200], ['aliases-invalid', 400],
  ['aliases-conflict', 409], ['aliases-unavailable', 503],
]);

async function checkState(page, state, status, viewport) {
  const label = `${base || '/'} ${viewport.width} ${state}`;
  const response = await page.goto(`${origin}${prefix}/${state}`);
  assert.equal(response.status(), status, `${label} full-page status`);
  assert.equal(await page.locator('main#main-content').count(), 1, `${label} one main`);
  assert.equal(await page.locator('main').getAttribute('hx-history'), 'false', `${label} private history`);
  const family = state.startsWith('schedules') ? 'Schedules' : 'Aliases';
  assert.deepEqual(
    (await page.locator('.app-sidebar [data-nav-link][aria-current="page"]').allTextContents()).map(text => text.trim()),
    [family],
    `${label} active navigation`,
  );
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth <= 1), `${label} no document overflow`);
  for (const target of await page.locator('main a[href], main form[action]').evaluateAll(elements => elements.map(element => element.getAttribute('href') || element.getAttribute('action')))) {
    assert.ok(target.startsWith(`${prefix}/`), `${label} canonical target ${target}`);
  }
  const fragment = await page.request.get(`${origin}${prefix}/${state}`, { headers: { 'HX-Request': 'true', 'HX-Target': 'main-content' } });
  assert.equal(fragment.status(), status, `${label} fragment status`);
  const html = await fragment.text();
  assert.equal((html.match(/id="main-content"/g) || []).length, 1, `${label} fragment main`);
  assert.ok(!/<html|<body|class="app-sidebar"/.test(html), `${label} fragment excludes shell`);

  if (state === 'schedules') {
    assert.deepEqual(await page.getByRole('columnheader').allTextContents(), ['Schedule', 'Source', 'Report to', 'State', 'Next run', 'Last run']);
  } else if (state === 'schedules/new') {
    await page.getByLabel('Schedule ID').focus();
    await page.keyboard.press('Tab');
    assert.equal(await page.getByLabel('Cron (UTC)').evaluate(element => element === document.activeElement), true, `${label} keyboard reaches cron`);
  } else if (state === 'schedules/daily-summary') {
    assert.equal(await page.getByLabel('Report to (optional)').inputValue(), 'main_chat');
    assert.equal(await page.getByRole('region', { name: 'Schedule run history', exact: true }).locator('tbody tr').count(), 2);
    const deleteButton = page.getByRole('button', { name: 'Delete schedule', exact: true });
    const deleteForm = deleteButton.locator('xpath=ancestor::form');
    assert.equal(await deleteForm.getAttribute('data-confirm-title'), 'Delete schedule?', `${label} delete confirmation title`);
    assert.match(await deleteForm.getAttribute('data-confirm-message'), /Run history remains available/, `${label} retained-history consequence`);
    assert.equal(await deleteForm.locator('[data-confirm-consent]').isDisabled(), true, `${label} delete consent disabled before confirmation`);
    let deleteRequests = 0;
    const observeDelete = request => {
      if (request.method() === 'POST' && new URL(request.url()).pathname.endsWith('/schedules/daily-summary/delete')) deleteRequests++;
    };
    page.on('request', observeDelete);
    await deleteButton.click();
    const dialog = page.getByRole('dialog');
    await dialog.waitFor({ state: 'visible' });
    assert.equal(await dialog.getByRole('heading', { name: 'Delete schedule?', exact: true }).count(), 1, `${label} delete dialog heading`);
    assert.match(await dialog.textContent(), /daily-summary/, `${label} delete dialog names target`);
    assert.match(await dialog.textContent(), /Run history remains available/, `${label} delete dialog states consequence`);
    await dialog.getByRole('button', { name: 'Cancel', exact: true }).click();
    await dialog.waitFor({ state: 'hidden' });
    assert.equal(deleteRequests, 0, `${label} cancelled delete sends no mutation`);
    assert.equal(await deleteButton.evaluate(element => element === document.activeElement), true, `${label} cancel restores trigger focus`);
    page.off('request', observeDelete);
  } else if (state === 'schedules-disabled') {
    await cancelConfirmation(page, page.getByRole('button', { name: 'Run now', exact: true }));
  } else if (state === 'schedules-deleted') {
    assert.equal(await page.getByRole('link', { name: 'deleted-summary', exact: true }).count(), 1);
    assert.equal(await page.getByRole('link', { name: 'daily-summary', exact: true }).count(), 0);
  } else if (state === 'schedules-all') {
    assert.equal(await page.getByRole('link', { name: 'daily-summary', exact: true }).count(), 1);
    assert.equal(await page.getByRole('link', { name: 'deleted-summary', exact: true }).count(), 1);
  } else if (state === 'schedules/deleted-summary') {
    assert.equal(await page.getByRole('heading', { name: 'Archived schedule', exact: true }).count(), 1);
    assert.equal(await page.getByRole('button', { name: 'Run now', exact: true }).count(), 0);
  } else if (state === 'schedules-config') {
    assert.equal(await page.getByRole('heading', { name: 'Configuration schedule', exact: true }).count(), 1);
    assert.equal(await page.getByRole('button', { name: 'Save schedule', exact: true }).count(), 0);
  } else if (state === 'schedules-history') {
    assert.equal(await page.getByRole('heading', { name: 'Run detail · Succeeded', exact: true }).count(), 1);
    assert.equal(await page.getByText('Synthetic summary output.', { exact: true }).count(), 1);
  } else if (state === 'aliases') {
    assert.deepEqual(await page.getByRole('columnheader').allTextContents(), ['Alias', 'Current locator']);
  } else if (state === 'aliases/new') {
    await page.getByLabel('Alias name').focus();
    await page.keyboard.press('Tab');
    assert.equal(await page.getByLabel('Public locator').evaluate(element => element === document.activeElement), true, `${label} keyboard reaches locator`);
  } else if (state === 'aliases/main_chat') {
    await cancelConfirmation(page, page.getByRole('button', { name: 'Delete alias', exact: true }));
  }

  if (status >= 400) {
    assert.ok(await page.locator('[role="alert"]').count() >= 1, `${label} visible recovery feedback`);
    assert.equal(await page.locator('[role="alert"] h2').first().isVisible(), true, `${label} recovery heading`);
  }
}

async function checkNavigation(page, viewport) {
  await page.goto(`${origin}${prefix}/schedules`);
  let settled = page.evaluate(() => new Promise(resolve => document.addEventListener('htmx:afterSettle', resolve, { once: true })));
  await page.getByRole('navigation', { name: 'Lifecycle filter' }).getByRole('link', { name: 'Deleted', exact: true }).click();
  await settled;
  assert.equal(await page.getByRole('link', { name: 'deleted-summary', exact: true }).count(), 1, `${viewport.width} deleted filter`);
  settled = page.evaluate(() => new Promise(resolve => document.addEventListener('htmx:afterSettle', resolve, { once: true })));
  await page.getByRole('navigation', { name: 'Lifecycle filter' }).getByRole('link', { name: 'All', exact: true }).click();
  await settled;
  assert.equal(await page.getByRole('link', { name: 'daily-summary', exact: true }).count(), 1, `${viewport.width} all filter active row`);
  assert.equal(await page.getByRole('link', { name: 'deleted-summary', exact: true }).count(), 1, `${viewport.width} all filter deleted row`);
  settled = page.evaluate(() => new Promise(resolve => document.addEventListener('htmx:afterSettle', resolve, { once: true })));
  await page.getByRole('link', { name: 'Add schedule', exact: true }).click();
  await settled;
  assert.equal(await page.locator('h1').textContent(), 'Add schedule', `${viewport.width} schedule create HTMX`);

  await page.goto(`${origin}${prefix}/schedules`);
  settled = page.evaluate(() => new Promise(resolve => document.addEventListener('htmx:afterSettle', resolve, { once: true })));
  await page.getByRole('link', { name: 'daily-summary', exact: true }).click();
  await settled;
  assert.equal(await page.locator('h1').textContent(), 'daily-summary', `${viewport.width} schedule HTMX detail`);
  assert.equal(await page.locator('main').evaluate(element => element === document.activeElement), true, `${viewport.width} HTMX focus`);
  await page.goBack();
  await page.getByRole('heading', { name: 'Schedules', exact: true }).waitFor();
  await page.goForward();
  await page.getByRole('heading', { name: 'daily-summary', exact: true }).waitFor();

  await page.goto(`${origin}${prefix}/aliases`);
  let aliasSettled = page.evaluate(() => new Promise(resolve => document.addEventListener('htmx:afterSettle', resolve, { once: true })));
  await page.getByRole('link', { name: 'Add alias', exact: true }).click();
  await aliasSettled;
  assert.equal(await page.locator('h1').textContent(), 'Add alias', `${viewport.width} alias create HTMX`);
  await page.goto(`${origin}${prefix}/aliases`);
  aliasSettled = page.evaluate(() => new Promise(resolve => document.addEventListener('htmx:afterSettle', resolve, { once: true })));
  await page.getByRole('link', { name: 'main_chat', exact: true }).click();
  await aliasSettled;
  assert.equal(await page.locator('h1').textContent(), 'main_chat', `${viewport.width} alias HTMX detail`);
  assert.equal(await page.locator('main').evaluate(element => element === document.activeElement), true, `${viewport.width} alias HTMX focus`);
}

(async () => {
  const browser = await chromium.launch({ headless: true });
  try {
    for (const viewport of viewports) {
      const page = await browser.newPage({ viewport });
      const errors = [];
      page.on('pageerror', error => errors.push(error.message));
      page.on('requestfailed', request => errors.push(`${request.url()}: ${request.failure()?.errorText}`));
      for (const [state, status] of states) await checkState(page, state, status, viewport);
      await checkNavigation(page, viewport);
      assert.deepEqual(errors, [], `${base || '/'} ${viewport.width} browser errors`);
      await page.close();
    }
    console.log(`Schedules and Aliases: ${states.size} states at 390/768/1024/1440 passed for ${base || '/'}`);
  } finally {
    await browser.close();
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
