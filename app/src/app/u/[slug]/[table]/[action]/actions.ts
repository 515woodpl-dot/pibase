"use server";

import { confirm, linkTarget, type Action } from "./shared";

export type ConfirmState = { done?: boolean; error?: string };

export async function submitConfirm(_: ConfirmState, f: FormData): Promise<ConfirmState> {
  const get = (k: string) => String(f.get(k) ?? "");
  const [slug, table, action, token] = [get("slug"), get("table"), get("action"), get("token")];
  if (!(await linkTarget(slug, table, action)) || action === "verify" || !token) return { error: "This link is invalid." };
  const password = get("password");
  if (action === "reset-password") {
    if (password !== get("passwordConfirm")) return { error: "The passwords don't match." };
    const err = await confirm(slug, table, action as Action, { token, password, passwordConfirm: password });
    return err ? { error: err } : { done: true };
  }
  const err = await confirm(slug, table, action as Action, { token, password });
  return err ? { error: err } : { done: true };
}
