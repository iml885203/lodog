import { test } from './fixtures.js';
import { pages } from '../pages.js';

// The sweep: every page, every language, every level, every accordion open.
// The console, uncaught errors and failed requests are checked for every test
// by the `problems` fixture.
for (const name of Object.keys(pages)) {
  test(`${name}: nothing reads NaN, undefined or <no value> anywhere on the page`, async ({ page, report }) => {
    const r = await report(name);
    const hasAfter = (await page.locator('#tab-after').count()) > 0;
    for (const lang of ['zh', 'en']) {
      await r.setLang(lang);
      await r.openEveryAccordion();
      await r.expectNoBrokenText();
      if (!hasAfter) continue;
      await page.locator('#tab-after').click();
      for (const chip of await page.locator('.lvchip').all()) {
        await chip.click();
        await r.openEveryAccordion();
        await r.expectNoBrokenText();
      }
      await page.locator('#tab-now').click();
    }
  });
}
