import { readFileSync } from 'node:fs';
import { defineConfig } from '@playwright/test';

// Playwright sizes its workers from the host's core count, which a container
// reports whole (16 here) while its cgroup allows far fewer: eight Chromiums
// then share them and every test runs ten times slower. Size the workers from
// what the container may use: cgroup v2, then v1; when neither says (the CI
// pod sets a request, not a limit) two workers, which is what one or two cores
// carry.
function cgroupCpus() {
  const read = (f) => {
    try {
      return readFileSync(f, 'utf8').trim();
    } catch {
      return undefined;
    }
  };
  const v2 = read('/sys/fs/cgroup/cpu.max');
  if (v2 && !v2.startsWith('max')) {
    const [quota, period] = v2.split(' ').map(Number);
    return Math.max(1, Math.floor(quota / period));
  }
  const quota = Number(read('/sys/fs/cgroup/cpu/cpu.cfs_quota_us'));
  const period = Number(read('/sys/fs/cgroup/cpu/cpu.cfs_period_us'));
  if (quota > 0 && period > 0) return Math.max(1, Math.floor(quota / period));
  return process.env.CI ? 2 : undefined;
}

export default defineConfig({
  testDir: './tests',
  globalSetup: './global-setup.js',
  // Every page is a local file: a test that needs more than this is waiting for
  // something it should be asserting.
  timeout: 20_000,
  expect: { timeout: 5_000 },
  fullyParallel: true,
  workers: cgroupCpus(),
  forbidOnly: !!process.env.CI,
  reporter: process.env.CI ? [['list'], ['junit', { outputFile: 'results/junit.xml' }]] : 'list',
  outputDir: 'results/artifacts',
  use: {
    browserName: 'chromium',
    locale: 'en-US',
    viewport: { width: 1280, height: 900 },
    // The dial and the accordions tween; the tests assert the end state, and a
    // single test in animation.spec.js runs with motion on.
    reducedMotion: 'reduce',
    trace: 'retain-on-failure',
  },
});
