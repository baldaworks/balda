const assert = require('node:assert/strict');
const { chromium } = require('playwright');

(async () => {
  const browser = await chromium.launch({ headless: true });
  try {
    const page = await browser.newPage();
    await page.setContent('<span id="font-probe">Balda browser preflight</span>');
    const font = await page.locator('#font-probe').evaluate(async element => {
      await document.fonts.ready;
      const bounds = element.getBoundingClientRect();
      return {
        family: getComputedStyle(element).fontFamily,
        width: bounds.width,
        height: bounds.height,
        status: document.fonts.status,
      };
    });
    assert.equal(font.status, 'loaded', 'browser fonts must finish loading');
    assert.ok(font.family, 'browser must resolve a font family');
    assert.ok(font.width > 0 && font.height > 0, 'browser must render the font probe');
    console.log(`Browser preflight: Chromium launched and fonts rendered (${font.family})`);
  } finally {
    await browser.close();
  }
})().catch(error => {
  console.error(error);
  process.exitCode = 1;
});
