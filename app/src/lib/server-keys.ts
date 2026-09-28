import "server-only";
import { and, count, eq } from "drizzle-orm";
import { db } from "@/db";
import { serverKey, type Project } from "@/db/schema";
import { randomId, randomPassword } from "./crypto";
import { enc, json, pb, type Page } from "./pb";

export const MAX_KEYS = 10;
export const KEY_DAYS = [30, 90, 365] as const;
const KEY_EMAIL_DOMAIN = "keys.datbase.internal";
const ADMINS = "/api/collections/_superusers";

type AdminRecord = { id: string; email: string };

/** Create a server key: a dedicated internal admin account + a long-lived token for it. Returns the token once. */
export async function createServerKey(p: Project, name: string, days: number) {
  const [{ n }] = await db.select({ n: count() }).from(serverKey).where(eq(serverKey.projectId, p.id));
  if (n >= MAX_KEYS) throw new KeyError(`A project can have at most ${MAX_KEYS} server keys. Revoke one first.`);

  const id = randomId(16);
  const password = randomPassword(32); // never used or stored; the token is the credential
  const user = await pb<AdminRecord>(
    p,
    `${ADMINS}/records`,
    json("POST", { email: `key-${id}@${KEY_EMAIL_DOMAIN}`, password, passwordConfirm: password }),
  );
  try {
    const { token } = await pb<{ token: string }>(p, `${ADMINS}/impersonate/${enc(user.id)}`, json("POST", { duration: days * 86_400 }));
    await db.insert(serverKey).values({
      id,
      projectId: p.id,
      name,
      pbUserId: user.id,
      hint: token.slice(-6),
      expiresAt: new Date(Date.now() + days * 86_400_000),
    });
    return token;
  } catch (e) {
    await pb(p, `${ADMINS}/records/${enc(user.id)}`, { method: "DELETE" }).catch(() => {});
    throw e;
  }
}

/** Revoke a key: deleting its internal account invalidates the token immediately. */
export async function revokeServerKey(p: Project, keyId: string) {
  const k = await db.query.serverKey.findFirst({ where: and(eq(serverKey.id, keyId), eq(serverKey.projectId, p.id)) });
  if (!k) return;
  await pb(p, `${ADMINS}/records/${enc(k.pbUserId)}`, { method: "DELETE" }).catch((e) => {
    if (e?.status !== 404) throw e;
  });
  await db.delete(serverKey).where(eq(serverKey.id, k.id));
}

/**
 * Make the project's key accounts match DATBase's list. Needed after a restore: an older backup could
 * bring back a revoked key's account (re-enabling its token) or lack accounts for newer keys.
 * Also removes expired keys.
 */
export async function reconcileServerKeys(p: Project) {
  const rows = await db.query.serverKey.findMany({ where: eq(serverKey.projectId, p.id) });
  const filter = enc(`email ~ "@${KEY_EMAIL_DOMAIN}"`);
  const res = await pb<Page<AdminRecord>>(p, `${ADMINS}/records?perPage=500&filter=${filter}`);
  const now = Date.now();
  const valid = new Set(rows.filter((r) => r.expiresAt.getTime() > now).map((r) => r.pbUserId));

  for (const u of res.items) {
    if (!valid.has(u.id)) await pb(p, `${ADMINS}/records/${enc(u.id)}`, { method: "DELETE" }).catch(() => {});
  }
  const present = new Set(res.items.map((u) => u.id));
  for (const r of rows) {
    if (!present.has(r.pbUserId) || r.expiresAt.getTime() <= now) await db.delete(serverKey).where(eq(serverKey.id, r.id));
  }
}

export function listServerKeys(projectId: string) {
  return db.query.serverKey.findMany({ where: eq(serverKey.projectId, projectId), orderBy: (k, { desc }) => desc(k.createdAt) });
}

export class KeyError extends Error {}
