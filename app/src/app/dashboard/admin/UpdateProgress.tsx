"use client";

import { useEffect, useRef, useState } from "react";

type UpdateState = {
  running: boolean;
  total: number;
  done: number;
  skipped: string[];
  failed: { slug: string; error: string }[];
  startedAt: string | null;
  finishedAt: string | null;
};

/** Polls /dashboard/admin/update-status every 2 s while an update is running, then stops. */
export function UpdateProgress({ initial }: { initial: UpdateState }) {
  const [st, setSt] = useState(initial);
  const intervalRef = useRef<ReturnType<typeof setInterval> | null>(null);

  useEffect(() => {
    // start polling only if something is (or was just) running
    if (!st.running && !st.startedAt) return;

    const poll = async () => {
      try {
        const res = await fetch("/dashboard/admin/update-status", { cache: "no-store" });
        if (!res.ok) return;
        const next: UpdateState = await res.json();
        setSt(next);
        if (!next.running && intervalRef.current) {
          clearInterval(intervalRef.current);
          intervalRef.current = null;
        }
      } catch {
        // network blip — keep polling
      }
    };

    intervalRef.current = setInterval(poll, 2000);
    // also kick off immediately so first render reflects reality
    void poll();

    return () => {
      if (intervalRef.current) clearInterval(intervalRef.current);
    };
    // Only re-run when running flips to true (a new update started)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [st.running]);

  if (!st.startedAt) return null;

  const pct = st.total > 0 ? Math.round((st.done / st.total) * 100) : 0;

  return (
    <div className="space-y-2" data-testid="update-progress">
      {/* Status line */}
      <p className="text-sm">
        {st.running ? (
          <>
            Updating… <strong>{st.done} of {st.total}</strong> done
            {st.skipped.length > 0 && (
              <span className="text-muted"> · skipped (busy): {st.skipped.join(", ")}</span>
            )}
          </>
        ) : (
          <>
            Update complete — <strong>{st.done} of {st.total}</strong> updated
            {st.skipped.length > 0 && (
              <span className="text-muted"> · skipped: {st.skipped.join(", ")}</span>
            )}
          </>
        )}
      </p>

      {/* Progress bar */}
      {st.total > 0 && (
        <div className="h-2 w-full rounded bg-border">
          <div
            className={`h-2 rounded transition-all duration-300 ${st.failed.length > 0 ? "bg-red-500" : "bg-brand-green"}`}
            style={{ width: `${pct}%` }}
          />
        </div>
      )}

      {/* Failures */}
      {st.failed.length > 0 && (
        <ul className="list-disc pl-5 text-sm text-red-600">
          {st.failed.map((f) => (
            <li key={f.slug}>
              <span className="font-mono">{f.slug}</span>: {f.error}
            </li>
          ))}
        </ul>
      )}

      {!st.running && st.failed.length === 0 && (
        <p className="text-xs text-success">All projects are running the current version.</p>
      )}
    </div>
  );
}
