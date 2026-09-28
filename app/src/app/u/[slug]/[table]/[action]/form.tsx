"use client";

import { useActionState } from "react";
import { submitConfirm, type ConfirmState } from "./actions";

export function ConfirmForm(p: { slug: string; table: string; action: "reset-password" | "confirm-email"; token: string; appName: string }) {
  const [state, run, pending] = useActionState<ConfirmState, FormData>(submitConfirm, {});
  if (state.done)
    return (
      <p className="text-sm" data-testid="confirm-done">
        {p.action === "reset-password" ? "Your password was changed." : "Your new email address is confirmed."} You can now sign in to {p.appName}.
        You can close this page.
      </p>
    );
  return (
    <form action={run} className="space-y-4">
      {(["slug", "table", "action", "token"] as const).map((k) => (
        <input key={k} type="hidden" name={k} value={p[k]} />
      ))}
      <label className="block space-y-1">
        <span className="text-sm">{p.action === "reset-password" ? "New password" : "Your current password"}</span>
        <input name="password" type="password" required minLength={p.action === "reset-password" ? 8 : 1} className="input"
          autoComplete={p.action === "reset-password" ? "new-password" : "current-password"} />
      </label>
      {p.action === "reset-password" && (
        <label className="block space-y-1">
          <span className="text-sm">Repeat new password</span>
          <input name="passwordConfirm" type="password" required minLength={8} className="input" autoComplete="new-password" />
        </label>
      )}
      {state.error && <p className="text-sm text-red-600">{state.error}</p>}
      <button className="btn w-full" disabled={pending}>
        {pending ? "Please wait…" : p.action === "reset-password" ? "Save new password" : "Confirm new email"}
      </button>
    </form>
  );
}
