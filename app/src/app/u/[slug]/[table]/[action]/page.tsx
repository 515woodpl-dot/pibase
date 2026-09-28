import type { Metadata } from "next";
import { ConfirmForm } from "./form";
import { confirm, linkTarget } from "./shared";

// Public pages that a project's app users reach from DATBase emails (verify email, reset password,
// confirm a new email). They talk to the project's backend server-side; no DATBase account involved.
export const metadata: Metadata = { robots: { index: false, follow: false }, referrer: "no-referrer" };

type Params = { slug: string; table: string; action: string };

export default async function EmailLinkPage({ params, searchParams }: { params: Promise<Params>; searchParams: Promise<Record<string, string | string[] | undefined>> }) {
  const { slug, table, action } = await params;
  const token = (await searchParams).token;
  const p = await linkTarget(slug, table, action);

  let title: string;
  let body: React.ReactNode;
  if (!p || typeof token !== "string" || !token) {
    title = "Link not valid";
    body = <p className="text-sm">This link is invalid or has expired. Request a new one from the app.</p>;
  } else if (action === "verify") {
    const err = await confirm(slug, table, "verify", { token });
    title = err ? "Link not valid" : "Email confirmed";
    body = <p className="text-sm" data-testid={err ? undefined : "confirm-done"}>{err ?? `Thanks! Your email is confirmed. You can return to ${p.name} and sign in.`}</p>;
  } else {
    title = action === "reset-password" ? "Choose a new password" : "Confirm your new email";
    body = <ConfirmForm slug={slug} table={table} action={action as "reset-password" | "confirm-email"} token={token} appName={p.name} />;
  }

  return (
    <main className="flex flex-1 items-center justify-center p-4">
      <div className="w-full max-w-sm">
        {p && <p className="mb-1 text-sm text-muted">{p.name}</p>}
        <h1 className="mb-4 text-2xl font-semibold">{title}</h1>
        <div className="card">{body}</div>
        <p className="mt-6 text-center text-xs text-muted">Secured by DATBase</p>
      </div>
    </main>
  );
}
