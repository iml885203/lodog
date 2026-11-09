import { test as base, expect } from '@playwright/test';
import { pageURL } from '../pages.js';

export { expect };

// The two ways a page shows a broken value, and the two ways it shows a key
// nobody translated (the key itself, or a placeholder never filled in).
const BROKEN_VALUE = /\bNaN\b|\bundefined\b|<no value>|\[object Object\]|\bInfinity\b/;
const UNFILLED_PLACEHOLDER = /\{\d+\}/;

export const test = base.extend({
  // Every test opens its page through this: the page is not clean while the
  // console, an uncaught exception or a request that cannot load says otherwise.
  problems: [
    async ({ page }, use) => {
      const problems = [];
      page.on('console', (m) => {
        if (m.type() === 'error' || m.type() === 'warning') problems.push(`console.${m.type()}: ${m.text()}`);
      });
      page.on('pageerror', (e) => problems.push(`pageerror: ${e.message}`));
      page.on('requestfailed', (r) => problems.push(`requestfailed: ${r.url()}`));
      await use(problems);
      expect(problems, 'console, uncaught errors and failed requests').toEqual([]);
    },
    { auto: true },
  ],

  // `report(name)` opens one generated page and waits for its script to run.
  report: async ({ page }, use) => {
    await use(async (name, hash = '') => {
      await page.goto(pageURL(name) + hash);
      await expect(page.locator('html.js')).toHaveCount(1);
      return new ReportPage(page);
    });
  },
});

// What the page itself says about its levels: the script reads the same JSON.
export class ReportPage {
  constructor(page) {
    this.page = page;
  }

  levels() {
    return this.page.evaluate(() => JSON.parse(document.getElementById('levels-data').textContent));
  }

  dict() {
    return this.page.evaluate(() => JSON.parse(document.getElementById('i18n').textContent));
  }

  // The number the dial must show for a level: the run's total when it was
  // measured, the static score when it was not.
  static expectedDial(level) {
    return (level.measured ? level.total : level.static).toFixed(1);
  }

  async setLang(lang) {
    await this.page.locator(`[data-lang="${lang}"]`).click();
    await expect(this.page.locator(`[data-lang="${lang}"]`)).toHaveAttribute('aria-pressed', 'true');
    await expect(this.page.locator('html')).toHaveAttribute('lang', lang === 'zh' ? 'zh-Hant' : 'en');
  }

  // Opens every accordion on the page (nested ones too) so hidden text is
  // part of what a check reads.
  async openEveryAccordion() {
    // one round trip: a nested accordion exists only once its parent is open,
    // so click until none is closed
    await this.page.evaluate(() => {
      for (let pass = 0; pass < 5; pass++) {
        const closed = [...document.querySelectorAll('.acc-h[aria-expanded="false"]')];
        if (!closed.length) return;
        for (const h of closed) h.click();
      }
    });
    await expect(this.page.locator('.acc-h[aria-expanded="false"]')).toHaveCount(0);
  }

  // No NaN, undefined or <no value> anywhere in what a reader can reach, and
  // no key shown instead of its text. Reads textContent, not innerText, so a
  // collapsed fold or a hidden level panel counts too.
  async expectNoBrokenText() {
    const { text, attrs, keyLeaks, dotted } = await this.page.evaluate(() => {
      const dict = JSON.parse(document.getElementById('i18n').textContent);
      const keys = new Set([...Object.keys(dict.zh), ...Object.keys(dict.en)]);
      const main = document.querySelector('main');
      const leaks = [];
      for (const el of main.querySelectorAll('*')) {
        if (el.children.length) continue;
        const t = el.textContent.trim();
        if (keys.has(t)) leaks.push(`${el.tagName.toLowerCase()}.${el.className}: ${t}`);
      }
      const attrs = [...main.querySelectorAll('[title],[aria-label]')].flatMap((e) => [e.title, e.getAttribute('aria-label')]).filter(Boolean);
      return { text: main.textContent, attrs, keyLeaks: leaks, dotted: document.title };
    });
    expect(text).not.toMatch(BROKEN_VALUE);
    expect(text).not.toMatch(UNFILLED_PLACEHOLDER);
    expect(attrs.join('\n')).not.toMatch(BROKEN_VALUE);
    expect(dotted).not.toMatch(BROKEN_VALUE);
    expect(keyLeaks, 'a dictionary key shown as text').toEqual([]);
  }

  // Every `data-k` and `data-ia-*` key the page uses has text in the language
  // on show, and the text is not the key.
  async expectEveryKeyTranslated(lang) {
    const missing = await this.page.evaluate((lang) => {
      const dict = JSON.parse(document.getElementById('i18n').textContent)[lang];
      const used = new Set();
      for (const el of document.querySelectorAll('[data-k]')) used.add(el.dataset.k);
      for (const el of document.querySelectorAll('*')) {
        for (const a of el.attributes) if (a.name.startsWith('data-ia-') && !a.name.endsWith('-a')) used.add(a.value);
      }
      used.add('title');
      return [...used].filter((k) => !(k in dict) || dict[k] === '');
    }, lang);
    expect(missing, `keys without ${lang} text`).toEqual([]);
  }
}
