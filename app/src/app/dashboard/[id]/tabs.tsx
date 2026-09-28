"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";

export function Tabs({ id }: { id: string }) {
  const path = usePathname();
  const base = `/dashboard/${id}`;
  const tabs = [
    { href: base, label: "Overview", active: path === base },
    {
      href: `${base}/tables`,
      label: "Tables",
      active: path.startsWith(`${base}/tables`) && !path.startsWith(`${base}/tables/users`),
    },
    { href: `${base}/tables/users`, label: "Users", active: path.startsWith(`${base}/tables/users`) },
    { href: `${base}/usage`, label: "Usage", active: path.startsWith(`${base}/usage`) },
    { href: `${base}/backups`, label: "Backups", active: path.startsWith(`${base}/backups`) },
  ];
  return (
    <nav className="flex gap-1 overflow-x-auto border-b border-border">
      {tabs.map((t) => (
        <Link
          key={t.href}
          href={t.href}
          className={`-mb-px border-b-2 px-3 py-2 text-sm ${
            t.active ? "border-accent font-medium" : "border-transparent text-muted hover:text-foreground"
          }`}
        >
          {t.label}
        </Link>
      ))}
    </nav>
  );
}
