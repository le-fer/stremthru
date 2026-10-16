import assert from "node:assert/strict";
import { Buffer } from "node:buffer";
import { readFileSync } from "node:fs";
import test from "node:test";
import ts from "typescript";

// Use the existing compiler so these tests also run on Node 20, without adding
// a loader dependency or requiring native TypeScript execution.
const source = readFileSync(
  new URL("./src/lib/store-stats.ts", import.meta.url),
  "utf8",
);
const compiled = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.ESNext },
}).outputText;
const { summarizeStoreStats } = await import(
  "data:text/javascript;base64," + Buffer.from(compiled).toString("base64")
);

test("recheck subsets do not inflate totals or count file-fetch hashes again", () => {
  const result = summarizeStoreStats([
    {
      methods: [
        {
          error_count: 1,
          rechecks: { errors: 1, hashes: 5, mixed_calls: 1, only_calls: 1 },
          total_count: 3,
        },
        {
          error_count: 1,
          rechecks: { errors: 1, hashes: 0, mixed_calls: 0, only_calls: 2 },
          total_count: 2,
        },
      ],
      name: "premiumize",
    },
    { methods: [{ error_count: 0, total_count: 10 }], name: "torbox" },
  ]);
  assert.deepEqual(result, {
    rechecks: { errors: 2, hashes: 5, mixed_calls: 1, only_calls: 3 },
    totalCalls: 15,
    totalErrors: 2,
  });
});

test("older servers and empty snapshots have zero recheck counters", () => {
  assert.deepEqual(summarizeStoreStats([]), {
    rechecks: { errors: 0, hashes: 0, mixed_calls: 0, only_calls: 0 },
    totalCalls: 0,
    totalErrors: 0,
  });
  assert.equal(
    summarizeStoreStats([
      { methods: [{ error_count: 2, total_count: 7 }], name: "old" },
    ]).rechecks.only_calls,
    0,
  );
});
