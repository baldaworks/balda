const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

module.exports = async function checkConfirmations(browser, baseURL, viewport) {
  const page = await browser.newPage({ viewport });
  const errors = [], requests = [];
  let releaseRequest;
  page.on('pageerror', error => errors.push(error.message));
  await page.route('**/confirmation-preview*', async route => {
    requests.push({ url: route.request().url(), body: route.request().postData() });
    await new Promise(resolve => { const timeout = setTimeout(resolve, 5000); releaseRequest = () => { clearTimeout(timeout); resolve(); }; });
    await route.fulfill(route.request().isNavigationRequest() ? { status: 303, headers: { location: `${baseURL}/qa/ui/style-guide` } } : { status: 503 });
  });
  for (const mode of ['native', 'htmx']) {
    await page.goto(`${baseURL}/qa/ui/style-guide`);
    const form = page.locator(`#guide-confirm-${mode}`);
    const trigger = form.getByRole('button', { name: 'Remove example' });
    const modal = page.getByRole('dialog', { name: 'Remove example?' });
    await form.getByLabel('Example name').fill('');
    await trigger.click();
    assert.equal(await modal.isVisible(), false, `${mode} validates before consent`);
    await form.getByLabel('Example name').fill('edited <example>');
    for (const dismiss of ['Cancel', 'Escape', 'Close', 'Backdrop']) {
      if (dismiss === 'Cancel') { await trigger.focus(); await page.keyboard.press('Enter'); }
      else await trigger.click();
      await modal.waitFor({ state: 'visible' });
      assert.match(await modal.textContent(), /<example> & retained history/);
      assert.equal(await modal.locator('script, img').count(), 0, 'target remains plain text');
      assert.equal(await modal.locator('.modal-body').evaluate(e => e.scrollWidth <= e.clientWidth), true, 'long escaped target wraps inside dialog');
      await page.waitForFunction(() => document.querySelector('[data-confirm-cancel]') === document.activeElement);
      await page.keyboard.press('Shift+Tab');
      assert.equal(await modal.evaluate(e => e.contains(document.activeElement)), true, 'focus stays inside dialog');
      if (dismiss === 'Escape') await page.keyboard.press('Escape');
      else if (dismiss === 'Backdrop') await page.locator('#action-confirmation').click({ position: { x: 2, y: 2 } });
      else await modal.getByRole('button', { name: dismiss, exact: true }).click();
      await modal.waitFor({ state: 'hidden' });
      assert.equal(requests.length, 0, `${mode} ${dismiss} sends no request`);
      await page.waitForFunction(id => document.querySelector(`#${id} button[type=submit]`) === document.activeElement, `guide-confirm-${mode}`);
    }
    await trigger.click();
    await modal.waitFor({ state: 'visible' });
    await page.waitForFunction(() => document.querySelector('[data-confirm-cancel]') === document.activeElement);
    const dir = process.env.BACKOFFICE_CONFIRMATION_SCREENSHOTS;
    if (dir) {
      fs.mkdirSync(dir, { recursive: true });
      await page.screenshot({ path: path.join(dir, `${new URL(baseURL).pathname.replaceAll('/', '-') || 'root'}-${mode}-${viewport.width}.png`) });
    }
    await page.evaluate(({ id, mode }) => {
      const form = document.getElementById(id);
      const duplicate = () => queueMicrotask(() => form.requestSubmit(form.querySelector('button[type=submit]')));
      form.addEventListener(mode === 'native' ? 'submit' : 'htmx:beforeRequest', duplicate, { once: true });
    }, { id: `guide-confirm-${mode}`, mode });
    const confirming = modal.getByRole('button', { name: 'Remove example', exact: true }).click({ noWaitAfter: true });
    for (let tries = 0; !releaseRequest && tries < 50; tries++) await new Promise(resolve => setTimeout(resolve, 50));
    assert.equal(requests.length, 1, `${mode} confirms once`);
    const completed = mode === 'native' ? page.waitForEvent('load') : page.waitForResponse(response => response.url().includes('/confirmation-preview'));
    releaseRequest(); releaseRequest = null;
    await completed;
    await confirming;
    assert.equal(await modal.isVisible(), false, 'in-flight invocation cannot reopen');
    assert.equal(requests.length, 1, 'in-flight duplicate sends no additional request');
    const values = new URLSearchParams(requests[0].body);
    assert.equal(values.get('name'), 'edited <example>');
    assert.equal(values.get('confirm'), 'on');
    assert.equal(values.get('operation'), 'remove');
    assert.equal(values.get('expected_version'), '7');
    assert.equal(values.get('request_key'), 'synthetic-request-key');
    assert.match(requests[0].url, /confirmation-preview-selected/);
    requests.length = 0;
    await trigger.click();
    await modal.waitFor({ state: 'visible' });
    assert.equal(requests.length, 0, 'subsequent attempt after completed response, including enhanced503, needs fresh consent');
    await form.getByLabel('Example name').evaluate(e => { e.value = 'changed during dialog'; });
    await modal.getByRole('button', { name: 'Remove example', exact: true }).click();
    await page.waitForFunction(() => !document.querySelector('#action-confirmation').classList.contains('show'));
    assert.equal(requests.length, 0, 'edited invocation cannot consume old consent');
    await page.evaluate(id => {
      document.getElementById(id).requestSubmit();
      dispatchEvent(new PopStateEvent('popstate'));
    }, `guide-confirm-${mode}`);
    await page.waitForTimeout(500);
    assert.equal(await modal.isVisible(), false, 'navigation during show clears the modal');
    assert.equal(await page.locator('.modal-backdrop').count(), 0);
    await trigger.click();
    await modal.waitFor({ state: 'visible' });
    await page.evaluate(() => {
      document.querySelector('main').dispatchEvent(new CustomEvent('htmx:beforeCleanupElement', { bubbles: true, detail: { elt: document.querySelector('main') } }));
      document.querySelector('main').remove();
    });
    await modal.waitFor({ state: 'hidden' });
    assert.equal(requests.length, 0, 'removed form cannot resume');
  }
  await page.goto(`${baseURL}/qa/ui/style-guide`);
  const fragment = await page.locator('main').evaluate(e => e.outerHTML);
  await page.route('**/confirmation-fragment', route => route.fulfill({ contentType: 'text/html', body: fragment }));
  await page.evaluate(url => htmx.ajax('GET', url, { target: '#main-content', swap: 'outerHTML' }), `${baseURL}/confirmation-fragment`);
  await page.locator('#guide-confirm-htmx button[type=submit]').click();
  await page.getByRole('dialog').waitFor({ state: 'visible' });
  await page.keyboard.press('Escape');
  await page.getByRole('dialog').waitFor({ state: 'hidden' });
  assert.equal(requests.length, 0, 'fresh fragment uses shared presenter');
  await page.locator('#guide-confirm-native input[data-confirm-consent]').evaluate(e => e.remove());
  await page.locator('#guide-confirm-native button[type=submit]').click();
  await page.getByRole('alert').filter({ hasText: 'Confirmation is unavailable' }).waitFor();
  assert.equal(requests.length, 0, 'malformed invocation fails closed visibly');
  await page.route('**/assets/vendor/bootstrap/bootstrap.min.js', route => route.fulfill({ contentType: 'application/javascript', body: '' }));
  await page.goto(`${baseURL}/qa/ui/style-guide`);
  await page.locator('#guide-confirm-native button[type=submit]').click();
  await page.getByRole('alert').filter({ hasText: 'Confirmation is unavailable' }).waitFor();
  assert.equal(requests.length, 0, 'missing Bootstrap fails closed visibly');
  assert.deepEqual(errors, [], 'confirmation browser errors');
  await page.close();
};

if (require.main === module) {
  const { chromium } = require('playwright');
  (async () => {
    const browser = await chromium.launch({ headless: true });
    try {
      for (const viewport of [{ width: 1440, height: 900 }, { width: 390, height: 844 }]) {
        await module.exports(browser, process.argv[2] + process.argv[3], viewport);
      }
      console.log('Native/HTMX confirmation request, validation, dismissal and focus checks passed');
    } finally { await browser.close(); }
  })().catch(error => { console.error(error); process.exitCode = 1; });
}
