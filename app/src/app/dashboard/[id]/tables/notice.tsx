import { notFound } from "next/navigation";
import type { Project } from "@/db/schema";
import { PbError } from "@/lib/pb";

/** Run a PocketBase read; return [data, errorMessage] so pages can show a friendly notice. */
export async function load<T>(fn: () => Promise<T>): Promise<[T, null] | [null, string]> {
  try {
    return [await fn(), null];
  } catch (e) {
    if (e instanceof PbError && e.status === 404) notFound(); // unknown/internal table or record
    if (e instanceof PbError) return [null, e.message];
    throw e;
  }
}

export function Notice({ project, error }: { project: Project; error?: string | null }) {
  const msg =
    project.status === "creating"
      ? "Your backend is still being set up."
      : project.status === "paused"
        ? "This project is paused. Resume it to manage its data."
        : project.status === "suspended"
          ? "This project is suspended by DATBase."
          : project.status === "error"
          ? "Setup failed. Retry it from the Overview tab."
          : error ?? "Something went wrong.";
  return <div className="card text-sm text-muted">{msg}</div>;
}
