import type { StoreRecheckStats, StoreStatsEntry } from "@/api/stats";

// Recheck figures are subsets of existing calls, never extra rows to sum into
// totalCalls. Optional fields keep the dashboard compatible with older servers.
export function summarizeStoreStats(stores: StoreStatsEntry[]) {
  const rechecks: StoreRecheckStats = {
    errors: 0,
    hashes: 0,
    mixed_calls: 0,
    only_calls: 0,
  };
  let totalCalls = 0;
  let totalErrors = 0;
  for (const store of stores) {
    for (const method of store.methods) {
      totalCalls += method.total_count;
      totalErrors += method.error_count;
      rechecks.only_calls += method.rechecks?.only_calls ?? 0;
      rechecks.mixed_calls += method.rechecks?.mixed_calls ?? 0;
      rechecks.errors += method.rechecks?.errors ?? 0;
      rechecks.hashes += method.rechecks?.hashes ?? 0;
    }
  }
  return { rechecks, totalCalls, totalErrors };
}
