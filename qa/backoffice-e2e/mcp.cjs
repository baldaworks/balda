const assert = require('node:assert/strict');
const { chromium } = require('playwright');
const [baseURL, workerURL, mode = 'crud', recorderURL = ''] = process.argv.slice(2);
const password = process.env.BALDA_E2E_ADMIN_PASSWORD;
const mcpBearer = process.env.BALDA_E2E_MCP_BEARER;
const protectedValue = 'synthetic-browser-protected-value';
assert.ok(password, 'browser password is supplied by the Go harness');
assert.ok(mcpBearer, 'MCP bearer is supplied by the Go harness');
async function login(page, username) {
  await page.goto(`${baseURL}/login`);
  await page.getByLabel('Username').fill(username);
  await page.getByLabel(/^Password(?: \(required\))?$/).fill(password);
  await Promise.all([page.waitForURL(`${baseURL}/overview`), page.getByRole('button', { name: 'Sign in', exact: true }).click()]);
}
async function link(page, name) {
  const target=page.getByRole('link',{name,exact:true});
  await target.click();
  await page.waitForLoadState('networkidle');
}
async function candidate(page, id) {
  await page.getByLabel(/^Server ID(?: \(required\))?$/).fill(id);
  await page.getByLabel('Transport', { exact: true }).selectOption('stdio');
  await page.getByLabel('Command', { exact: true }).fill('unused-draft-command');
  await page.getByLabel('Arguments · one per line', { exact: true }).fill('unused-draft-argument');
  await page.getByLabel('Working directory', { exact: true }).fill('/unused-draft-directory');
  await page.locator('#env-key-0').fill('UNUSED_DRAFT_ENV');
  await page.locator('#env-value-0').fill('synthetic-unused-value');
  await page.getByLabel('Transport', { exact: true }).selectOption('http');
  await page.getByLabel('Server URL', { exact: true }).fill(workerURL);
  await page.locator('#header-key-0').fill('X-Worker');
  await page.locator('#header-kind-0').selectOption('protected');
  await page.locator('#header-value-0').fill(protectedValue);
}
async function mutation(page, button, path, status) {
  let response;
  try {
    [response] = await Promise.all([
      page.waitForResponse(r => r.request().method() === 'POST' && new URL(r.url()).pathname === path),
      page.getByRole('button',{name:button,exact:true}).click(),
    ]);
  } catch (error) {
    const invalid = await page.locator('input:invalid, select:invalid, textarea:invalid').evaluateAll(inputs => inputs.map(i=>({name:i.name,reason:i.validationMessage})));
    const control=await page.getByRole('button',{name:button,exact:true}).evaluate(b=>({action:new URL(b.form.action).pathname,method:b.form.method,formValid:b.form.checkValidity(),box:{x:b.getBoundingClientRect().x,y:b.getBoundingClientRect().y,width:b.getBoundingClientRect().width,height:b.getBoundingClientRect().height},hit:document.elementFromPoint(b.getBoundingClientRect().x+5,b.getBoundingClientRect().y+5)?.tagName}));
    throw new Error(`Mutation ${button} at ${new URL(page.url()).pathname}, invalid controls: ${JSON.stringify(invalid)}, control: ${JSON.stringify(control)}`,{cause:error});
  }
  if (response.status()!==status) {
    const fields=new URLSearchParams(response.request().postData()||'');
    const safe={version:fields.get('expected_version'),confirmation:fields.get('confirm'),enabled:fields.get('enabled'),hasCSRF:!!fields.get('csrf_token'),names:[...new Set(fields.keys())]};
    throw new Error(`${button} response ${response.status()}, expected ${status}; safe submitted metadata ${JSON.stringify(safe)}`);
  }
  if (path === '/mcp/connections') {
    const fields = new URLSearchParams(response.request().postData() || '');
    for (const name of ['command', 'args', 'directory', 'env_key', 'env_value']) {
      assert.equal(fields.has(name), false, `HTTP create/probe excludes inactive ${name} from the actual request`);
    }
  }
  if (status === 303) assert.equal((response.headers().location || '').includes(protectedValue),false,'protected input absent from redirect');
  else assert.equal((await response.text()).includes(protectedValue), false, 'protected input absent from response');
  if (path.endsWith('/diagnostic')) return;
  // Complete the HX-Location GET before operating on its replacement forms.
  await page.waitForLoadState('networkidle');
}
async function recorder(page, reject = false) {
  const response = await page.request.post(`${recorderURL}/__recorder/reset${reject ? '?reject' : ''}`);
  assert.equal(response.status(), 204, 'controlled recorder reset');
}
async function records(page) {
  const response = await page.request.get(`${recorderURL}/__recorder`);
  assert.equal(response.status(), 200, 'controlled recorder read');
  return response.json();
}
async function sameOriginPost(page, path, csrf) {
  return page.evaluate(async ({path, csrf}) => {
    const response = await fetch(path, {
      method: 'POST',
      headers: {'Content-Type': 'application/x-www-form-urlencoded'},
      body: new URLSearchParams({csrf_token: csrf}),
    });
    return response.status;
  }, {path, csrf});
}
async function assertNoCredentials(page) {
  const html = await page.content();
  assert.equal(html.includes(password), false, 'login password absent from rendered HTML');
  assert.equal(html.includes(mcpBearer), false, 'MCP bearer absent from rendered HTML');
}
async function configuredDiagnostic(browser) {
  assert.ok(recorderURL, 'diagnostic mode requires the controlled recorder URL');
  const context = await browser.newContext({ viewport: {width: 1440, height: 900} });
  const page = await context.newPage();
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));

  await recorder(page);
  await login(page, 'operator');
  const operatorCSRF = await page.locator('form[action$="/logout"] input[name="csrf_token"]').inputValue();
  assert.equal(await sameOriginPost(page, '/mcp/connections/config:knowl/diagnostic', operatorCSRF), 403, 'operator diagnostic is forbidden');
  assert.deepEqual(await records(page), [], 'operator rejection makes no MCP request');
  await Promise.all([page.waitForURL(`${baseURL}/login`), page.getByRole('button', {name:'Sign out', exact:true}).click()]);

  await login(page, 'administrator');
  await page.goto(`${baseURL}/mcp`);
  assert.equal(await page.getByRole('link', {name:'knowl', exact:true}).count(), 1, 'configured Knowl is listed');
  assert.equal(await page.getByRole('row', {name:/knowl/}).getByText('Not checked', {exact:true}).count(), 1, 'inventory uses truthful unobserved state');
  await link(page, 'knowl');
  await page.reload();
  await page.getByRole('heading', {name:'Configuration definition', exact:true}).waitFor();
  assert.equal(await page.getByText('Configuration · Read-only', {exact:true}).count(), 1, 'Knowl is configuration-owned');
  assert.equal(await page.getByText('Not checked', {exact:true}).count(), 1, 'passive detail is not checked');
  assert.equal(await page.locator('input[name="expected_version"]').count(), 0, 'configured detail has no edit form');
  assert.equal(await page.getByRole('heading', {name:'OAuth', exact:true}).count(), 0, 'static bearer definition is OAuth-neutral');
  assert.equal(await page.getByText(/Not configured|Authorization required/).count(), 0, 'static bearer definition has no OAuth status');
  assert.deepEqual(await records(page), [], 'inventory, navigation and reload are passive');
  await assertNoCredentials(page);

  const diagnosticPath = '/mcp/connections/config:knowl/diagnostic';
  assert.equal(await sameOriginPost(page, diagnosticPath, 'invalid-csrf'), 403, 'invalid CSRF is forbidden');
  assert.deepEqual(await records(page), [], 'invalid CSRF makes no MCP request');

  await mutation(page, 'Refresh state', diagnosticPath, 200);
  await page.getByText('Check succeeded', {exact:true}).waitFor();
  assert.equal(await page.getByText(/2 tools/).count(), 1, 'diagnostic displays the exact tool count');
  assert.deepEqual(await records(page), [
    {order: 1, category: 'jsonrpc', operation: 'server/discover', headerMatched: true},
    {order: 2, category: 'jsonrpc', operation: 'initialize', headerMatched: true},
    {order: 3, category: 'jsonrpc', operation: 'notifications/initialized', headerMatched: true},
    {order: 4, category: 'jsonrpc', operation: 'tools/list', headerMatched: true},
    {order: 5, category: 'http', operation: 'DELETE', headerMatched: true},
  ], 'diagnostic performs authenticated initialization, tools/list and bounded cleanup');
  await assertNoCredentials(page);

  await recorder(page);
  await page.reload();
  await page.getByText('Check succeeded', {exact:true}).waitFor();
  assert.equal(await page.getByText('Not checked', {exact:true}).count(), 1, 'runtime readiness remains separate');
  assert.deepEqual(await records(page), [], 'reload after success is passive');

  await recorder(page, true);
  await mutation(page, 'Refresh state', diagnosticPath, 200);
  await page.getByText('Check failed', {exact:true}).waitFor();
  assert.equal(await page.getByText(/Unauthorized|401|Bearer/).count(), 0, 'failure is sanitized');
  assert.equal(await page.getByRole('heading', {name:'OAuth', exact:true}).count(), 0, 'failure does not offer OAuth remediation');
  assert.deepEqual(await records(page), [
    {order: 1, category: 'jsonrpc', operation: 'server/discover', headerMatched: true},
    {order: 2, category: 'jsonrpc', operation: 'initialize', headerMatched: true},
  ], 'controlled authentication failure is terminal');
  await assertNoCredentials(page);

  await recorder(page);
  await page.reload();
  await page.getByText('Check failed', {exact:true}).waitFor();
  assert.equal(await page.getByText('Not checked', {exact:true}).count(), 1, 'failed check does not replace runtime readiness');
  assert.deepEqual(await records(page), [], 'reload after failure is passive');
  assert.deepEqual(errors, [], 'configured diagnostic has no browser errors');
  await Promise.all([page.waitForURL(`${baseURL}/login`), page.getByRole('button', {name:'Sign out', exact:true}).click()]);
  await context.close();
}
(async () => {
 const browser = await chromium.launch({ headless: true });
 try {
  if (mode === 'diagnostic') {
    await configuredDiagnostic(browser);
    console.log('MCP configured diagnostic: ordinary login, passive reads, protected success/failure and retained process state passed');
    return;
  }
  for (const viewport of [{ width:1440,height:900 },{ width:390,height:844 }]) {
    console.log(`MCP browser context ${viewport.width}px`);
    const context = await browser.newContext({ viewport });
    const page = await context.newPage();
    const errors = [];page.on('pageerror', e => errors.push(e.message));
    await login(page, 'operator');
    assert.equal(await page.locator('[data-nav-link]').filter({hasText:'MCP'}).count(),0,'operator navigation has no MCP');
    const denied = await page.goto(`${baseURL}/mcp`);assert.equal(denied.status(),403);
    await page.goto(`${baseURL}/overview`);
    await Promise.all([page.waitForURL(`${baseURL}/login`),page.getByRole('button',{name:'Sign out',exact:true}).click()]);
    await login(page,'administrator');
    await page.goto(`${baseURL}/mcp`);
    await link(page,'knowl');
    await page.getByRole('heading',{name:'Configuration definition'}).waitFor();
    assert.equal(await page.getByRole('button',{name:'Save definition',exact:true}).count(),0);
    await link(page,'All MCP servers');
    await link(page,'Add server');
    const id=`browser-${viewport.width}`;
    await candidate(page,id);
    await mutation(page,'Probe candidate','/mcp/connections',200);
    await page.getByRole('status').filter({hasText:'Candidate probe succeeded'}).waitFor();
    assert.equal(await page.locator('input[data-mcp-secret]').evaluateAll(inputs => inputs.every(i => i.value === '')),true,'probe clears write-only input');
    await candidate(page,id);
    await mutation(page,'Create server','/mcp/connections',303);
    await page.waitForURL(/\/mcp\/connections\//);
    await page.getByRole('heading',{name:'Current state',exact:true}).waitFor();
    assert.equal(await page.locator('.status-chip').filter({hasText:/^Not checked$/}).count(),1,'new remote connection has no fabricated readiness observation');
    const detailPath=new URL(page.url()).pathname;
    const version=await page.locator('input[name="expected_version"]').first().inputValue();
    assert.equal(await page.locator('input[data-mcp-secret]').evaluateAll(inputs => inputs.every(i => i.value === '')),true);
    await page.locator('#header-value-0').fill(protectedValue);
    await link(page,'All MCP servers');
    await page.waitForURL(`${baseURL}/mcp`);
    await page.goBack();
    await page.waitForURL(`${baseURL}${detailPath}`);
    await page.getByRole('heading',{name:'Current state',exact:true}).waitFor();
    assert.equal(await page.locator('input[data-mcp-secret]').evaluateAll(inputs=>inputs.every(i=>i.value==='')),true,'Back does not restore protected input');
    await page.goForward();await page.waitForURL(`${baseURL}/mcp`);
    await link(page,id);
    await page.waitForURL(`${baseURL}${detailPath}`);
    assert.equal(await page.locator('input[data-mcp-secret]').evaluateAll(inputs=>inputs.every(i=>i.value==='')),true,'Forward/reopen has no protected input');
    await mutation(page,'Probe candidate',detailPath,200);
    await page.getByRole('status').filter({hasText:'Candidate probe succeeded'}).waitFor();
    assert.equal(await page.locator('input[name="expected_version"]').first().inputValue(),version,'probe does not save revision');
    await page.locator('input[name="expected_version"]').first().evaluate(i=>i.value='999999');
    await page.locator('#header-operation-0').selectOption('set');
    await page.locator('#header-value-0').fill(protectedValue);
    await mutation(page,'Save definition',detailPath,409);
    await page.locator('#request-error:not([hidden])').waitFor();
    assert.equal(await page.locator('input[data-mcp-secret]').evaluateAll(inputs=>inputs.every(i=>i.value==='')),true,'error clears write-only input');
    await page.goto(`${baseURL}${detailPath}`);
    await page.locator('#mcp-url').fill(workerURL);
    await mutation(page,'Save definition',detailPath,204);
    await page.getByRole('heading',{name:'Current state',exact:true}).waitFor();
    await page.waitForFunction(v=>document.querySelector('input[name="expected_version"]')?.value!==v,version);
    await page.locator('#mcp-confirm-selection').check();
    await mutation(page,'Disable connection',`${detailPath}/selection`,204);
    await page.locator('.status-chip').filter({hasText:/^Disabled$/}).waitFor();
    const beforeDisabledSave=await page.locator('input[name="expected_version"]').first().inputValue();
    await page.getByLabel('All providers',{exact:true}).focus();
    await page.keyboard.press('Space');
    await page.getByLabel('hosted',{exact:true}).focus();
    await page.keyboard.press('Space');
    await mutation(page,'Save definition',detailPath,204);
    await page.waitForFunction(v=>document.querySelector('input[name="expected_version"]')?.value!==v,beforeDisabledSave);
    await page.locator('.status-chip').filter({hasText:/^Disabled$/}).waitFor();
    assert.equal(await page.getByLabel('hosted',{exact:true}).isChecked(),true,'disabled save retains provider target');
    assert.equal(await page.getByLabel('All providers',{exact:true}).isChecked(),false);
    const disabledVersion=await page.locator('input[name="expected_version"]').first().inputValue();
    await mutation(page,'Probe candidate',detailPath,200);
    await page.locator('.status-chip').filter({hasText:/^Disabled$/}).waitFor();
    assert.equal(await page.locator('input[name="expected_version"]').first().inputValue(),disabledVersion,'disabled probe does not save');
    await page.locator('#mcp-confirm-selection').check();
    await mutation(page,'Enable connection',`${detailPath}/selection`,204);
    await page.waitForFunction(() => document.querySelector('.status-chip')?.textContent.trim() !== 'Disabled');
    const enabledStatus = (await page.locator('.status-chip').first().textContent()).trim();
    assert.equal(['Not checked', 'Available'].includes(enabledStatus), true, `enabled connection has a supported observed state, got ${enabledStatus}`);
    assert.equal(await page.getByLabel('hosted',{exact:true}).isChecked(),true,'enable retains provider target');
    await page.locator('#mcp-confirm-delete').check();
    await mutation(page,'Delete connection',`${detailPath}/delete`,204);
    await page.waitForURL(`${baseURL}/mcp`);
    assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true,'no document overflow');
    const restored=await page.request.get(`${baseURL}${detailPath}`,{headers:{'HX-Request':'true','HX-Target':'main-content','HX-History-Restore-Request':'true'}});
    assert.equal(restored.status(),200);assert.equal((await restored.text()).toLowerCase().includes('<!doctype html>'),true);
    assert.equal((await restored.text()).includes(protectedValue),false);
    assert.equal(await page.evaluate(()=>Object.values(sessionStorage).every(v=>!v.includes('synthetic-browser-protected-value'))),true,'HTMX history contains no protected input');
    assert.deepEqual(errors,[],'no browser errors');
    await Promise.all([page.waitForURL(`${baseURL}/login`),page.getByRole('button',{name:'Sign out',exact:true}).click()]);
    await context.close();
  }
  console.log('MCP actual runtime browser: ordinary admin/operator login, read-only config, native/HTMX CRUD, protected keep/replace, candidate probe, conflict, history and logout passed at desktop/mobile');
 } finally {await browser.close();}
})().catch(error=>{console.error(error);process.exitCode=1;});
