import Link from "next/link";
import { enc } from "@/lib/pb";
import { pageProject } from "@/lib/projects";
import { TableTabs } from "./table-tabs";

export default async function TableLayout({ children, params }: LayoutProps<"/dashboard/[id]/tables/[table]">) {
  const { id, table: raw } = await params;
  const p = await pageProject(id);
  const table = decodeURIComponent(raw);
  const base = `/dashboard/${p.id}/tables/${enc(table)}`;

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex items-center gap-2 text-sm">
          {table !== "users" && (
            <>
              <Link href={`/dashboard/${p.id}/tables`} className="text-muted hover:underline">
                Tables
              </Link>
              <span className="text-muted">/</span>
            </>
          )}
          <span className="font-mono font-medium">{table}</span>
        </div>
        {p.status === "active" && (
          <Link href={`${base}/new`} className="btn">
            + New {table === "users" ? "user" : "record"}
          </Link>
        )}
      </div>
      <TableTabs base={base} />
      {children}
    </div>
  );
}
