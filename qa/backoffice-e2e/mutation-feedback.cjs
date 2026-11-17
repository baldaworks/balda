const assert = require('node:assert/strict');

async function waitForPlan(plan) {
  for (let tries = 0; !plan.route && tries < 100; tries++) await new Promise(resolve => setTimeout(resolve, 10));
  assert.ok(plan.route, 'mutation request reached the route');
}

module.exports = async function checkMutationFeedback(browser, baseURL, viewport) {
  const page = await browser.newPage({ viewport });
  const errors = [];
  let plan = null;
  page.on('pageerror', error => errors.push(error.message));
  await page.route('**/confirmation-preview', async route => {
    if (route.request().method() !== 'POST' || !plan) return route.fallback();
    const active = plan;
    active.route = route;
    if (active.wait) await active.wait;
    if (active.abort) return route.abort('failed');
    try {
      await route.fulfill({
        status: active.status,
        contentType: 'text/html',
        headers: active.headers,
        body: active.body || '<main id="main-content"><h1>Mutation failed</h1><p role="alert">Synthetic failure.</p></main>',
      });
    } catch (error) {
      if (!/Target page, context or browser has been closed|Route is already handled/.test(error.message)) throw error;
    }
  });

  await page.goto(`${baseURL}/qa/ui/webhooks-secret`);
  const webhookSecret = page.locator('#webhook-new-secret');
  assert.equal(await webhookSecret.inputValue(), 'synthetic-invalid-secret-preview-only', 'fresh pageshow preserves server-rendered one-time webhook secret');
  await page.evaluate(() => dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true })));
  assert.equal(await webhookSecret.inputValue(), '', 'BFCache pageshow clears one-time webhook secret');

  await page.goto(`${baseURL}/qa/ui/bindings-issued`);
  const invitation = page.locator('input[data-binding-secret]').first();
  assert.equal(await invitation.inputValue(), 'bind_SYNTHETIC_PREVIEW_ONLY', 'fresh pageshow preserves server-rendered invitation');
  await page.evaluate(() => document.dispatchEvent(new CustomEvent('htmx:historyRestore')));
  assert.equal(await invitation.inputValue(), '', 'history restore clears one-time invitation');

  async function openForm() {
    await page.goto(`${baseURL}/qa/ui/style-guide`);
    const structures = await page.locator('form[hx-post]').evaluateAll(forms => forms.map(form => ({
      marked: form.hasAttribute('data-mutation-form'),
      statuses: form.querySelectorAll(':scope > .mutation-status[role="status"][aria-live="polite"]').length,
    })));
    assert.ok(structures.length > 0, 'style guide exposes HTMX mutation forms');
    assert.deepEqual(structures.filter(item => !item.marked || item.statuses !== 1), [], 'each rendered HTMX mutation has one direct accessible status');
    return page.locator('#guide-mutation');
  }

  async function exercise(status) {
    const form = await openForm();
    const secret = form.locator('[data-write-only]');
    const submit = form.getByRole('button', { name: 'Save example' });
    const unavailable = form.getByRole('button', { name: 'Initially unavailable' });
    let release;
    plan = { status, wait: new Promise(resolve => { release = resolve; }) };
    await secret.fill(`synthetic-${status}`);
    const response = page.waitForResponse(value => value.request().method() === 'POST' && value.url().endsWith('/confirmation-preview'));
    await submit.click({ noWaitAfter: true });
    await waitForPlan(plan);
    assert.equal(await form.getAttribute('aria-busy'), 'true', `${status} exposes aria-busy`);
    assert.equal(await submit.isDisabled(), true, `${status} disables submit`);
    assert.equal(await unavailable.isDisabled(), true, `${status} preserves initial disabled state while busy`);
    assert.equal(await form.getByRole('status').isVisible(), true, `${status} exposes visible status`);
    release();
    await response;
    await page.waitForFunction(() => !document.querySelector('#guide-mutation').hasAttribute('aria-busy'));
    assert.equal(await submit.isDisabled(), false, `${status} restores enabled submit`);
    assert.equal(await unavailable.isDisabled(), true, `${status} retains initially disabled submit`);
    assert.equal(await secret.inputValue(), '', `${status} clears write-only input`);
    assert.equal(await form.getByRole('status').isHidden(), true, `${status} hides status`);
    plan = null;
  }

  for (const status of [400, 403, 404, 409, 503]) await exercise(status);

  {
    const form = await openForm();
    await form.locator('[data-write-only]').fill('synthetic-success');
    plan = {
      status: 204,
      headers: { 'HX-Location': JSON.stringify({ path: `${baseURL}/qa/ui/style-guide`, target: '#main-content', swap: 'outerHTML' }) },
      body: '',
    };
    await Promise.all([
      page.waitForResponse(value => value.request().method() === 'GET' && value.url().endsWith('/qa/ui/style-guide')),
      form.getByRole('button', { name: 'Save example' }).click(),
    ]);
    const replacement = page.locator('#guide-mutation');
    assert.equal(await replacement.locator('[data-write-only]').inputValue(), '', 'HX-Location replacement does not refill write-only input');
    assert.equal(await replacement.getAttribute('aria-busy'), null, 'HX-Location replacement is settled');
    plan = null;
  }

  {
    const form = await openForm();
    let release;
    plan = { status: 200, wait: new Promise(resolve => { release = resolve; }) };
    await form.locator('[data-write-only]').fill('synthetic-abort');
    await form.getByRole('button', { name: 'Save example' }).click({ noWaitAfter: true });
    await waitForPlan(plan);
    await form.evaluate(element => htmx.trigger(element, 'htmx:abort'));
    await page.waitForFunction(() => !document.querySelector('#guide-mutation').hasAttribute('aria-busy'));
    assert.equal(await form.locator('[data-write-only]').inputValue(), '', 'abort clears write-only input');
    release();
    plan = null;
  }

  {
    const form = await openForm();
    plan = { abort: true };
    await form.locator('[data-write-only]').fill('synthetic-send-error');
    await form.getByRole('button', { name: 'Save example' }).click({ noWaitAfter: true });
    await waitForPlan(plan);
    await page.waitForFunction(() => !document.querySelector('#guide-mutation').hasAttribute('aria-busy'));
    assert.equal(await form.locator('[data-write-only]').inputValue(), '', 'send error clears write-only input');
    plan = null;
  }

  {
    const form = await openForm();
    let release;
    plan = { status: 200, wait: new Promise(resolve => { release = resolve; }) };
    await form.evaluate(element => element.setAttribute('hx-request', '{"timeout":25}'));
    await form.locator('[data-write-only]').fill('synthetic-timeout');
    await form.getByRole('button', { name: 'Save example' }).click({ noWaitAfter: true });
    await waitForPlan(plan);
    await page.waitForFunction(() => !document.querySelector('#guide-mutation').hasAttribute('aria-busy'));
    assert.equal(await form.locator('[data-write-only]').inputValue(), '', 'timeout clears write-only input');
    release();
    plan = null;
  }

  {
    const form = await openForm();
    await form.evaluate(element => {
      element.setAttribute('aria-busy', 'true');
      const submit = element.querySelector('button:not([disabled])');
      submit.disabled = true;
      submit.setAttribute('data-mutation-disabled', '');
      element.querySelector('.mutation-status').hidden = false;
      element.querySelector('[data-write-only]').value = 'history-secret';
      document.dispatchEvent(new CustomEvent('htmx:historyRestore'));
    });
    assert.equal(await form.getAttribute('aria-busy'), null, 'history restore settles form');
    assert.equal(await form.locator('[data-write-only]').inputValue(), '', 'history restore clears write-only input');
    await form.locator('[data-write-only]').fill('pageshow-secret');
    await page.evaluate(() => dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true })));
    assert.equal(await form.locator('[data-write-only]').inputValue(), '', 'BFCache pageshow clears write-only input');
  }

  {
    await openForm();
    const fragment = await page.locator('main').evaluate(element => element.outerHTML);
    await page.route('**/mutation-fragment', route => route.fulfill({ contentType: 'text/html', body: fragment }));
    await page.evaluate(url => htmx.ajax('GET', url, { target: '#main-content', swap: 'outerHTML' }), `${baseURL}/mutation-fragment`);
    const form = page.locator('#guide-mutation');
    plan = { status: 409 };
    await form.locator('[data-write-only]').fill('swapped-secret');
    await form.getByRole('button', { name: 'Save example' }).click();
    await page.waitForFunction(() => !document.querySelector('#guide-mutation').hasAttribute('aria-busy'));
    assert.equal(await form.locator('[data-write-only]').inputValue(), '', 'dynamically swapped form uses shared clearing');
    plan = null;
  }

  assert.deepEqual(errors, [], 'mutation feedback browser errors');
  await page.close();

  const nativeContext = await browser.newContext({ javaScriptEnabled: false, viewport });
  const nativePage = await nativeContext.newPage();
  let nativePosts = 0;
  await nativePage.route('**/confirmation-preview', route => {
    if (route.request().method() !== 'POST') return route.fallback();
    nativePosts++;
    return route.fulfill({ status: 303, headers: { location: `${baseURL}/qa/ui/style-guide` } });
  });
  await nativePage.goto(`${baseURL}/qa/ui/style-guide`);
  await nativePage.locator('#guide-mutation [data-write-only]').fill('native-secret');
  await nativePage.locator('#guide-mutation').getByRole('button', { name: 'Save example' }).click({ force: true });
  assert.equal(nativePosts, 1, 'JS-disabled form submits one native POST');
  await nativeContext.close();
};

if (require.main === module) {
  const { chromium } = require('playwright');
  (async () => {
    const browser = await chromium.launch({ headless: true });
    try {
      await module.exports(browser, process.argv[2] + process.argv[3], { width: 390, height: 844 });
      console.log('Mutation feedback lifecycle checks passed');
    } finally {
      await browser.close();
    }
  })().catch(error => { console.error(error); process.exitCode = 1; });
}
