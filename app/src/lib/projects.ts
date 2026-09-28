import "server-only";
import { and, eq } from "drizzle-orm";
import { notFound } from "next/navigation";
import { cache } from "react";
import { db } from "@/db";
import { project } from "@/db/schema";
import { requireUser } from "./auth";

/**
 * The only way to load a project: by id AND the signed-in owner.
 * The browser never chooses which tenant it talks to.
 */
export const findOwnedProject = cache(async (id: string) => {
  const user = await requireUser();
  return db.query.project.findFirst({
    where: and(eq(project.id, id), eq(project.ownerId, user.id)),
  });
});

/** For pages: 404 if missing or not yours. */
export async function pageProject(id: string) {
  const p = await findOwnedProject(id);
  if (!p) notFound();
  return p;
}

/** For server actions: a controlled 404 if missing or not yours (not a generic 500). */
export async function actionProject(id: unknown) {
  const p = await findOwnedProject(String(id));
  if (!p) notFound();
  return p;
}
