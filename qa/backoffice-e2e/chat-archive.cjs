const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { chromium } = require('playwright');
const [origin, base, rawWidth, first, second, controller] = process.argv.slice(2);
const width = Number(rawWidth), height = width === 390 ? 844 : width === 768 ? 1024 : width === 1024 ? 768 : 900;
const url = `${origin}${base}`;
assert.equal(new URL(origin).hostname, '127.0.0.1', 'isolated application');
assert.equal(new URL(controller).hostname, '127.0.0.1', 'isolated fixture controller');
(async () => {
 const browser = await chromium.launch({ headless: true });
 try {
  const context = await browser.newContext({ viewport: { width, height } });
  const page = await context.newPage();
  const errors = [];
  page.on('pageerror', e => errors.push(e.message));
  const directory = path.join(process.env.BALDA_CHAT_ARCHIVE_APPLICATION_SCREENSHOTS || '/tmp/balda-chat-archive-application', base ? 'prefixed' : 'root', `${width}x${height}`);
  fs.mkdirSync(directory, { recursive: true });
  async function screenshot(name) {
   assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), `${name}: no page overflow`);
   assert.equal(await page.locator('main').count(), 1, `${name}: single main`);
   await page.screenshot({ path: path.join(directory, `${name}.png`), fullPage: true });
  }
  async function login(p, username) {
   await p.goto(`${url}/login`);
   await p.getByLabel(/^Username/).fill(username);
   await p.getByLabel(/^Password/).fill('correct horse battery staple');
   await Promise.all([p.waitForURL(`${url}/overview`), p.getByRole('button', { name: 'Sign in', exact: true }).click()]);
  }
  async function fixture(action) { const r = await context.request.post(`${controller}?action=${action}`); assert.equal(r.status(), 200, action); }
  async function privateGet(route, status, fragment = false, client = context.request) {
   const r = await client.get(`${url}${route}`, { maxRedirects: 0, headers: fragment ? { 'HX-Request': 'true', 'HX-Target': 'main-content' } : {} });
   assert.equal(r.status(), status, `${route} fragment=${fragment}`);
   assert.match(r.headers()['cache-control'], /no-store/);
   return r;
  }
  const anonymous = await browser.newContext();
  for (const fragment of [false, true]) for (const route of ['/chat-sessions', `/chat-sessions/${first}`]) {
   const r = await privateGet(route, 401, fragment, anonymous.request);
   assert.ok(!(await r.text()).includes('Synthetic line-'), 'anonymous content denial');
  }
  await anonymous.close();
  await login(page, 'reader');
  const nav = page.locator('.app-sidebar a[href$="/chat-sessions"]');
  assert.equal(await nav.count(), 1, 'administrator navigation');
  await page.goto(`${url}/chat-sessions`);
  await page.getByRole('heading', { name: 'Chat sessions', exact: true }).waitFor();
  assert.equal(await page.locator('tbody tr').count(), 50, 'bounded real inventory');
  const olderHref = await page.getByRole('link', { name: 'Older sessions', exact: true }).getAttribute('href');
  await page.getByRole('link', { name: 'Older sessions', exact: true }).click();
  await page.waitForURL(`${origin}${olderHref}`);
  assert.equal(await page.locator('tbody tr').count(), 3, 'older keyset window');
  await screenshot('older-inventory');
  await page.goto(`${url}/chat-sessions?q=Synthetic+browser&channel=telegram&status=ended`);
  assert.equal(await page.locator('tbody tr').count(), 1, 'combined metadata filters');
  await page.getByRole('link', { name: 'Synthetic browser first', exact: true }).click();
  await page.getByRole('heading', { name: 'Session chat', exact: true }).waitFor();
  assert.equal(await page.locator('.archive-message').count(), 100, 'bounded latest transcript');
  assert.equal(await page.locator('.archive-message-agent').count(), 1, 'selected final only');
  assert.ok((await page.locator('.archive-message strong').allTextContents()).includes('Synthetic speaker two'), 'actual second speaker');
  assert.ok((await page.locator('.archive-text').allTextContents()).some(t => t.includes('<script>harmless user</script>')), 'escaped original text');
  assert.equal(await page.locator('.archive-text script').count(), 0, 'no injected script');
  assert.equal(await page.locator('.archive-attachment a').count(), 0, 'safe attachment labels');
  const body = await page.locator('main').innerText();
  for (const hidden of ['hidden reasoning marker', 'hidden provider prompt marker', 'execution-one', 'browser-final']) assert.ok(!body.includes(hidden), `private ${hidden}`);
  await screenshot('transcript');
  await page.reload();
  await page.getByRole('heading', { name: 'Session chat', exact: true }).waitFor();
  await page.getByRole('link', { name: 'Older messages', exact: true }).click();
  await page.waitForFunction(() => document.querySelectorAll('.archive-message').length === 6);
  assert.match(await page.locator('.archive-text').first().innerText(), /line-000/);
  await screenshot('older-transcript');
  await page.getByRole('link', { name: 'Back to Chat sessions', exact: true }).click();
  await page.getByRole('heading', { name: 'Chat sessions', exact: true }).waitFor();
  assert.equal(await page.locator('#archive-query').inputValue(), 'Synthetic browser');
  assert.equal(await page.locator('#archive-channel').inputValue(), 'telegram');
  assert.equal(await page.locator('#archive-status').inputValue(), 'ended');
  await page.goto(`${url}/chat-sessions/${second}`);
  await page.getByRole('heading', { name: 'No chat messages', exact: true }).waitFor();
  await screenshot('empty-replacement');
  await page.goto(`${url}/chat-sessions?q=no-synthetic-match`);
  await page.getByRole('heading', { name: 'No chat sessions', exact: true }).waitFor();
  for (const fragment of [false, true]) {
   const full = await privateGet(`/chat-sessions/${first}`, 200, fragment);
   const text = await full.text();
   assert.match(text, /&lt;script&gt;harmless/);
   assert.ok(!text.includes('hidden provider prompt marker'));
   assert.ok(full.headers()['content-security-policy'], 'CSP retained');
   await privateGet('/chat-sessions/invalid', 400, fragment);
   await privateGet('/chat-sessions/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa', 404, fragment);
   await privateGet(`/chat-sessions/${first}?cursor=bad`, 400, fragment);
  }
  await fixture('unavailable');
  for (const fragment of [false, true]) for (const route of ['/chat-sessions', `/chat-sessions/${first}`]) await privateGet(route, 503, fragment);
  await page.goto(`${url}/chat-sessions`);
  await page.getByRole('heading', { name: 'Chat archive could not be opened', exact: true }).waitFor();
  await screenshot('unavailable');
  await fixture('available');
  const operatorContext = await browser.newContext();
  const operator = await operatorContext.newPage();
  await login(operator, 'operator');
  assert.equal(await operator.locator('.app-sidebar a[href$="/chat-sessions"]').count(), 0, 'operator nav omitted');
  for (const fragment of [false, true]) for (const route of ['/chat-sessions', `/chat-sessions/${first}`]) await privateGet(route, 403, fragment, operatorContext.request);
  await operatorContext.close();
  // Removing the short-lived access cookie exercises ordinary refresh with its
  // retained refresh family and requested archive return path.
  const oldAccess = (await context.cookies()).find(c => c.name === 'balda_access').value;
  await context.clearCookies({ name: 'balda_access' });
  const refreshed = page.waitForResponse(r => r.request().method() === 'POST' && new URL(r.url()).pathname === base + '/auth/session/refresh');
  await page.goto(`${url}/chat-sessions/${first}`);
  try { assert.equal((await refreshed).status(), 303); } catch (error) { console.error('REFRESH', page.url(), await page.locator('body').innerText()); throw error; }
  await page.waitForLoadState('networkidle');
  await page.getByRole('heading', { name: 'Session chat', exact: true }).waitFor();
  assert.notEqual((await context.cookies()).find(c => c.name === 'balda_access').value, oldAccess);
  assert.equal(page.url(), `${url}/chat-sessions/${first}`, 'refresh returns to archive');
  await screenshot('refreshed-transcript');
  for (const change of ['demote', 'disable']) {
   await fixture(change);
   for (const fragment of [false, true]) for (const route of ['/chat-sessions', `/chat-sessions/${first}`]) {
    const r = await context.request.get(`${url}${route}`, { maxRedirects: 0, headers: fragment ? { 'HX-Request': 'true' } : {} });
    assert.ok([401,403].includes(r.status()), `${change} current authority denied: ${r.status()}`);
    assert.ok(!(await r.text()).includes('Synthetic line-'), `${change} content denied`);
   }
   await fixture('restore');
   await login(page, 'reader');
  }
  await fixture('expire');
  for (const fragment of [false, true]) for (const route of ['/chat-sessions', `/chat-sessions/${first}`]) {
   const r = await context.request.get(`${url}${route}`, { maxRedirects: 0, headers: fragment ? { 'HX-Request': 'true' } : {} });
   assert.equal(r.status(), 401, 'expired family denied');
   assert.ok(!(await r.text()).includes('Synthetic line-'), 'expired content denied');
  }
  await login(page, 'reader');
  await fixture('revoke');
  for (const fragment of [false, true]) for (const route of ['/chat-sessions', `/chat-sessions/${first}`]) {
   const r = await context.request.get(`${url}${route}`, { maxRedirects: 0, headers: fragment ? { 'HX-Request': 'true' } : {} });
   assert.ok([401,403].includes(r.status()), 'revoked family denied');
   assert.ok(!(await r.text()).includes('Synthetic line-'), 'revoked content denied');
  }
  assert.deepEqual(errors, [], 'no browser errors');
  await context.close();
  console.log(`PASS authenticated archive ${base || '/'} ${width}x${height}: normal login, shared reopened SQLite+ADK chat, bounded inventory/transcript, HTMX/back/refresh, safe text, admin/operator/anonymous/demoted/disabled/revoked, errors, passive reads; screens ${directory}`);
 } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
