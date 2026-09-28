"use client";

import { useRouter } from "next/navigation";
import { useState, useTransition } from "react";
import type { AdminState } from "./actions";

export function AdminButton({ label, run }: { label: string; run: () => Promise<AdminState> }) {
  const router = useRouter();
  const [state, setState] = useState<AdminState>({});
  const [pending, start] = useTransition();
  return (
    <div className="space-y-1">
      <button
        className="btn-ghost"
        disabled={pending}
        onClick={() =>
          start(async () => {
            setState(await run());
            router.refresh();
          })
        }
      >
        {pending ? "Working…" : label}
      </button>
      {state.message && <p className="text-xs text-success">{state.message}</p>}
      {state.error && <p className="text-xs text-red-600">{state.error}</p>}
    </div>
  );
}

/** A button that asks for a reason (suspend) before running. */
export function ReasonButton({ label, run, testId }: { label: string; run: (reason: string) => Promise<AdminState>; testId?: string }) {
  const router = useRouter();
  const [state, setState] = useState<AdminState>({});
  const [pending, start] = useTransition();
  return (
    <span className="inline-flex flex-col">
      <button
        data-testid={testId}
        className="btn-danger text-xs"
        disabled={pending}
        onClick={() => {
          const reason = window.prompt("Reason (shown to the owner):", "Violates the acceptable use policy.");
          if (reason === null) return;
          start(async () => {
            setState(await run(reason));
            router.refresh();
          });
        }}
      >
        {pending ? "…" : label}
      </button>
      {state.error && <span className="text-xs text-red-600">{state.error}</span>}
    </span>
  );
}
