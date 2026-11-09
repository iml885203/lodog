import { test, expect } from './fixtures.js';
import { pageURL } from '../pages.js';

// The page `lodog review ask --html` writes (global-setup.js): a person who has
// never seen the YAML must see how to answer, each question in plain words and
// no wall of items.

const open = async (page) => {
  await page.goto(pageURL('review'));
  await expect(page.locator('html.js')).toHaveCount(1);

};

test('how to answer: both ways, the real file path and a copyable example', async ({ page }) => {
  await open(page);
  const how = page.locator('#how');
  await expect(how).toContainText('lodog-onboard');
  await expect(how).toContainText('lodog review apply');
  await expect(page.locator('#path')).toContainText(/\/review\.yaml$/);
  await expect(page.locator('#example')).toContainText('answer: {confirmed: true}');
  await expect(page.locator('#command')).toContainText('--by');
});

test('each question explains itself and its items start folded', async ({ page }) => {
  await open(page);
  const sections = page.locator('section');
  expect(await sections.count()).toBeGreaterThanOrEqual(3);
  for (const s of await sections.all()) {
    expect(await s.locator('dt').count()).toBeGreaterThanOrEqual(4);
    await expect(s.locator('details')).not.toHaveJSProperty('open', true);
  }
  const first = sections.first();
  const items = first.locator('li').first();
  await expect(items).toBeHidden();
  await first.locator('summary').click();
  await expect(items).toBeVisible();
  await first.locator('summary').click();
  await expect(items).toBeHidden();
});

test('the applied line reports the review log', async ({ page }) => {
  await open(page);
  await expect(page.locator('#applied')).toContainText('Ada');
  await expect(page.locator('#applied')).toContainText('2026-10-01');
});

test('switching language changes the text, leaves no key or placeholder, and survives a reload', async ({ page }) => {
  await open(page);
  const body = () => page.locator('main').textContent();
  const noLeaks = async () => {
    const t = await body();
    expect(t).not.toMatch(/\{\d\}|\bundefined\b|\bNaN\b/);
    const dict = await page.evaluate(() => JSON.parse(document.getElementById('i18n').textContent));
    expect(Object.keys(dict.en).sort()).toEqual(Object.keys(dict.zh).sort());
    const keys = await page.evaluate(() => [...document.querySelectorAll('.i[data-k]')].map((e) => [e.dataset.k, e.textContent]));
    for (const [k, text] of keys) expect(text, `key ${k} shown as text`).not.toBe(k);
  };
  await page.locator('[data-lang="zh"]').click();
  await expect(page.locator('html')).toHaveAttribute('lang', 'zh-Hant');
  await expect(page.locator('#how h2')).toHaveText('怎麼作答');
  await noLeaks();
  await page.locator('[data-lang="en"]').click();
  await expect(page.locator('html')).toHaveAttribute('lang', 'en');
  await expect(page.locator('#how h2')).toHaveText('How to answer');
  await expect(page.locator('section').first()).not.toContainText(/[一-龥]/);
  await noLeaks();
  await page.reload();
  await expect(page.locator('html')).toHaveAttribute('lang', 'en');
  await expect(page.locator('#how h2')).toHaveText('How to answer');
});

test('a Chinese browser starts in Chinese and an English one in English', async ({ browser }) => {
  for (const [locale, lang] of [['zh-TW', 'zh-Hant'], ['en-US', 'en']]) {
    const ctx = await browser.newContext({ locale });
    const page = await ctx.newPage();
    await page.goto(pageURL('review'));
    await expect(page.locator('html')).toHaveAttribute('lang', lang);
    await ctx.close();
  }
});

test('Copy puts the example on the clipboard', async ({ browser }) => {
  const ctx = await browser.newContext({ permissions: ['clipboard-read', 'clipboard-write'] });
  const page = await ctx.newPage();
  await page.goto(pageURL('review'));
  await page.locator('[data-copy="example"]').click();
  await expect(page.locator('[data-copy="example"] .i')).toHaveAttribute('data-k', 'copied');
  expect(await page.evaluate(() => navigator.clipboard.readText())).toContain('answer: {confirmed: true}');
  await ctx.close();
});

test('a Chinese browser that picks English keeps English after a reload', async ({ browser }) => {
  const ctx = await browser.newContext({ locale: 'zh-TW' });
  const page = await ctx.newPage();
  await page.goto(pageURL('review'));
  await expect(page.locator('html')).toHaveAttribute('lang', 'zh-Hant');
  await page.locator('[data-lang="en"]').click();
  await expect(page.locator('html')).toHaveAttribute('lang', 'en');
  await page.reload();
  await expect(page.locator('html')).toHaveAttribute('lang', 'en');
  await expect(page.locator('#how h2')).toHaveText('How to answer');
  await ctx.close();
});

test('the example comments follow the language', async ({ page }) => {
  await open(page);
  await page.locator('[data-lang="zh"]').click();
  await expect(page.locator('#example')).toContainText('# 對：每一項照建議');
  await page.locator('[data-lang="en"]').click();
  await expect(page.locator('#example')).toContainText('# right: every item as proposed');
  await expect(page.locator('#example')).not.toContainText('每一項');
});
