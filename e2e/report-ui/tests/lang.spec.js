import { test, expect } from './fixtures.js';
import { pages, pageURL } from '../pages.js';

for (const name of Object.keys(pages)) {
  test(`${name}: both dictionaries carry the same keys, and every key the page uses has text`, async ({ report }) => {
    const r = await report(name);
    const dict = await r.dict();
    expect(Object.keys(dict.en).sort()).toEqual(Object.keys(dict.zh).sort());
    for (const lang of ['zh', 'en']) await r.expectEveryKeyTranslated(lang);
  });

  test(`${name}: switching language changes the page text and leaves no key or broken value`, async ({ page, report }) => {
    const r = await report(name);
    const hasAfter = (await page.locator('#tab-after').count()) > 0;
    const seen = {};
    for (const lang of ['zh', 'en', 'zh']) {
      await r.setLang(lang);
      await expect(page.locator('html')).toHaveAttribute('lang', lang === 'zh' ? 'zh-Hant' : 'en');
      await expect(page.locator('[data-lang="zh"]')).toHaveAttribute('aria-pressed', String(lang === 'zh'));
      await expect(page.locator('[data-lang="en"]')).toHaveAttribute('aria-pressed', String(lang === 'en'));
      // the words around the dial are written by the script, not the server
      const dial = await page.locator('#dial-now-block .badge2').innerText();
      const title = await page.title();
      expect(title.trim()).not.toBe('');
      if (seen[lang]) expect({ dial, title }, 'the same language reads the same twice').toEqual(seen[lang]);
      seen[lang] = { dial, title };
      await r.expectNoBrokenText();
      if (hasAfter) {
        await page.locator('#tab-after').click();
        for (const chip of await page.locator('.lvchip').all()) {
          await chip.click();
          await r.expectNoBrokenText();
        }
        await page.locator('#tab-now').click();
      }
    }
    expect(seen.zh.title, 'the title is the same in both languages').not.toBe(seen.en.title);
    expect(seen.zh.dial).not.toBe(seen.en.dial);
  });
}

test('the chosen language survives a reload', async ({ page, report }) => {
  const r = await report('full');
  await r.setLang('en');
  await page.reload();
  await expect(page.locator('html')).toHaveAttribute('lang', 'en');
  await r.setLang('zh');
  await page.reload();
  await expect(page.locator('html')).toHaveAttribute('lang', 'zh-Hant');
});

test('a Chinese browser starts in Chinese and an English one in English', async ({ browser }) => {
  for (const [locale, lang] of [['zh-TW', 'zh-Hant'], ['en-US', 'en']]) {
    const ctx = await browser.newContext({ locale });
    const page = await ctx.newPage();
    await page.goto(pageURL('full'));
    await expect(page.locator('html')).toHaveAttribute('lang', lang);
    await ctx.close();
  }
});

test('the labels of the text that follows the language read in the language, in both views', async ({ page, report }) => {
  const r = await report('full');
  await r.setLang('en');
  await expect(page.locator('#tab-now')).toHaveText('Your system today');
  await expect(page.locator('#tab-after')).toHaveText('With lodog');
  await r.setLang('zh');
  await expect(page.locator('#tab-now')).not.toHaveText('Your system today');
  // an aria-label and a title are translated too
  await expect(page.locator('nav.tabs2')).toHaveAttribute('aria-label', /\S/);
});
