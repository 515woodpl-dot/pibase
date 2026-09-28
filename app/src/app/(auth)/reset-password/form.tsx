"use client";

import { useRouter } from "next/navigation";
import { useState } from "react";
import { authClient } from "@/lib/auth-client";

export function ResetPasswordForm({ token }: { token: string }) {
  const router = useRouter();
  const [error, setError] = useState<string | null>(null);
  const [pending, setPending] = useState(false);

  async function onSubmit(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const f = new FormData(e.currentTarget);
    const newPassword = String(f.get("password"));
    if (newPassword !== String(f.get("confirm"))) return setError("The passwords don't match.");
    setPending(true);
    setError(null);
    const { error } = await authClient.resetPassword({ newPassword, token });
    setPending(false);
    if (error) return setError(error.code === "INVALID_TOKEN" ? "This link has expired. Request a new one." : (error.message ?? "Something went wrong"));
    router.push("/login?reset=1");
  }

  return (
    <main className="flex flex-1 items-center justify-center p-4">
      <div className="w-full max-w-sm">
        <h1 className="mb-1 text-2xl font-semibold">Choose a new password</h1>
        <p className="mb-6 text-sm text-muted">You'll be signed out everywhere else.</p>
        <form onSubmit={onSubmit} className="card space-y-4">
          <label className="block space-y-1">
            <span className="text-sm">New password</span>
            <input name="password" type="password" required minLength={8} className="input" autoComplete="new-password" />
          </label>
          <label className="block space-y-1">
            <span className="text-sm">Repeat new password</span>
            <input name="confirm" type="password" required minLength={8} className="input" autoComplete="new-password" />
          </label>
          {error && <p className="text-sm text-red-600">{error}</p>}
          <button className="btn w-full" disabled={pending}>{pending ? "Please wait…" : "Save new password"}</button>
        </form>
      </div>
    </main>
  );
}
