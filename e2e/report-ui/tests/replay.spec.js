import { test, expect } from './fixtures.js';

// The trap replay: the same dangerous request, once with the legacy system
// open and once behind the gateway. It lives in the "after" view.
for (const name of ['full', 'backoffice', 'keycloak']) {
  test(`${name}: each trap tab shows its own task, the open side and the guarded side`, async ({ page, report }) => {
    await report(name);
    await page.locator('#tab-after').click();
    const replay = page.locator('#replay');
    await replay.scrollIntoViewIfNeeded();
    await expect(replay.locator('#no-replay')).toHaveCount(0);

    const tabs = replay.locator('[role="tab"]');
    const n = await tabs.count();
    expect(n, 'a trap to replay').toBeGreaterThan(0);
    const asks = new Set();
    for (let i = 0; i < n; i++) {
      const tab = tabs.nth(i);
      const id = await tab.getAttribute('aria-controls');
      await tab.click();
      await expect(tab).toHaveAttribute('aria-selected', 'true');
      // one panel on show, and it is this tab's
      await expect(replay.locator('.trap:not([hidden])')).toHaveCount(1);
      const panel = page.locator(`#${id}`);
      await expect(panel).toBeVisible();
      const ask = (await panel.locator('.ask').innerText()).trim();
      expect(ask).toContain((await tab.innerText()).trim());
      asks.add(ask);

      const open = panel.locator('.side.open');
      const guarded = panel.locator('.side.guarded');
      await expect(open).toBeVisible();
      await expect(guarded).toBeVisible();
      await expect(open.locator('.calls li').first()).toBeVisible();
      await expect(guarded.locator('.calls li').first()).toBeVisible();
      // the open side did the dangerous thing, the guarded side did not
      await expect(open.locator('.verdict.bad')).toHaveCount(1);
      await expect(open.locator('li.danger').first()).toBeVisible();
      await expect(guarded.locator('.verdict.good')).toHaveCount(1);
      // every call line names a method, a path and a status
      for (const li of await panel.locator('.calls li:not(.more)').all()) {
        await expect(li.locator('.m')).toHaveText(/^[A-Z]+$/);
        await expect(li.locator('.p')).toHaveText(/\S/);
        await expect(li.locator('.st')).toHaveText(/\S/);
      }
    }
    expect(asks.size, 'two tabs show the same task').toBe(n);
  });

  test(`${name}: the arrow keys move between the trap tabs`, async ({ page, report }) => {
    await report(name);
    await page.locator('#tab-after').click();
    const tabs = page.locator('#replay [role="tab"]');
    test.skip((await tabs.count()) < 2, 'one trap only');
    await tabs.first().focus();
    await page.keyboard.press('ArrowRight');
    await expect(tabs.nth(1)).toHaveAttribute('aria-selected', 'true');
    await expect(page.locator(`#${await tabs.nth(1).getAttribute('aria-controls')}`)).toBeVisible();
    await page.keyboard.press('ArrowLeft');
    await expect(tabs.first()).toHaveAttribute('aria-selected', 'true');
  });
}

test('a page with no run to replay says so instead of showing an empty replay', async ({ page, report }) => {
  await report('recorded');
  await expect(page.locator('#no-replay')).toBeVisible();
  await expect(page.locator('#replay')).toHaveCount(0);
});
