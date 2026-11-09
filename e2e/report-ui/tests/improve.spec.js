import { test, expect } from './fixtures.js';

test.use({ permissions: ['clipboard-read', 'clipboard-write'] });

const readClipboard = (page) => page.evaluate(() => navigator.clipboard.readText());

// Pages whose Improve dialog lists items. 'norun' has none to list on a fresh
// case, so it is in the no-items test below.
const withItems = ['full', 'capped', 'backoffice', 'keycloak', 'recorded'];

for (const name of withItems) {
  test.describe(`${name}: the Improve dialog`, () => {
    test('opens from the button, lists the items, and closes by x, Escape and the backdrop', async ({ page, report }) => {
      await report(name);
      const dialog = page.locator('#improve');
      await expect(dialog).toBeHidden();
      const open = page.locator('#view-now [data-open="improve"]');
      const count = Number(await open.locator('.count').innerText());
      expect(count).toBeGreaterThan(0);

      for (const close of [
        () => page.locator('#improve [data-close]').click(),
        () => page.keyboard.press('Escape'),
        () => page.mouse.click(2, 2),
      ]) {
        await open.click();
        await expect(dialog).toBeVisible();
        await expect(dialog.locator('.iitem')).toHaveCount(count);
        for (const item of await dialog.locator('.iitem').all()) {
          await expect(item.locator('pre').nth(0)).not.toBeEmpty(); // Goal
          await expect(item.locator('pre').nth(1)).not.toBeEmpty(); // Issue
          await expect(item.locator('pre').nth(2)).not.toBeEmpty(); // Fix
        }
        await close();
        await expect(dialog).toBeHidden();
      }
    });

    test('Copy all puts the whole list on the clipboard', async ({ page, report }) => {
      const r = await report(name);
      await page.locator('#view-now [data-open="improve"]').click();
      const copy = page.locator('#improve [data-copy-from]');
      await copy.click();
      // the "copied" mark lasts 1.5 seconds: look at it before anything slow
      await expect(copy).toHaveClass(/done/);

      const text = await readClipboard(page);
      expect(text.trim(), 'the clipboard is empty').not.toBe('');
      expect(text).toBe(await page.locator('#improve-text').inputValue());
      const parts = await page.locator('#improve .iitem pre').allTextContents();
      expect(parts.length, 'items in the dialog').toBeGreaterThan(0);
      for (const part of parts) {
        // the dialog appends the gain "(static +5.00)" to a goal; the copied text carries the goal alone
        const own = part.replace(/\s*[（(][^()（）]*\+\d[^()（）]*[)）]\s*$/, '').trim();
        expect(text, `"${own.slice(0, 40)}" is missing from what was copied`).toContain(own);
      }
      await r.expectNoBrokenText();
      expect(text).not.toMatch(/\bNaN\b|\bundefined\b|<no value>/);
    });
  });
}

test('the two paths: look at the levels, or copy the list', async ({ page, report }) => {
  await report('full');
  await page.locator('#view-now [data-open="improve"]').click();
  // path 1: the dialog sends the reader to the other view and closes
  await page.locator('#improve [data-goto="after"]').click();
  await expect(page.locator('#improve')).toBeHidden();
  await expect(page.locator('#view-after')).toBeVisible();
  await expect(page.locator('#view-now')).toBeHidden();
  // the second view has its own button for the same dialog
  await page.locator('#view-after [data-open="improve"]').click();
  await expect(page.locator('#improve')).toBeVisible();
  // path 2: copy the list from here too
  await page.locator('#improve [data-copy-from]').click();
  expect((await readClipboard(page)).trim()).not.toBe('');
});

test('a page with no second view offers only the copy path', async ({ page, report }) => {
  await report('recorded');
  await page.locator('#view-now [data-open="improve"]').click();
  await expect(page.locator('#improve [data-goto]')).toHaveCount(0);
  await expect(page.locator('#improve [data-copy-from]')).toBeVisible();
});

test('a per-check Copy prompt copies the prompt of the language on show', async ({ page, report }) => {
  const r = await report('full');
  const failed = page.locator('#view-now .chk', { has: page.locator('.ico.no') }).first();
  await failed.locator('> .acc-h').click();
  const btn = failed.locator('[data-copy]');
  const [zh, en] = await btn.evaluate((b) => [b.dataset.copy, b.dataset.copyEn]);
  expect(zh.trim()).not.toBe('');
  expect(en.trim()).not.toBe('');
  await r.setLang('en');
  await btn.click();
  expect(await readClipboard(page)).toBe(en);
  await r.setLang('zh');
  await btn.click();
  expect(await readClipboard(page)).toBe(zh);
});
