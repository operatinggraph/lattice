// Regression test for facet-app-ux.md §3.3/§3.6's degraded-render contract
// ("Facet never crashes or blocks on an undescribed op; it degrades
// gracefully per the design's explicit contract") — edge-showcase-app-
// design.md §3's Fire-1 green-bar item ("an undescribed op degrades"),
// shipped without a regression test until Inc 4. No test framework exists
// elsewhere in this repo's JS surfaces, so this uses only Node's built-in
// test runner (`node --test`) — no new dependency.
//
// app.js is a plain browser script (function declarations + `const`s at
// module scope, no exports); vm.runInContext hoists its function
// declarations onto the sandbox object (const/let stay lexical and are NOT
// exposed — verified separately), which is enough to exercise opButton in
// isolation without a real DOM.

import { test } from "node:test";
import assert from "node:assert/strict";
import vm from "node:vm";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const appSrc = fs.readFileSync(path.join(__dirname, "app.js"), "utf8");

function loadApp() {
  const sandbox = {
    console,
    document: { addEventListener() {} },
  };
  vm.createContext(sandbox);
  vm.runInContext(appSrc, sandbox, { filename: "app.js" });
  return sandbox;
}

test("opButton degrades gracefully for an undescribed op (no dispatchClass)", () => {
  const { opButton } = loadApp();
  const html = opButton({ key: "manifest.op.abc", data: { operationType: "SomeUndescribedOp" } }, {});
  assert.match(html, /degraded-card/);
  assert.match(html, /Some Undescribed Op/);
  assert.match(html, /ask staff to help via the admin console/);
  assert.doesNotMatch(html, /data-open-op/);
});

test("opButton renders a normal submit button for a described op (has dispatchClass)", () => {
  const { opButton } = loadApp();
  const html = opButton(
    { key: "manifest.op.abc", data: { operationType: "RequestService", dispatchClass: "write", submitLabel: "Order" } },
    { serviceKey: "manifest.svc.xyz" }
  );
  assert.doesNotMatch(html, /degraded-card/);
  assert.match(html, /data-open-op="manifest\.op\.abc"/);
  assert.match(html, /data-service-key="manifest\.svc\.xyz"/);
  assert.match(html, />Order</);
});
