"use client";

import { useActionState } from "react";
import { deleteAccountAction, type DeleteState } from "./actions";

export function DeleteAccountForm({ email }: { email: string }) {
  const [state, run, pending] = useActionState<DeleteState, FormData>(deleteAccountAction, {});
  return (
    <form action={run} className="space-y-3" onSubmit={(e) => { if (!window.confirm("Delete your account and ALL projects? This can't be undone.")) e.preventDefault(); }}>
      <label className="block space-y-1">
        <span className="text-sm">Type <b>{email}</b> to confirm</span>
        <input name="confirm" className="input" autoComplete="off" />
      </label>
      {state.error && <p className="text-sm text-red-600">{state.error}</p>}
      <button className="btn-danger" disabled={pending}>{pending ? "Deleting…" : "Delete my account"}</button>
    </form>
  );
}
