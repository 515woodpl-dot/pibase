"use client";

import Link from "next/link";
import { useState } from "react";
import { authClient } from "@/lib/auth-client";

export default function ForgotPasswordPage() {
  const [sent, setSent] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [pending, setPending] = useState(false);

  async function onSubmit(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setPending(true);
    setError(null);
    const email = String(new FormData(e.currentTarget).get("email"));
    const { error } = await authClient.requestPasswordReset({ email, redirectTo: "/reset-password" });
    setPending(false);
    if (error) return setError(error.message ?? "Something went wrong");
    setSent(email); // same answer whether or not the account exists
  }

  return (
    <main className="flex flex-1 items-center justify-center p-4">
      <div className="w-full max-w-sm">
        <h1 className="mb-1 text-2xl font-semibold">Reset your password</h1>
        {sent ? (
          <div className="card mt-4 space-y-2" data-testid="reset-sent">
            <p className="text-sm">
              If <strong>{sent}</strong> has a DATBase account, we sent it a link to choose a new password. The link works for 1 hour.
            </p>
            <Link href="/login" className="text-sm text-accent-text hover:underline">Back to sign in</Link>
          </div>
        ) : (
          <form onSubmit={onSubmit} className="card mt-4 space-y-4">
            <label className="block space-y-1">
              <span className="text-sm">Email</span>
              <input name="email" type="email" required className="input" autoComplete="email" />
            </label>
            {error && <p className="text-sm text-red-600">{error}</p>}
            <button className="btn w-full" disabled={pending}>{pending ? "Please wait…" : "Send reset link"}</button>
            <Link href="/login" className="block text-center text-sm text-accent-text hover:underline">Back to sign in</Link>
          </form>
        )}
      </div>
    </main>
  );
}
