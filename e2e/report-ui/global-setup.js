import { execFileSync } from 'node:child_process';
import { mkdirSync, writeFileSync, rmSync, mkdtempSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { pages, repoRoot as root, outDir as out } from './pages.js';


// What `lodog enrich`, `workflows draft` and `policy draft` write; they ask a
// model, and the report only reads the files.
const specFiles = {
  'overlay.yaml': `overlay: 1.0.0
info: {title: petshop overlay, version: "1"}
actions:
  - target: $.paths['/pet'].post
    update:
      summary: Add a pet to the store.
`,
  'workflows.arazzo.yaml': `arazzo: 1.0.1
info: {title: petshop task tools, version: "1", description: Start with get_pet.}
sourceDescriptions:
  - {name: shop, url: openapi.json, type: openapi}
workflows:
  - workflowId: get_pet
    summary: Read one pet.
    inputs:
      type: object
      properties:
        petId: {type: integer, description: The pet's id.}
      required: [petId]
    steps:
      - stepId: read
        operationPath: '{$sourceDescriptions.shop.url}#/paths/~1pet~1{petId}/get'
        parameters:
          - {name: petId, in: path, value: $inputs.petId}
    x-agent: {risk: read}
`,
  'policy.yaml': `risks:
  read: allow
  write: approval
default: write
rules:
  - match: "GET /pet/*"
    risk: read
  - match: "GET /store/inventory"
    risk: read
  - match: "POST /pet"
    risk: write
`,
};

// What `lodog review ask` reads: a policy draft with one rule the model was
// unsure of, and a task tool waiting for review.
const reviewDrafts = {
  'policy.auto.yaml': `risks:
  irreversible: {action: approval, never_auto_approve: true}
  write: approval
  read: allow
default: write
rules:
  - {match: "DELETE /pet/{petId}", risk: irreversible, why: "heuristic: a DELETE"}
  - {match: "POST /pet", risk: write, why: "needs review: model, not confident: may create a pet"}
  - {match: "PUT /pet", risk: write, why: "heuristic: a PUT"}
  - {match: "GET /pet/{petId}", risk: read, why: "heuristic: a GET"}
`,
  'workflows.arazzo.yaml': specFiles['workflows.arazzo.yaml'].replace('    x-agent: {risk: read}', '    x-agent: {risk: read, needsReview: true, reviewNote: "Is petId a database id?"}'),
};

// The review page, with a review log next to its checklist so the page has an
// applied line to show.
function reviewPage(bin, out) {
  const work = mkdtempSync(path.join(tmpdir(), 'report-ui-review-'));
  try {
    for (const [f, body] of Object.entries(reviewDrafts)) writeFileSync(path.join(work, f), body);
    writeFileSync(path.join(work, 'review-log.yaml'), '- {at: 2026-10-01T08:30:00Z, by: Ada, question: policy-money, ask: a, file: f, wrote: w, proposed: money, items: 2}\n');
    execFileSync(bin, ['review', 'ask', '--policy', 'policy.auto.yaml', '--workflows', 'workflows.arazzo.yaml', '-o', 'review.yaml', '--html', path.join(out, 'review.html')], { cwd: work, stdio: ['ignore', 'pipe', 'inherit'] });
  } finally {
    rmSync(work, { recursive: true, force: true });
  }
}

export default function globalSetup() {
  const bin = process.env.LODOG_BIN ? path.resolve(process.env.LODOG_BIN) : path.join(root, 'bin/lodog');
  rmSync(out, { recursive: true, force: true });
  mkdirSync(out, { recursive: true });
  const run = (args, cwd = root) => execFileSync(bin, args, { cwd, stdio: ['ignore', 'pipe', 'inherit'] });

  for (const [name, p] of Object.entries(pages)) {
    const file = path.join(out, `${name}.html`);
    if (p.kind === 'nobench') {
      const work = mkdtempSync(path.join(tmpdir(), 'report-ui-'));
      try {
        run(['init', 'petshop', '--swagger', path.join(root, 'internal/initcase/testdata/petshop.json'), '--upstream', 'https://petshop.test/api/v3'], work);
        for (const [f, body] of Object.entries(specFiles)) writeFileSync(path.join(work, 'petshop/spec', f), body);
        run(['report', '--case', 'petshop', '--runs', 'no-runs', '-o', file], work);
      } finally {
        rmSync(work, { recursive: true, force: true });
      }
      continue;
    }
    run(['report', '--case', p.case, '--runs', p.runs, '-o', file], p.kind === 'fixture' ? path.join(root, 'internal/report') : root);
  }
  reviewPage(bin, out);
}
