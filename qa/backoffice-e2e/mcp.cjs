const assert = require('node:assert/strict');
const { chromium } = require('playwright');
const [baseURL, workerURL] = process.argv.slice(2);
const password = 'correct-horse-battery';
const protectedValue = 'synthetic-browser-protected-value';
async function login(page, username) {
  await page.goto(`${baseURL}/login`);
  await page.getByLabel('Username').fill(username);
  await page.getByLabel(/^Password(?: \(required\))?$/).fill(password);
  await Promise.all([page.waitForURL(`${baseURL}/overview`), page.getByRole('button', { name: 'Sign in', exact: true }).click()]);
}
async function link(page, name) {
  const target=page.getByRole('link',{name,exact:true});
  if (page.mcpJavaScriptEnabled===false) {await target.focus();await page.keyboard.press('Enter');}
  else await target.click();
  await page.waitForLoadState('networkidle');
}
async function candidate(page, id) {
  await page.getByLabel('Server ID', { exact: true }).fill(id);
  await page.getByLabel('Transport', { exact: true }).selectOption('http');
  await page.getByLabel('Server URL', { exact: true }).fill(workerURL);
  await page.locator('#header-key-0').fill('X-Worker');
  await page.locator('#header-kind-0').selectOption('protected');
  await page.locator('#header-value-0').fill(protectedValue);
}
async function mutation(page, button, path, status) {
  if (page.mcpJavaScriptEnabled === false) {
    const viewport = page.viewportSize();
    await page.mouse.move(viewport.width * .7, viewport.height * .6);
    await page.mouse.wheel(0, 3000);
    await page.waitForTimeout(200);
  }
  let response;
  try {
    [response] = await Promise.all([
      page.waitForResponse(r => r.request().method() === 'POST' && new URL(r.url()).pathname === path),
      (async()=>{const target=page.getByRole('button',{name:button,exact:true});if(page.mcpJavaScriptEnabled===false){await target.focus();await page.keyboard.press('Enter');}else{await target.click();}})(),
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
  if (status === 303) assert.equal((response.headers().location || '').includes(protectedValue),false,'protected input absent from redirect');
  else assert.equal((await response.text()).includes(protectedValue), false, 'protected input absent from response');
  // Complete the HX-Location GET before operating on its replacement forms.
  await page.waitForLoadState('networkidle');
}
(async () => {
 const browser = await chromium.launch({ headless: true });
 try {
  for (const viewport of [{ width:1440,height:900 },{ width:390,height:844 }]) {
   for (const javaScriptEnabled of [true,false]) {
    console.log(`MCP browser context ${viewport.width}px JavaScript=${javaScriptEnabled}`);
    const context = await browser.newContext({ viewport, javaScriptEnabled });
    const page = await context.newPage();
    page.mcpJavaScriptEnabled = javaScriptEnabled;
    const errors = [];page.on('pageerror', e => errors.push(e.message));
    await login(page, 'operator');
    assert.equal(await page.locator('[data-nav-link]').filter({hasText:'MCP'}).count(),0,'operator navigation has no MCP');
    const denied = await page.goto(`${baseURL}/mcp`);assert.equal(denied.status(),403);
    await page.goto(`${baseURL}/overview`);
    await Promise.all([page.waitForURL(`${baseURL}/login`),page.getByRole('button',{name:'Sign out',exact:true}).click()]);
    await login(page,'administrator');
    await page.goto(`${baseURL}/mcp`);
    await link(page,'file-server');
    await page.getByRole('heading',{name:'Configuration definition'}).waitFor();
    assert.equal(await page.getByRole('button',{name:'Save definition',exact:true}).count(),0);
    await link(page,'All MCP servers');
    await link(page,'Add server');
    const id=`browser-${viewport.width}-${javaScriptEnabled}`;
    await candidate(page,id);
    await mutation(page,'Probe candidate','/mcp/connections',200);
    await page.getByRole('status').filter({hasText:'Candidate probe succeeded'}).waitFor();
    assert.equal(await page.locator('input[data-mcp-secret]').evaluateAll(inputs => inputs.every(i => i.value === '')),true,'probe clears write-only input');
    await candidate(page,id);
    await mutation(page,'Create server','/mcp/connections',javaScriptEnabled?204:303);
    await page.waitForURL(/\/mcp\/connections\//);
    await page.getByRole('heading',{name:'Current state',exact:true}).waitFor();
    assert.equal(await page.locator('.status-chip').filter({hasText:/^Ready$/}).count(),1,'real catalog discovers tools');
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
    await (javaScriptEnabled?page.locator('#request-error:not([hidden])'):page.getByRole('alert')).waitFor();
    assert.equal(await page.locator('input[data-mcp-secret]').evaluateAll(inputs=>inputs.every(i=>i.value==='')),true,'error clears write-only input');
    await page.goto(`${baseURL}${detailPath}`);
    await page.locator('#mcp-url').fill(workerURL);
    await mutation(page,'Save definition',detailPath,javaScriptEnabled?204:303);
    await page.getByRole('heading',{name:'Current state',exact:true}).waitFor();
    await page.waitForFunction(v=>document.querySelector('input[name="expected_version"]')?.value!==v,version);
    await page.locator('#mcp-confirm-selection').check();
    await mutation(page,'Disable connection',`${detailPath}/selection`,javaScriptEnabled?204:303);
    await page.locator('.status-chip').filter({hasText:/^Disabled$/}).waitFor();
    await page.locator('#mcp-confirm-delete').check();
    await mutation(page,'Delete connection',`${detailPath}/delete`,javaScriptEnabled?204:303);
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
  }
  console.log('MCP actual runtime browser: ordinary admin/operator login, read-only config, native/HTMX CRUD, protected keep/replace, candidate probe, conflict, history and logout passed at desktop/mobile');
 } finally {await browser.close();}
})().catch(error=>{console.error(error);process.exitCode=1;});
