const assert = require('node:assert/strict');
const { spawn } = require('node:child_process');
const path = require('node:path');
const { chromium } = require('playwright');

async function startPreview() {
  const server = spawn('go', ['run', './cmd/balda', 'backoffice', 'qa', 'serve', '--listen', '127.0.0.1:0'], {
    cwd: path.resolve(__dirname, '../..'),
    stdio: ['ignore', 'pipe', 'pipe'],
    detached: true,
  });
  let output = '';
  const url = await new Promise((resolve, reject) => {
    const timeout = setTimeout(() => reject(new Error(`QA preview did not start: ${output}`)), 90000);
    server.stdout.on('data', chunk => {
      output += chunk;
      const match = output.match(/Backoffice QA: (http:\/\/127\.0\.0\.1:\d+)\/qa\/ui\//);
      if (match) {
        clearTimeout(timeout);
        resolve(match[1]);
      }
    });
    server.stderr.on('data', chunk => { output += chunk; });
    server.once('exit', code => {
      clearTimeout(timeout);
      reject(new Error(`QA preview exited (${code}): ${output}`));
    });
  });
  return { server, url };
}

(async () => {
  let server;
  let browser;
  try {
    const preview = await startPreview();
    server = preview.server;
    const url = preview.url;
    browser = await chromium.launch({ headless: true });
    const page = await browser.newPage({ viewport: { width: 390, height: 844 } });
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));

    await page.goto(`${url}/qa/ui/`);
    await page.getByRole('heading', { name: 'Sign in' }).waitFor();
    await page.getByLabel('Username').fill('preview');
    await page.getByLabel('Password').fill('preview-password');
    await page.getByRole('button', { name: 'Sign in' }).click();
    await page.getByText('Signed in as').waitFor();
    assert.equal(new URL(page.url()).pathname, '/qa/ui/overview');

    await page.getByRole('link', { name: 'Account' }).click();
    await page.getByRole('heading', { name: 'Account' }).waitFor();
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
    assert.deepEqual(errors, []);
    console.log('Standalone Backoffice QA: login, authenticated navigation, and mobile layout passed');
  } finally {
    if (browser) await browser.close();
    if (server) {
      try { process.kill(-server.pid, 'SIGTERM'); } catch (_) {}
    }
  }
})().catch(error => {
  console.error(error);
  process.exitCode = 1;
});
