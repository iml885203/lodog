import { test, expect, ReportPage } from './fixtures.js';

// With motion on, the dial tweens for a third of a second. The end state is
// what a reader sees, so it is what is asserted.
test.use({ reducedMotion: 'no-preference' });

test('with motion on, the dial settles on the chosen level and the language switch lands', async ({ page, report }) => {
  const r = await report('full');
  const levels = await r.levels();
  await page.locator('#tab-after').click();
  for (const key of ['L3', 'L0', 'L2']) {
    await page.locator(`#chip-${key}`).click();
    await expect(page.locator('#dial-after .big')).toHaveText(ReportPage.expectedDial(levels[key]));
  }
  await page.locator('[data-lang="en"]').click();
  await expect(page.locator('html')).toHaveAttribute('lang', 'en');
  await expect(page.locator('#tab-now')).toHaveText('Your system today');
  await expect(page.locator('main')).not.toHaveClass(/lang(out|in)/);
  await r.expectNoBrokenText();
});

test('with motion on, the first view settles on the measured number', async ({ page, report }) => {
  const r = await report('backoffice');
  const levels = await r.levels();
  const now = await page.locator('#dial-now').getAttribute('data-key');
  await expect(page.locator('#dial-now .big')).toHaveText(ReportPage.expectedDial(levels[now]));
});
