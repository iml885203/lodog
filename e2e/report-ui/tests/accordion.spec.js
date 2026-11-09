import { test, expect } from './fixtures.js';

const names = ['full', 'backoffice', 'keycloak', 'recorded'];

for (const name of names) {
  test.describe(`${name}: categories and checks`, () => {
    test('a category header counts the checks under it', async ({ page, report }) => {
      await report(name);
      const counts = await page.locator('#view-now').evaluate((view) => {
        const out = [];
        for (const h of view.querySelectorAll('h2.cat')) {
          const [passed, total] = h.querySelector('.cx').textContent.split('/').map(Number);
          let n = 0;
          let ok = 0;
          for (let e = h.nextElementSibling; e && !e.matches('h2.cat'); e = e.nextElementSibling) {
            if (!e.matches('.chk')) continue;
            n++;
            if (e.querySelector('.ico.ok')) ok++;
          }
          out.push({ name: h.querySelector('span').textContent, passed, total, n, ok });
        }
        return out;
      });
      expect(counts.length, 'categories on the page').toBeGreaterThan(0);
      for (const c of counts) {
        expect({ name: c.name, total: c.n, passed: c.ok }, c.name).toEqual({ name: c.name, total: c.total, passed: c.passed });
      }
    });

    test('a check opens on click, shows its goal, and closes on a second click', async ({ page, report }) => {
      await report(name);
      const check = page.locator('#view-now .chk').first();
      const head = check.locator('> .acc-h');
      const body = check.locator('> .acc-b');
      await expect(head).toHaveAttribute('aria-expanded', 'false');
      await expect(body.locator('.cb')).toBeHidden();

      await head.click();
      await expect(head).toHaveAttribute('aria-expanded', 'true');
      await expect(body).toHaveClass(/open/);
      await expect(body.locator('.cb')).toBeVisible();
      await expect(body.locator('.cb')).not.toBeEmpty();

      await head.click();
      await expect(head).toHaveAttribute('aria-expanded', 'false');
      await expect(body.locator('.cb')).toBeHidden();
    });

    test('a failed check offers a prompt to copy and its audit details open inside it', async ({ page, report }) => {
      await report(name);
      const failed = page.locator('#view-now .chk', { has: page.locator('.ico.no') }).first();
      await expect(failed, 'a failed check on this page').toHaveCount(1);
      await failed.locator('> .acc-h').click();
      await expect(failed.locator('.actions [data-copy]')).toBeVisible();
      const audit = failed.locator('.audit > .acc-h');
      if (await audit.count()) {
        await audit.click();
        await expect(audit).toHaveAttribute('aria-expanded', 'true');
        await expect(failed.locator('.audit .acc-in')).toBeVisible();
      }
    });
  });
}

test('every fold at the bottom opens and closes', async ({ page, report }) => {
  await report('full');
  await page.locator('#tab-after').click();
  for (const id of ['levels', 'gate', 'by-type', 'stability', 'static', 'l4', 'not-measured', 'guide']) {
    const fold = page.locator(`#${id}`).first();
    const head = fold.locator('> .acc-h');
    await head.scrollIntoViewIfNeeded();
    await head.click();
    await expect(head, id).toHaveAttribute('aria-expanded', 'true');
    await expect(fold.locator('> .acc-b .acc-in').first(), id).toBeVisible();
    await expect(fold.locator('> .acc-b .acc-in').first(), id).not.toBeEmpty();
    await head.click();
    await expect(head, id).toHaveAttribute('aria-expanded', 'false');
  }
});

test('the example before and after opens on a level that has one', async ({ page, report }) => {
  await report('backoffice');
  await page.locator('#tab-after').click();
  await page.locator('#chip-L3').click();
  const example = page.locator('.lv:not([hidden]) .example').first();
  await expect(example).toHaveCount(1);
  await example.locator('.acc-h').click();
  await expect(example.locator('.sidepair .cside')).toHaveCount(2);
  await expect(example.locator('.cside.before')).toBeVisible();
  await expect(example.locator('.cside.after')).toBeVisible();
});
