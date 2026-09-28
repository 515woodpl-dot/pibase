import Link from "next/link";
import { ResetPasswordForm } from "./form";

export default async function ResetPasswordPage({ searchParams }: { searchParams: Promise<Record<string, string | string[] | undefined>> }) {
  const { token, error } = await searchParams;
  if (error || typeof token !== "string" || !token)
    return (
      <main className="flex flex-1 items-center justify-center p-4">
        <div className="card w-full max-w-sm space-y-3">
          <h1 className="text-xl font-semibold">Link expired</h1>
          <p className="text-sm">This reset link is invalid or has expired.</p>
          <Link href="/forgot-password" className="text-sm text-accent-text hover:underline">Send a new link</Link>
        </div>
      </main>
    );
  return <ResetPasswordForm token={token} />;
}
