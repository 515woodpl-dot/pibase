import Link from "next/link";
import { isAdmin, requireUser } from "@/lib/auth";
import { Logo } from "@/components/logo";
import { SignOutButton } from "./ui";

export default async function DashboardLayout({ children }: LayoutProps<"/dashboard">) {
  const user = await requireUser();
  return (
    <>
      <header className="border-b border-border bg-card">
        <div className="mx-auto flex max-w-4xl items-center justify-between px-4 py-3">
          <Link href="/dashboard">
            <Logo />
          </Link>
          <div className="flex items-center gap-3 text-sm">
            {isAdmin(user) && (
              <Link href="/dashboard/admin" className="text-accent-text hover:underline">
                Server status
              </Link>
            )}
            <Link href="/dashboard/account" className="text-muted hover:underline" data-testid="account-link">
              <span className="hidden sm:inline">{user.email}</span>
              <span className="sm:hidden">Account</span>
            </Link>
            <SignOutButton />
          </div>
        </div>
      </header>
      <main className="mx-auto w-full max-w-4xl flex-1 px-4 py-8">{children}</main>
    </>
  );
}
