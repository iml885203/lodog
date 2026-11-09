import { test, expect } from './fixtures.js';

// A case made by `lodog init` without --with-bench: static scores only (#67).
test.describe('a page with no bench', () => {
  test('has the with-lodog view: L0 to L3 switch, each on the 30-point scale with its static checks', async ({ page, report }) => {
    const r = await report('nobench');
    const levels = await r.levels();
    await expect(page.locator('#tab-after')).toHaveCount(1);
    await page.locator('#tab-after').click();
    await expect(page.locator('#view-after')).toBeVisible();
    const keys = await page.locator('.lvchip').evaluateAll((els) => els.map((e) => e.dataset.key));
    expect(keys).toEqual(['L0', 'L1', 'L2', 'L3']);
    const shown = [];
    for (const key of keys) {
      await page.locator(`#chip-${key}`).click();
      await expect(page.locator(`#lv-after-${key}`)).toBeVisible();
      const lv = levels[key];
      expect(lv.measured, `${key} measured`).toBe(false);
      expect(lv.scale, `${key} scale`).toBe(30);
      // the dial number is the page data's static score, on a 30 scale
      await expect(page.locator('#dial-after .big')).toHaveText(lv.static.toFixed(1));
      await expect(page.locator('#dial-after .of')).toHaveText(/Static|靜態/);
      await expect(page.locator('#dial-after .endmax')).toHaveText('30');
      await expect(page.locator('#dial-after')).toHaveAttribute('data-scale', '30');
      const arc = parseFloat(((await page.locator('#dial-after .fillarc').getAttribute('stroke-dasharray')) || '').split(' ')[0]);
      expect(arc).toBeCloseTo((lv.static / 30) * 100, 1);
      // the chip carries the same number
      await expect(page.locator(`#chip-${key} .st`)).toContainText(lv.static.toFixed(1));
      // each level lists its static checks
      expect(await page.locator(`#lv-after-${key} .chk`).count(), `${key} checks`).toBeGreaterThan(0);
      shown.push(lv.static);
    }
    // lodog adds points level by level: the last level is above the first
    expect(shown[3]).toBeGreaterThan(shown[0]);
    await r.expectNoBrokenText();
  });

  test('the now view is the L0 static score out of 30, not a 100-point dial', async ({ page, report }) => {
    const r = await report('nobench');
    await expect(page.locator('#dial-now')).toHaveAttribute('data-scale', '30');
    await expect(page.locator('#dial-now .endmax')).toHaveText('30');
    await expect(page.locator('#dial-now-block')).not.toContainText('100');
    await r.expectNoBrokenText();
  });

  test('says there is no bench in one quiet line and shows static numbers', async ({ page, report }) => {
    const r = await report('nobench');
    await expect(page.locator('#no-bench')).toBeVisible();
    await expect(page.locator('#static-only')).toBeVisible();
    await expect(page.locator('.banner#static-only')).toHaveCount(0);
    await expect(page.locator('#static-only')).toContainText(/lodog init --with-bench/);
    await expect(page.locator('#dial-now .of')).toHaveText(/Static/);
    const levels = await r.levels();
    for (const k of Object.keys(levels)) expect(levels[k].measured, `${k} measured`).toBe(false);
    await expect(page.locator('#dial-now .big')).toHaveText((levels.L0.static).toFixed(1));
    await r.expectNoBrokenText();
  });

  test('keeps working in both languages and opens its checks and folds', async ({ page, report }) => {
    const r = await report('nobench');
    for (const lang of ['en', 'zh']) {
      await r.setLang(lang);
      await r.expectEveryKeyTranslated(lang);
      await r.openEveryAccordion();
      await r.expectNoBrokenText();
    }
  });

  test('the Improve dialog opens and Copy all copies something', async ({ page, report, context }) => {
    await context.grantPermissions(['clipboard-read', 'clipboard-write']);
    await report('nobench');
    await page.locator('[data-open="improve"]').first().click();
    await expect(page.locator('#improve')).toBeVisible();
    await expect(page.locator('#improve [data-goto="after"]')).toHaveCount(1);
    const items = await page.locator('#improve .iitem').count();
    if (items > 0) {
      await page.locator('#improve [data-copy-from]').click();
      expect((await page.evaluate(() => navigator.clipboard.readText())).trim()).not.toBe('');
    } else {
      await expect(page.locator('#improve .db .muted')).toBeVisible();
    }
  });
});

test('a page with a bench but no run says there is no run', async ({ page, report }) => {
  const r = await report('norun');
  await expect(page.locator('#static-only')).toBeVisible();
  await expect(page.locator('#no-bench')).toHaveCount(0);
  await expect(page.locator('#dial-now .of')).toHaveText(/Static/);
  await r.expectNoBrokenText();
});
