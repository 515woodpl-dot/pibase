"use server";

import { redirect } from "next/navigation";
import { requireUser } from "@/lib/auth";
import { deleteAccount } from "@/lib/lifecycle";
import { renderMail, sendMail } from "@/lib/mail";

export type DeleteState = { error?: string };

export async function deleteAccountAction(_: DeleteState, f: FormData): Promise<DeleteState> {
  const u = await requireUser();
  if (String(f.get("confirm") ?? "").trim().toLowerCase() !== u.email.toLowerCase()) return { error: "Type your email address exactly to confirm." };
  await deleteAccount(u.id);
  const m = renderMail({
    heading: "Your DATBase account was deleted",
    lines: ["Your DATBase account, its projects, their data, files and backups have been deleted (backups finish within a day if storage was busy).", "Thanks for trying DATBase."],
  });
  await sendMail({ to: u.email, subject: "Your DATBase account was deleted", ...m });
  redirect("/login?deleted=1");
}
