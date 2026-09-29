import { NextResponse } from "next/server";
import { isAdmin, requireUser } from "@/lib/auth";
import { notFound } from "next/navigation";
import { updateState } from "@/lib/lifecycle";

export const dynamic = "force-dynamic";

export async function GET() {
  const user = await requireUser();
  if (!isAdmin(user)) notFound();
  return NextResponse.json(updateState());
}
