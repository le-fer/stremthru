import { ChevronRight } from "lucide-react";
import { useState } from "react";

import { StoreStatsEntry, useStoreStats } from "@/api/stats";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { summarizeStoreStats } from "@/lib/store-stats";

export function StoreStatsCard() {
  const stats = useStoreStats();
  const [rechecksOpen, setRechecksOpen] = useState(false);

  const { rechecks, totalCalls, totalErrors } = summarizeStoreStats(
    stats.data?.stores ?? [],
  );
  const overallErrorRate =
    totalCalls > 0 ? ((totalErrors / totalCalls) * 100).toFixed(2) : "0";

  return (
    <Card className="py-4 sm:py-0">
      <CardHeader className="flex flex-col items-stretch border-b !p-0 sm:flex-row">
        <div className="flex flex-1 flex-col justify-center gap-1 px-6 pb-3 sm:pb-0">
          <CardTitle>Store Statistics</CardTitle>
          <CardDescription>
            API call performance (rolling 1-hour window)
          </CardDescription>
        </div>
        <div className="flex flex-wrap">
          {(
            [
              ["Total Calls", totalCalls.toLocaleString()],
              ["Total Errors", totalErrors.toLocaleString()],
              ["Error Rate", `${overallErrorRate}%`],
            ] as const
          ).map(([label, value]) => (
            <div
              className="flex flex-1 flex-col justify-center gap-1 border-l border-t px-6 py-4 text-left sm:border-l sm:border-t-0 sm:px-8 sm:py-6"
              key={label}
            >
              <span className="text-muted-foreground text-xs">{label}</span>
              <span className="text-lg font-bold leading-none sm:text-3xl">
                {stats.isLoading ? <Skeleton className="h-8 w-24" /> : value}
              </span>
            </div>
          ))}
        </div>
      </CardHeader>
      <CardContent className="px-2 pb-4">
        {stats.isLoading ? (
          <div className="space-y-2">
            <Skeleton className="h-8 w-full" />
            <Skeleton className="h-8 w-full" />
            <Skeleton className="h-8 w-full" />
          </div>
        ) : stats.data?.stores.length ? (
          <div>
            {stats.data.stores.map((store) => (
              <StoreSection key={store.name} store={store} />
            ))}
          </div>
        ) : (
          <p className="text-muted-foreground text-center text-sm">
            No store activity recorded yet
          </p>
        )}
        <Collapsible
          className="mt-3 border-t pt-2"
          onOpenChange={setRechecksOpen}
          open={rechecksOpen}
        >
          <CollapsibleTrigger className="hover:bg-muted/50 flex w-full items-center gap-2 rounded-md px-3 py-2 text-left text-sm font-medium">
            <ChevronRight
              className={`h-4 w-4 shrink-0 transition-transform ${rechecksOpen ? "rotate-90" : ""}`}
            />
            <span>Missing-file-list checks</span>
            <span className="text-muted-foreground ml-auto text-xs font-normal">
              Included in total calls
            </span>
          </CollapsibleTrigger>
          <CollapsibleContent>
            <div className="space-y-3 px-3 pb-2 pt-3">
              <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
                {[
                  ["Recheck-only calls", rechecks.only_calls],
                  ["Mixed calls", rechecks.mixed_calls],
                  ["Recheck call errors", rechecks.errors],
                  ["Recheck hash attempts", rechecks.hashes],
                ].map(([label, value]) => (
                  <div
                    className="bg-muted/30 rounded-md border p-3"
                    key={label}
                  >
                    <p className="text-muted-foreground text-xs">{label}</p>
                    <p className="mt-1 text-lg font-semibold tabular-nums">
                      {stats.isLoading ? "…" : value.toLocaleString()}
                    </p>
                  </div>
                ))}
              </div>
              <p className="text-muted-foreground max-w-3xl text-xs leading-relaxed">
                Direct provider calls on this instance only. Mixed calls also
                contain ordinary lookups, so these figures do not measure net
                additional traffic. Hash attempts count cache-check submissions,
                not unique hashes; subsequent file fetches count as calls but
                not again as hashes.
              </p>
            </div>
          </CollapsibleContent>
        </Collapsible>
      </CardContent>
    </Card>
  );
}

function StoreSection({ store }: { store: StoreStatsEntry }) {
  const [open, setOpen] = useState(false);
  const { methods } = store;
  const showRechecks = methods.some(
    (method) =>
      (method.rechecks?.only_calls ?? 0) + (method.rechecks?.mixed_calls ?? 0) >
      0,
  );

  return (
    <Collapsible onOpenChange={setOpen} open={open}>
      <CollapsibleTrigger className="hover:bg-muted/50 flex w-full items-center gap-2 rounded-md px-3 py-2 text-sm font-medium">
        <ChevronRight
          className={`h-4 w-4 transition-transform ${open ? "rotate-90" : ""}`}
        />
        <span className="capitalize">{store.name}</span>
        <span className="text-muted-foreground ml-auto text-xs">
          {methods.reduce((s, m) => s + m.total_count, 0).toLocaleString()}{" "}
          calls
        </span>
      </CollapsibleTrigger>
      <CollapsibleContent>
        <div className="px-2 pb-2">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Method</TableHead>
                <TableHead className="text-right">Calls</TableHead>
                <TableHead className="text-right">Errors</TableHead>
                {showRechecks && (
                  <>
                    <TableHead className="text-right">Recheck-only</TableHead>
                    <TableHead className="text-right">Mixed</TableHead>
                    <TableHead className="text-right">Recheck errors</TableHead>
                    <TableHead className="text-right">Hash attempts</TableHead>
                  </>
                )}
                <TableHead className="text-right">Error %</TableHead>
                <TableHead className="text-right">Avg (ms)</TableHead>
                <TableHead className="text-right">P50 (ms)</TableHead>
                <TableHead className="text-right">P95 (ms)</TableHead>
                <TableHead className="text-right">P99 (ms)</TableHead>
                <TableHead className="text-right">Min (ms)</TableHead>
                <TableHead className="text-right">Max (ms)</TableHead>
                <TableHead className="text-right">Req/Min</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {methods.map((method) => (
                <TableRow key={method.name}>
                  <TableCell className="font-medium">{method.name}</TableCell>
                  <TableCell className="text-right">
                    {method.total_count.toLocaleString()}
                  </TableCell>
                  <TableCell className="text-right">
                    {method.error_count.toLocaleString()}
                  </TableCell>
                  {showRechecks && (
                    <>
                      <TableCell className="text-right">
                        {(method.rechecks?.only_calls ?? 0).toLocaleString()}
                      </TableCell>
                      <TableCell className="text-right">
                        {(method.rechecks?.mixed_calls ?? 0).toLocaleString()}
                      </TableCell>
                      <TableCell className="text-right">
                        {(method.rechecks?.errors ?? 0).toLocaleString()}
                      </TableCell>
                      <TableCell className="text-right">
                        {(method.rechecks?.hashes ?? 0).toLocaleString()}
                      </TableCell>
                    </>
                  )}
                  <TableCell className="text-right">
                    {method.error_rate.toFixed(2)}
                  </TableCell>
                  <TableCell className="text-right">
                    {method.avg_duration_ms.toFixed(2)}
                  </TableCell>
                  <TableCell className="text-right">
                    {method.p50_duration_ms.toFixed(2)}
                  </TableCell>
                  <TableCell className="text-right">
                    {method.p95_duration_ms.toFixed(2)}
                  </TableCell>
                  <TableCell className="text-right">
                    {method.p99_duration_ms.toFixed(2)}
                  </TableCell>
                  <TableCell className="text-right">
                    {method.min_duration_ms.toFixed(2)}
                  </TableCell>
                  <TableCell className="text-right">
                    {method.max_duration_ms.toFixed(2)}
                  </TableCell>
                  <TableCell className="text-right">
                    {method.requests_per_minute.toFixed(2)}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      </CollapsibleContent>
    </Collapsible>
  );
}
