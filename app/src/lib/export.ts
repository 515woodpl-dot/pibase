import "server-only";
import { PassThrough, Readable } from "node:stream";
import { once } from "node:events";
import { ZipFile } from "yazl";
import { eq } from "drizzle-orm";
import { db } from "@/db";
import { sqlFolder, sqlSnippet, type Project } from "@/db/schema";
import { controlHeaders, enc, fileToken, fileUrl, listCollections, pb, type Page, type RecordItem } from "./pb";

/**
 * A project's data as a portable zip (owner's "Download data"):
 *   README.txt, tables.json (every table and its fields),
 *   tables/<table>.json (all records, as the API returns them; no passwords),
 *   files/<table>/<recordId>/<fileName> (every uploaded file).
 * Streamed page by page, so large projects don't need memory. DATBase's own calls aren't counted.
 */
export function exportProject(p: Project): Readable {
  const zip = new ZipFile();
  const output = zip.outputStream as unknown as Readable;
  let token = { value: "", at: 0 };
  const freshToken = async () => {
    if (Date.now() - token.at > 60_000) token = { value: await fileToken(p), at: Date.now() }; // file tokens are short-lived
    return token.value;
  };
  void (async () => {
    try {
      const tables = await listCollections(p);
      zip.addBuffer(
        Buffer.from(
          `Data export of the DATBase project "${p.name}" (${new Date().toISOString()}).\n\n` +
            "tables.json         every table with its fields\n" +
            "tables/<name>.json  all records of the table (JSON array)\n" +
            "files/<table>/<record id>/<file name>  uploaded files, referenced by name in the records\n\n" +
            "Passwords of app users are never exported (they are stored only as one-way hashes).\n",
        ),
        "README.txt",
      );
      zip.addBuffer(
        Buffer.from(
          JSON.stringify(
            tables.map((t) => ({
              name: t.name,
              kind: t.type === "auth" ? "users" : "data",
              fields: t.fields.filter((f) => !f.hidden && f.type !== "password").map((f) => ({ name: f.name, type: f.type, required: !!f.required })),
            })),
            null,
            2,
          ),
        ),
        "tables.json",
      );

      const withFiles: { table: string; items: { id: string; names: string[] }[]; collectionId: string }[] = [];
      for (const t of tables) {
        const fileFields = t.fields.filter((f) => f.type === "file").map((f) => f.name);
        const out = new PassThrough();
        zip.addReadStream(out, `tables/${t.name}.json`);
        const files: { id: string; names: string[] }[] = [];
        out.write("[\n");
        let first = true;
        for (let page = 1; ; page++) {
          const r = await pb<Page<RecordItem>>(p, `/api/collections/${enc(t.name)}/records?page=${page}&perPage=500&skipTotal=1&sort=%40rowid`);
          for (const rec of r.items) {
            if (!out.write((first ? "" : ",\n") + JSON.stringify(rec))) await once(out, "drain");
            first = false;
            const names = fileFields.flatMap((f) => (Array.isArray(rec[f]) ? (rec[f] as string[]) : rec[f] ? [String(rec[f])] : []));
            if (names.length) files.push({ id: rec.id, names });
          }
          if (r.items.length < 500) break;
        }
        out.end("\n]\n");
        await once(out, "end").catch(() => {});
        if (files.length) withFiles.push({ table: t.name, items: files, collectionId: t.id });
      }

      for (const t of withFiles) {
        for (const rec of t.items) {
          for (const name of rec.names) {
            const res = await fetch(`${fileUrl(p, t.collectionId, rec.id, name)}?token=${enc(await freshToken())}`, {
              headers: controlHeaders(p.slug),
              signal: AbortSignal.timeout(120_000),
            });
            if (!res.ok || !res.body) continue; // a missing file shouldn't break the whole export
            const body = Readable.fromWeb(res.body as import("node:stream/web").ReadableStream);
            zip.addReadStream(body, `files/${t.table}/${rec.id}/${name}`);
            await once(body, "end");
          }
        }
      }
      // SQL snippets: exported as .sql files inside a sql-snippets/ folder tree.
      const folders = await db.query.sqlFolder.findMany({ where: eq(sqlFolder.projectId, p.id) });
      const snippets = await db.query.sqlSnippet.findMany({ where: eq(sqlSnippet.projectId, p.id) });
      if (snippets.length > 0) {
        // Build folder id → path map.
        const folderMap = new Map<string | null, string>();
        folderMap.set(null, "sql-snippets");
        // Resolve nested paths (up to 5 levels; sort by creation so parents come first).
        folders.sort((a, b) => a.createdAt.getTime() - b.createdAt.getTime());
        for (const f of folders) {
          const parentPath = folderMap.get(f.parentId ?? null) ?? "sql-snippets";
          folderMap.set(f.id, `${parentPath}/${f.name}`);
        }
        for (const s of snippets) {
          const dir = folderMap.get(s.folderId ?? null) ?? "sql-snippets";
          const safeName = s.name.replace(/[/\\:*?"<>|]/g, "_").slice(0, 100);
          zip.addBuffer(Buffer.from(s.sqlText, "utf8"), `${dir}/${safeName}.sql`);
        }
      }
      zip.end();
    } catch (e) {
      console.error(`[export] ${p.slug}:`, e instanceof Error ? e.message : e);
      output.destroy(e instanceof Error ? e : new Error(String(e)));
    }
  })();
  return output;
}
