const assert = require('node:assert/strict');
const { chromium } = require('playwright');
const [baseURL, issuerURL, flow, phase, widthString, javaScriptString, markerDirectory] = process.argv.slice(2);
const js = javaScriptString === 'true';
let detailURL = `${baseURL}/mcp/connections/config%3Aworker-tools`;
const prefix = new URL(baseURL).pathname.replace(/\/$/, '');
let stage = 'launch';
async function activate(page, role, name) {
  const target = page.getByRole(role, { name, exact: true });
  const press = async () => {
    if (js) await target.click();
    else { await target.focus(); await page.keyboard.press('Enter'); }
  };
  if (await target.getAttribute('target') === '_blank') await press();
  else await Promise.all([page.waitForNavigation({ waitUntil: 'domcontentloaded' }), press()]);
}
async function current(page, url) {
  for (let attempt = 0; attempt < 20; attempt++) {
    const response = await page.goto(url);
    if (response.status() === 200) return response;
    assert.equal(response.status(), 409, 'concurrent binding renders a safe current-state conflict');
    const html = await page.content();
    for (const secret of ['synthetic-browser-access', 'synthetic-browser-refresh', 'SYNTHETIC-CODE']) assert.equal(html.includes(secret), false, 'conflict does not echo protected protocol data');
    await page.waitForTimeout(100);
  }
  throw new Error('current metadata remained conflicted after bounded GET refresh');
}
async function detail(page) {
  await current(page, `${baseURL}/mcp`);
  const href = await page.getByRole('link', { name: 'worker-tools', exact: true }).getAttribute('href');
  detailURL = new URL(href, baseURL).href;
  const response = await current(page, detailURL);
  assert.equal(response.status(), 200, 'current configured connection remains accessible');
  await page.getByRole('heading', { name: 'Current state', exact: true }).waitFor();
  assert.equal(await page.getByRole('button', { name: 'Save definition', exact: true }).count(), 0, 'configured capture stays read-only');
  assert.equal(await page.locator('input[data-mcp-secret]').evaluateAll(fields => fields.every(field => field.value === '')), true, 'client secrets stay write-only');
}
async function begin(page, expectedStatus) {
  await page.getByLabel('Client ID', { exact: true }).fill('browser-client');
  await page.getByLabel('Client authentication', { exact: true }).selectOption('none');
  const name = flow === 'browser' ? 'Authorize in browser' : 'Authorize with device';
  const path = await page.getByRole('button', { name, exact: true }).evaluate(button => new URL(button.getAttribute('formaction') || button.form.getAttribute('action'), location.href).pathname);
  stage = `native ${flow} begin`;
  const [response] = await Promise.all([
    page.waitForResponse(r => r.request().method() === 'POST' && decodeURIComponent(new URL(r.url()).pathname) === decodeURIComponent(path)),
    activate(page, 'button', flow === 'browser' ? 'Authorize in browser' : 'Authorize with device'),
  ]);
  assert.equal(response.status(), expectedStatus || (flow === 'browser' ? 303 : 200), 'native authorization begin');
  assert.equal(response.headers()['cache-control'], 'no-store', 'authorization issuance is not cached');
  await page.waitForLoadState('networkidle');
}
async function pendingDeviceHistory(page) {
  stage = 'pending device native history';
  assert.equal(await page.locator('[data-mcp-transient]').count(), 1, 'pending issuance contains the one-time instructions');
  const verification = await page.getByRole('link', { name: 'Open verification page', exact: true }).getAttribute('href');
  const status = await page.getByRole('link', { name: 'Refresh authorization status', exact: true }).getAttribute('href');
  const secrets = ['SYNTHETIC-CODE', verification, `${issuerURL}/verify`, 'synthetic-browser-access', 'synthetic-browser-refresh', 'synthetic-oauth-header'];
  const before = await (await page.request.get(`${issuerURL}/fixture/counts`)).json();
  const postStatuses = [];
  let cacheMissPosts = 0;
  const record = response => { if (response.request().method() === 'POST') postStatuses.push(response.status()); };
  const failed = request => {
    if (request.method() === 'POST' && request.failure()?.errorText === 'net::ERR_CACHE_MISS') cacheMissPosts++;
  };
  const inspect = async label => {
    let html;
    for (let attempt = 0; attempt < 20; attempt++) {
      try { html = await page.content(); break; }
      catch (error) {
        if (!String(error.message).includes('page is navigating') || attempt === 19) throw error;
        await page.waitForTimeout(50);
      }
    }
    for (const secret of secrets) assert.equal(html.includes(secret), false, `${label} omits pending device instructions and credentials`);
    assert.equal(await page.locator('[data-mcp-transient]').count(), 0, `${label} does not restore the issuance panel`);
  };
  page.on('response', record);
  page.on('requestfailed', failed);
  try {
    await activate(page, 'link', 'Refresh authorization status');
    assert.equal((await page.locator('#main-content p[role="status"]').innerText()).toLowerCase().includes('pending'), true);
    await inspect('pending status GET');
    for (const action of ['back', 'post-entry reload', 'status reload']) {
      stage = `pending device native ${action}`;
      if (action === 'status reload') {
        await current(page, new URL(status, baseURL).href);
        await inspect('pending status recovery GET');
      }
      const priorResponses = postStatuses.length;
      let cacheMiss = false;
      try {
        if (action === 'back') await page.goBack({ waitUntil: 'domcontentloaded' });
        else await page.reload({ waitUntil: 'domcontentloaded' });
      } catch (error) {
        assert.equal(String(error.message).includes('net::ERR_CACHE_MISS'), true, 'only a no-store history cache miss is an accepted browser error');
        cacheMiss = true;
      }
      await inspect(`pending native ${action}`);
      if (action === 'post-entry reload') {
        if (cacheMiss) assert.equal(postStatuses.length, priorResponses, 'cache-miss reload does not reach the native handler');
        else {
          assert.equal(postStatuses.length, priorResponses + 1, 'native POST-entry reload receives one guarded response');
          assert.equal(postStatuses.at(-1), 403, 'native POST-entry reload is rejected before authorization starts');
        }
      } else assert.equal(postStatuses.length, priorResponses, 'metadata history actions do not resend issuance POST');
      const observed = await (await page.request.get(`${issuerURL}/fixture/counts`)).json();
      assert.equal(observed.device_begins, before.device_begins, 'pending history never invokes another native BeginDevice');
      assert.equal(observed.device_starts, before.device_starts, 'pending history never starts another issuer authorization');
      console.log(`Pending device history ${action}: cache_miss=${cacheMiss} failed_post_cache_lookups=${cacheMissPosts} rejected_post_status=${postStatuses.at(-1) || 'none'} private_visible=false native_begin_unchanged=true issuer_start_unchanged=true`);
    }
    const after = await (await page.request.get(`${issuerURL}/fixture/counts`)).json();
    assert.equal(after.device_starts, before.device_starts, 'pending history does not start another device authorization');
    await current(page, new URL(status, baseURL).href);
    await inspect('pending status recovery GET');
  } finally { page.off('response', record); page.off('requestfailed', failed); }
}
async function complete(page, context, denied = false) {
  if (flow === 'browser') {
    stage = denied ? 'browser denial callback' : 'browser completion callback';
    const [response] = await Promise.all([
      page.waitForResponse(r => new URL(r.url()).pathname === `${prefix}/mcp/oauth/callback`),
      activate(page, 'button', denied ? 'Deny worker' : 'Authorize worker'),
    ]);
    const callbackHeaders = await response.request().allHeaders();
    const cookieEligible = Boolean(callbackHeaders.cookie);
    console.log(`Native callback cookie_eligible=${cookieEligible} login_outcome=${Boolean(response.headers().location?.includes('/login'))}`);
    assert.equal(cookieEligible, true, 'issuer callback includes ordinary browser family credentials');
    assert.equal(response.headers()['cache-control'], 'no-store', 'callback is not cached');
    assert.equal(response.status(), 303, 'callback redirects every outcome to metadata');
    assert.equal(response.headers().location.includes('state='), false, 'callback target omits protocol state');
    assert.equal(response.headers().location.includes('code='), false, 'callback target omits protocol code');
    const callbackURL = response.url();
    stage = denied ? 'denied callback native return' : 'authorized callback native return';
    await activate(page, 'link', 'Continue to MCP');
    const replay = await page.request.get(callbackURL, { maxRedirects: 0 });
    assert.equal(replay.status(), 303, 'callback replay redirects to safe metadata');
    assert.equal(replay.headers().location.includes('oauth_result=ended'), true, 'callback state is one-use');
    assert.equal((await replay.text()).includes('synthetic-browser-access'), false, 'callback replay does not reveal access credential');
    await page.waitForLoadState('networkidle');
    return;
  }
  stage = 'device one-time instructions';
  assert.equal(await page.locator('[data-mcp-transient]').count(), 1, 'device issuance supplies one-time instructions');
  const statusURL = await page.getByRole('link', { name: 'Refresh authorization status', exact: true }).getAttribute('href');
  const safeGET = await page.request.get(`${new URL(baseURL).origin}${statusURL}`);
  assert.equal(safeGET.status(), 200);
  assert.equal(safeGET.headers()['cache-control'], 'no-store');
  const safeHTML = await safeGET.text();
  assert.equal(safeHTML.includes('SYNTHETIC-CODE'), false, 'device code is absent from status GET');
  assert.equal(safeHTML.includes('device='), false, 'verification capability is absent from status GET');
  assert.equal(safeHTML.includes(`${issuerURL}/verify`), false, 'verification URI is absent from status GET');
  stage = 'device verification page';
  const [verification] = await Promise.all([
    context.waitForEvent('page'), activate(page, 'link', 'Open verification page'),
  ]);
  await verification.waitForLoadState();
  await activate(verification, 'button', 'Authorize worker');
  await verification.close();
  stage = 'device completion metadata polling';
  await activate(page, 'link', 'Refresh authorization status');
  for (let attempt = 0; attempt < 30; attempt++) {
    await page.waitForLoadState('networkidle');
    assert.equal(await page.locator('[data-mcp-transient]').count(), 0, 'device refresh does not repeat private instructions');
    const message = await page.locator('#main-content p[role="status"]').innerText();
    if (!message.toLowerCase().includes('pending')) {
      assert.equal(message.includes('authorization was saved'), true, 'device completion stores authorization before any readiness claim');
      break;
    }
    await page.waitForTimeout(500); await page.reload();
    if (attempt === 29) throw new Error('device completion did not finish within fixture budget');
  }
  stage = 'device completion current connection';
  await activate(page, 'link', 'Open connection');
}
(async () => {
  const browser = await chromium.launch({ headless: true });
  try {
    const context = await browser.newContext({ viewport: { width: Number(widthString), height: 900 }, javaScriptEnabled: js });
    const page = await context.newPage();
    page.on('response', r => { if (r.request().method() === 'POST') console.log(`Native POST ${new URL(r.url()).pathname} status=${r.status()} origin=${r.request().headers().origin === new URL(baseURL).origin ? 'trusted' : r.request().headers().origin === 'null' ? 'null' : 'other'}`); });
    const errors = []; page.on('pageerror', () => errors.push('browser script error'));
    stage = 'ordinary login';
    await page.goto(`${baseURL}/login`);
    await page.getByLabel('Username').fill('superuser');
    await page.getByLabel(/^Password(?: \(required\))?$/).fill('correct-horse-battery');
    await Promise.all([page.waitForURL(`${baseURL}/overview`), activate(page, 'button', 'Sign in')]);
    await detail(page);
    stage = 'security rejection';
    const denied = await page.request.post(`${detailURL}/oauth/${flow}`, { form: { client_id: 'browser-client', client_auth_method: 'none', csrf_token: 'invalid' }, headers: { Origin: new URL(baseURL).origin } });
    assert.equal(denied.status(), 403, 'invalid CSRF cannot start authorization');
    if (phase === 'pending-restart') {
      stage = 'pending device before host restart';
      await begin(page);
      const statusPath = await page.getByRole('link', { name: 'Refresh authorization status', exact: true }).getAttribute('href');
      await activate(page, 'link', 'Refresh authorization status');
      assert.equal((await page.locator('#main-content p[role="status"]').innerText()).toLowerCase().includes('pending'), true);
      assert.equal(await page.locator('[data-mcp-transient]').count(), 0);
      const fs = require('node:fs/promises');
      await fs.writeFile(`${markerDirectory}/pending`, 'pending', { mode: 0o600 });
      for (let attempt = 0; ; attempt++) {
        try { await fs.stat(`${markerDirectory}/restarted`); break; }
        catch { if (attempt === 450) throw new Error('host restart checkpoint timed out'); }
        await new Promise(resolve => setTimeout(resolve, 100));
      }
      stage = 'stale attempt after host restart';
      const stale = await page.goto(`${new URL(baseURL).origin}${statusPath}`);
      assert.equal(stale.status(), 404, 'host restart invalidates unfinished device attempt');
      await page.getByText('the host restarted', { exact: false }).waitFor();
      const staleHTML = await page.content();
      for (const secret of ['SYNTHETIC-CODE', 'device=', 'synthetic-browser-access']) assert.equal(staleHTML.includes(secret), false, 'stale attempt shows only safe retry metadata');
      await activate(page, 'link', 'Return to MCP');
      await detail(page);
    }
    if (phase === 'fresh') {
      stage = flow === 'browser' ? 'denial then retry' : 'cancellation then retry';
      await begin(page);
      if (flow === 'browser') await complete(page, context, true);
      else {
        if (!js) await pendingDeviceHistory(page);
        stage = 'native device cancellation';
        const [cancelled] = await Promise.all([
          page.waitForResponse(r => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/cancel')),
          activate(page, 'button', 'Cancel authorization'),
        ]);
        assert.equal(cancelled.status(), 303, 'native device cancellation respects origin and CSRF guards');
        assert.equal(page.url(), `${baseURL}/mcp`);
      }
      await detail(page);
    }
    if (flow === 'device' && phase === 'fresh') {
      stage = 'unsupported device alternative';
      await page.request.get(`${issuerURL}/fixture/device-support?value=false`);
      await begin(page, 503);
      await page.getByText('Browser authorization remains available', { exact: false }).waitFor();
      await page.request.get(`${issuerURL}/fixture/device-support?value=true`);
      await detail(page);
    }
    stage = 'authorization with unavailable tools';
    await page.request.get(`${issuerURL}/fixture/unavailable?value=true`);
    await begin(page); await complete(page, context);
    await detail(page);
    assert.equal(await page.locator('.status-chip').filter({ hasText: /^Ready$/ }).count(), 0, 'saved authorization does not imply Ready');
    stage = 'saved grant retry control';
    await page.getByRole('button', { name: 'Retry tool attachment', exact: true }).waitFor();
    const before = await (await page.request.get(`${issuerURL}/fixture/counts`)).json();
    const revision = await page.locator('input[name="expected_revision_id"]').inputValue();
    stage = 'attachment retry using saved grant';
    await page.request.get(`${issuerURL}/fixture/unavailable?value=false`);
    await activate(page, 'button', 'Retry tool attachment');
    await detail(page);
    assert.equal(await page.locator('.status-chip').filter({ hasText: /^Ready$/ }).count(), 1, 'real MCP discovery reaches Ready');
    assert.equal(await page.locator('input[name="expected_revision_id"]').inputValue(), revision, 'same-binding retry keeps revision');
    const after = await (await page.request.get(`${issuerURL}/fixture/counts`)).json();
    assert.equal(after.exchanges, before.exchanges, 'tool retry does not repeat OAuth');
    stage = 'history and protected output';
    const html = await page.content();
    for (const secret of ['synthetic-browser-access', 'synthetic-browser-refresh', 'synthetic-oauth-header', 'SYNTHETIC-CODE']) assert.equal(html.includes(secret), false, 'metadata detail does not reveal credentials or device instructions');
    await page.goto(`${baseURL}/mcp`); await page.goBack();
    await page.getByRole('heading', { name: 'Current state', exact: true }).waitFor();
    assert.equal(await page.locator('[data-mcp-transient]').count(), 0);
    assert.equal(await page.evaluate(() => Object.values(sessionStorage).every(value => !value.includes('synthetic-browser-access') && !value.includes('SYNTHETIC-CODE'))), true, 'history never persists authorization credentials');
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, 'authorization layout fits viewport');
    assert.deepEqual(errors, []);
    stage = 'native sign out';
    await activate(page, 'button', 'Sign out');
    await page.waitForURL(`${baseURL}/login`);
    await context.close();
    console.log(`MCP OAuth ${flow} ${phase}: ordinary login, native authorization, safe recovery, real Ready and history passed (${widthString}px JavaScript=${js})`);
  } finally { await browser.close(); }
})().catch(error => {
  // Playwright diagnostics may contain an issuer/callback query. Keep OAuth
  // capabilities and cookies out of test logs; the stage is sufficient to retry.
  const safeMessage = String(error.message).split('\n')[0].replace(/https?:\/\/[^\s]+/g, '[redacted URL]');
  console.error(`MCP OAuth browser verification failed during ${stage}: ${safeMessage}`);
  process.exitCode = 1;
});
