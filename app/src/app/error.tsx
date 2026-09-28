"use client";

// Friendly error page. Production builds never expose the error message or stack to the browser.
export default function ErrorPage({ reset }: { error: Error & { digest?: string }; reset: () => void }) {
  return (
    <main className="flex flex-1 items-center justify-center p-4">
      <div className="card max-w-sm space-y-3 text-center">
        <h1 className="text-lg font-semibold">Something went wrong</h1>
        <p className="text-sm text-muted">Please try again. If it keeps happening, the service may be busy.</p>
        <button onClick={reset} className="btn">
          Try again
        </button>
      </div>
    </main>
  );
}
