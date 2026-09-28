import "server-only";
import { eq } from "drizzle-orm";
import { db } from "@/db";
import { project } from "@/db/schema";
import { pbInternalUrl } from "@/lib/config";

export const ACTIONS = ["verify", "reset-password", "confirm-email"] as const;
export type Action = (typeof ACTIONS)[number];

/** The project behind an email link, or null if the link can't be valid. */
export async function linkTarget(slug: string, table: string, action: string) {
  if (!/^[a-z0-9-]{1,40}$/.test(slug) || !/^[A-Za-z0-9][A-Za-z0-9_]{0,63}$/.test(table) || !ACTIONS.includes(action as Action)) return null;
  const p = await db.query.project.findFirst({ where: eq(project.slug, slug) });
  return p && p.status === "active" ? p : null;
}

/** Call the project's confirm endpoint. Returns an error message, or null on success. */
export async function confirm(slug: string, table: string, action: Action, body: Record<string, string>) {
  const path = { verify: "confirm-verification", "reset-password": "confirm-password-reset", "confirm-email": "confirm-email-change" }[action];
  const r = await fetch(`${pbInternalUrl(slug)}/api/collections/${encodeURIComponent(table)}/${path}`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify(body),
    signal: AbortSignal.timeout(10_000),
    cache: "no-store",
  }).catch(() => null);
  if (!r) return "The app's backend is not reachable right now. Try again in a few minutes.";
  if (r.ok) return null;
  const j = (await r.json().catch(() => null)) as { data?: Record<string, { message?: string }> } | null;
  if (r.status === 429) return "The app is temporarily unavailable. Try again later.";
  const field = j?.data?.password?.message ?? j?.data?.passwordConfirm?.message;
  if (field) return field;
  return "This link is invalid or has expired. Request a new one from the app.";
}
