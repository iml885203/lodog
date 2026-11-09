import { test, expect, ReportPage } from './fixtures.js';

// Pages with an "after" view: a run for at least L0 and one more level.
const withAfter = ['full', 'capped', 'incomplete', 'l0l3', 'backoffice', 'keycloak'];

for (const name of withAfter) {
  test.describe(`${name}: the two views and the level chips`, () => {
    test('the view switch shows one view at a time and keeps it in the link', async ({ page, report }) => {
      await report(name);
      const now = page.locator('#view-now');
      const after = page.locator('#view-after');
      await expect(page.locator('#tab-now')).toHaveAttribute('aria-selected', 'true');
      await expect(now).toBeVisible();
      await expect(after).toBeHidden();

      await page.locator('#tab-after').click();
      await expect(page.locator('#tab-after')).toHaveAttribute('aria-selected', 'true');
      await expect(page.locator('#tab-now')).toHaveAttribute('aria-selected', 'false');
      await expect(after).toBeVisible();
      await expect(now).toBeHidden();
      await expect(page).toHaveURL(/#view=after&level=L\d/);

      await page.locator('#tab-now').click();
      await expect(now).toBeVisible();
      await expect(after).toBeHidden();
      await expect(page).toHaveURL(/#view=now$/);
    });

    test('the arrow keys move between the two views', async ({ page, report }) => {
      await report(name);
      await page.locator('#tab-now').focus();
      await page.keyboard.press('ArrowRight');
      await expect(page.locator('#view-after')).toBeVisible();
      await page.keyboard.press('ArrowLeft');
      await expect(page.locator('#view-now')).toBeVisible();
    });

    test('a link carrying the view and the level opens there', async ({ page, report }) => {
      await report(name, '#view=after&level=L2');
      await expect(page.locator('#view-after')).toBeVisible();
      await expect(page.locator('#chip-L2')).toHaveAttribute('aria-selected', 'true');
      await expect(page.locator('#lv-after-L2')).toBeVisible();
    });

    test('each level chip shows its own panel and a dial that matches the page data', async ({ page, report }) => {
      const r = await report(name);
      const levels = await r.levels();
      await page.locator('#tab-after').click();

      const chips = page.locator('.lvchip');
      const keys = await chips.evaluateAll((els) => els.map((e) => e.dataset.key));
      expect(keys).toEqual(['L0', 'L1', 'L2', 'L3']);

      const seen = { dial: new Set(), words: new Set() };
      for (const key of keys) {
        await page.locator(`#chip-${key}`).click();
        await expect(page.locator(`#chip-${key}`)).toHaveAttribute('aria-selected', 'true');
        // exactly one level panel is on show: the chip's
        await expect(page.locator('.lv:not([hidden])')).toHaveCount(1);
        await expect(page.locator(`#lv-after-${key}`)).toBeVisible();
        // the dial and the words around it follow the chip
        await expect(page.locator('#dial-after .big')).toHaveText(ReportPage.expectedDial(levels[key]));
        await expect(page.locator('#dial-after-block .pill')).toContainText(key);
        seen.dial.add(await page.locator('#dial-after .big').innerText());
        seen.words.add(await page.locator('#dial-after-block .badge2').innerText());
        // a capped level shows the cap's cost, an uncapped one does not
        await expect(page.locator('#dial-after .big')).toHaveClass(levels[key].capped ? /capped/ : /^(?!.*capped)/);
        await expect(page.locator('#dial-after-block .capline .dot')).toHaveCount(levels[key].measured && levels[key].capped ? 1 : 0);
      }
      expect(seen.words.size, 'the text beside the dial is the same for every level').toBe(4);
      // the numbers cannot all be one number unless the page data says so
      const distinct = new Set(keys.map((k) => ReportPage.expectedDial(levels[k])));
      expect(seen.dial.size).toBe(distinct.size);
    });

    test('the arrow keys move between the level chips', async ({ page, report }) => {
      await report(name);
      await page.locator('#tab-after').click();
      await page.locator('.lvchip[aria-selected="true"]').focus();
      const before = await page.locator('.lvchip[aria-selected="true"]').getAttribute('data-key');
      await page.keyboard.press('ArrowRight');
      const after = await page.locator('.lvchip[aria-selected="true"]').getAttribute('data-key');
      expect(after).not.toBe(before);
      await expect(page.locator(`#lv-after-${after}`)).toBeVisible();
    });
  });
}

// The dial is a fact of the run: it must agree with the table of scores that
// the server wrote, not only with the JSON the script reads.
for (const name of ['full', 'backoffice', 'keycloak']) {
  test(`${name}: a measured level's dial equals its total in the scores table`, async ({ page, report }) => {
    const r = await report(name);
    const levels = await r.levels();
    const head = await page.locator('#points thead th').allTextContents();
    const totalRow = await page.locator('#points tbody tr', { hasText: /^(Total|總分)/ }).first().locator('th, td').allTextContents();
    expect(totalRow.length, 'a total row').toBeGreaterThan(1);
    await page.locator('#tab-after').click();
    for (const [i, h] of head.entries()) {
      const key = Object.keys(levels).find((k) => h.trim().startsWith(k));
      if (!key || !levels[key].measured) continue;
      const cell = Number.parseFloat(totalRow[i]);
      await page.locator(`#chip-${key}`).click();
      await expect(page.locator('#dial-after .big')).toHaveText(levels[key].total.toFixed(1));
      expect(Math.abs(cell - levels[key].total), `${key}: table says ${totalRow[i]}`).toBeLessThan(0.06);
    }
  });
}
