// Offline pages use synthetic report fixtures. The backoffice and keycloak
// aliases exercise the same browser flows without shipping recorded live runs.
export const pages = {
  full: { kind: 'fixture', case: 'testdata/preview/shop', runs: 'testdata/preview/scenarios/full' },
  capped: { kind: 'fixture', case: 'testdata/preview/shop', runs: 'testdata/preview/scenarios/all-capped' },
  incomplete: { kind: 'fixture', case: 'testdata/preview/shop', runs: 'testdata/preview/scenarios/incomplete' },
  l0l3: { kind: 'fixture', case: 'testdata/preview/shop-l0-l3', runs: 'testdata/preview/scenarios/l0-l3' },
  norun: { kind: 'fixture', case: 'testdata/preview/shop', runs: 'testdata/preview/scenarios/none' },
  recorded: { kind: 'fixture', case: 'testdata/case', runs: 'testdata/recorded' },
  backoffice: { kind: 'fixture', case: 'testdata/preview/shop', runs: 'testdata/preview/scenarios/full' },
  keycloak: { kind: 'fixture', case: 'testdata/preview/shop', runs: 'testdata/preview/scenarios/full' },
  nobench: { kind: 'nobench' },
};

import path from 'node:path';
import { fileURLToPath } from 'node:url';

export const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
export const outDir = path.resolve(process.env.REPORT_UI_DIR || path.join(repoRoot, 'bin/report-ui'));
export const pageURL = (name) => `file://${path.join(outDir, `${name}.html`)}`;
