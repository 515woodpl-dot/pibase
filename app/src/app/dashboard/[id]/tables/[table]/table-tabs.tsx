"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";

export function TableTabs({ base }: { base: string }) {
  const path = usePathname();
  const onSettings = path.endsWith("/settings");
  const cls = (active: boolean) =>
    `rounded-md px-3 py-1 text-sm ${active ? "bg-border/60 font-medium" : "text-muted hover:text-foreground"}`;
  return (
    <div className="flex gap-1">
      <Link href={base} className={cls(!onSettings)}>
        Data
      </Link>
      <Link href={`${base}/settings`} className={cls(onSettings)}>
        Settings
      </Link>
    </div>
  );
}
