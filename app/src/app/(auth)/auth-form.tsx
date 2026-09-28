"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useState } from "react";
import { Logo } from "@/components/logo";
import { authClient } from "@/lib/auth-client";

export function AuthForm({ mode, notice, initialError }: { mode: "login" | "signup"; notice?: string; initialError?: string }) {
  const router = useRouter();
  const [error, setError] = useState<string | null>(initialError ?? null);
  const [pending, setPending] = useState(false);
  const [checkEmail, setCheckEmail] = useState<string | null>(null);
  const isSignup = mode === "signup";

  async function onSubmit(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setPending(true);
    setError(null);
    const f = new FormData(e.currentTarget);
    const email = String(f.get("email"));
    const password = String(f.get("password"));
    const { data, error } = isSignup
      ? await authClient.signUp.email({ email, password, name: String(f.get("name")), callbackURL: "/dashboard" })
      : await authClient.signIn.email({ email, password, callbackURL: "/dashboard" });
    setPending(false);
    if (error?.code === "EMAIL_NOT_VERIFIED") return setCheckEmail(email); // a new link was sent
    if (error) return setError(error.message ?? "Something went wrong");
    if (!data?.token) return setCheckEmail(email); // email confirmation required
    router.push("/dashboard");
    router.refresh();
  }

  if (checkEmail)
    return (
      <main className="flex flex-1 items-center justify-center p-4">
        <div className="card w-full max-w-sm space-y-3" data-testid="check-email">
          <h1 className="text-xl font-semibold">Check your email</h1>
          <p className="text-sm">
            We sent a confirmation link to <strong>{checkEmail}</strong>. Open it to finish {isSignup ? "creating your account" : "signing in"}.
          </p>
          <p className="text-sm text-muted">Nothing arrived after a few minutes? Check spam, then sign in again to get a new link.</p>
          <Link href="/login" className="text-sm text-accent-text hover:underline">Back to sign in</Link>
        </div>
      </main>
    );

  return (
    <main className="flex flex-1 items-center justify-center p-4">
      <div className="w-full max-w-sm">
        <h1 className="mb-2"><Logo size="lg" /></h1>
        <p className="mb-6 text-sm text-muted">
          {isSignup ? "Create your account" : "Sign in to your account"}
        </p>
        <form onSubmit={onSubmit} className="card space-y-4">
          {isSignup && (
            <label className="block space-y-1">
              <span className="text-sm">Name</span>
              <input name="name" required className="input" autoComplete="name" />
            </label>
          )}
          <label className="block space-y-1">
            <span className="text-sm">Email</span>
            <input name="email" type="email" required className="input" autoComplete="email" />
          </label>
          <label className="block space-y-1">
            <span className="text-sm">Password</span>
            <input
              name="password"
              type="password"
              required
              minLength={8}
              className="input"
              autoComplete={isSignup ? "new-password" : "current-password"}
            />
          </label>
          {notice && !error && <p className="text-sm text-success">{notice}</p>}
          {error && <p className="text-sm text-red-600">{error}</p>}
          <button className="btn w-full" disabled={pending}>
            {pending ? "Please wait…" : isSignup ? "Create account" : "Sign in"}
          </button>
        </form>
        {!isSignup && (
          <p className="mt-4 text-center text-sm">
            <Link href="/forgot-password" className="text-accent-text hover:underline">Forgot your password?</Link>
          </p>
        )}
        <p className="mt-4 text-center text-sm text-muted">
          {isSignup ? "Already have an account? " : "New here? "}
          <Link href={isSignup ? "/login" : "/signup"} className="text-accent-text hover:underline">
            {isSignup ? "Sign in" : "Create an account"}
          </Link>
        </p>
      </div>
    </main>
  );
}
