import Link from "next/link";

// Own 404 page: matches the app and, unlike Next's default, uses no inline <style> (blocked by our CSP).
export default function NotFound() {
  return (
    <main className="flex flex-1 items-center justify-center p-4">
      <div className="card max-w-sm space-y-3 text-center">
        <h1 className="text-lg font-semibold">Not found</h1>
        <p className="text-sm text-muted">This page doesn&apos;t exist, or you don&apos;t have access to it.</p>
        <Link href="/dashboard" className="btn">
          Back to projects
        </Link>
      </div>
    </main>
  );
}
