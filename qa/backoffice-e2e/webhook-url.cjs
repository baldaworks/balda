const assert = require('node:assert/strict');
const path = require('node:path');
const { chromium } = require('playwright');

const script = path.join(__dirname, '../../internal/apps/backoffice/internal/webui/static/app.js');
const prefix = 'https://lab.metalagman.dev/balda/webhooks/';

function form() {
  return `<main id="main-content"><form><input data-webhook-url-name maxlength="64">
    <input data-webhook-url-prefix="${prefix}" readonly></form></main>`;
}

(async () => {
  const browser = await chromium.launch({ headless: true });
  try {
    const page = await browser.newPage();
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    await page.setContent(form());
    await page.addScriptTag({ path: script });
    const name = page.locator('[data-webhook-url-name]');
    const url = page.locator('[data-webhook-url-prefix]');
    assert.equal(await url.inputValue(), '');
    await name.fill('orders');
    assert.equal(await url.inputValue(), `${prefix}orders`);
    await name.fill('Bad Name');
    assert.equal(await url.inputValue(), '');
    await name.fill('order_events');
    assert.equal(await url.inputValue(), `${prefix}order_events`);

    await page.evaluate(markup => {
      document.querySelector('#main-content').outerHTML = markup;
      document.dispatchEvent(new CustomEvent('htmx:afterSwap', {
        detail: { target: document.querySelector('#main-content') },
      }));
    }, form());
    await page.locator('[data-webhook-url-name]').fill('new_route');
    assert.equal(await page.locator('[data-webhook-url-prefix]').inputValue(), `${prefix}new_route`);
    assert.deepEqual(errors, []);
  } finally {
    await browser.close();
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
