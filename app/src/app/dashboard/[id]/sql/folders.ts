// Pure folder-tree helpers for SQL snippets (no database, no server imports): used by the actions,
// the sidebar and the data export, and unit-tested in tests/unit/sql-folders.test.mjs.

export const MAX_FOLDER_DEPTH = 5; // a top-level folder is depth 1
export const MAX_FOLDER_NAME = 80;
export const MAX_SNIPPET_NAME = 200;
export const MAX_SNIPPET_BYTES = 100 * 1024;

export type FolderLike = { id: string; parentId: string | null; name: string };

/** A folder name as the user typed it, or an error: no slashes (they are path separators in the export). */
export function checkName(raw: unknown, kind: "Folder" | "Snippet"): { name: string } | { error: string } {
  const name = String(raw ?? "").replace(/\s+/g, " ").trim();
  const max = kind === "Folder" ? MAX_FOLDER_NAME : MAX_SNIPPET_NAME;
  if (!name) return { error: `${kind} name is required.` };
  if (name.length > max) return { error: `${kind} names can be at most ${max} characters.` };
  if (/[/\\]/.test(name)) return { error: `${kind} names can't contain / or \\.` };
  if (name === "." || name === "..") return { error: `That ${kind.toLowerCase()} name isn't allowed.` };
  return { name };
}

/** Ancestors of a folder, nearest first. Stops (without looping) if the data ever contains a cycle. */
export function ancestors(folders: FolderLike[], id: string | null): FolderLike[] {
  const byId = new Map(folders.map((f) => [f.id, f]));
  const out: FolderLike[] = [];
  const seen = new Set<string>();
  let cur = id ? byId.get(id) : undefined;
  while (cur && !seen.has(cur.id)) {
    seen.add(cur.id);
    out.push(cur);
    cur = cur.parentId ? byId.get(cur.parentId) : undefined;
  }
  return out;
}

/** Depth of a folder (top level = 1; null = the root = 0). */
export function depthOf(folders: FolderLike[], id: string | null): number {
  return ancestors(folders, id).length;
}

/** Levels in a folder's subtree, counting itself (a folder without subfolders = 1). */
export function subtreeHeight(folders: FolderLike[], id: string): number {
  const children = new Map<string, string[]>();
  for (const f of folders) if (f.parentId) children.set(f.parentId, [...(children.get(f.parentId) ?? []), f.id]);
  const seen = new Set<string>();
  const height = (fid: string): number => {
    if (seen.has(fid)) return 0;
    seen.add(fid);
    return 1 + Math.max(0, ...(children.get(fid) ?? []).map(height));
  };
  return height(id);
}

/** Every folder id inside a folder (not including itself). */
export function descendants(folders: FolderLike[], id: string): string[] {
  const out: string[] = [];
  const queue = [id];
  const seen = new Set([id]);
  while (queue.length) {
    const cur = queue.shift()!;
    for (const f of folders)
      if (f.parentId === cur && !seen.has(f.id)) {
        seen.add(f.id);
        out.push(f.id);
        queue.push(f.id);
      }
  }
  return out;
}

/** Why a folder can't be moved under newParentId (null = top level), or null if it can. */
export function moveProblem(folders: FolderLike[], id: string, newParentId: string | null): string | null {
  if (newParentId === id) return "A folder can't be moved into itself.";
  if (newParentId && descendants(folders, id).includes(newParentId)) return "A folder can't be moved into one of its own subfolders.";
  if (depthOf(folders, newParentId) + subtreeHeight(folders, id) > MAX_FOLDER_DEPTH) {
    return `Folders can be nested up to ${MAX_FOLDER_DEPTH} levels.`;
  }
  return null;
}

/** Folder id → its names from the top ("Reports", "Monthly"). Folders in a broken cycle get no entry. */
export function folderPaths(folders: FolderLike[]): Map<string, string[]> {
  const byId = new Map(folders.map((f) => [f.id, f]));
  const out = new Map<string, string[]>();
  for (const f of folders) {
    const chain = ancestors(folders, f.id);
    const top = chain[chain.length - 1];
    if (top.parentId && byId.has(top.parentId)) continue; // cycle
    if (top.parentId && !byId.has(top.parentId)) continue; // orphan
    out.set(f.id, chain.map((x) => x.name).reverse());
  }
  return out;
}

/** One safe path segment for a zip entry: no separators, no control characters, no ".." tricks. */
export function safeSegment(name: string): string {
  const s = name
    .replace(/[\u0000-\u001f\u007f]/g, "")
    .replace(/[/\\:*?"<>|]/g, "_")
    .trim()
    .slice(0, 100);
  return !s || /^\.+$/.test(s) ? "_" : s;
}

/**
 * Zip paths for snippets: sql-snippets/<folder>/<sub>/<name>.sql, unique (a second snippet with the
 * same name in one folder becomes "<name> (2).sql"). Snippets in a missing folder go to the top.
 */
export function snippetPaths(
  folders: FolderLike[],
  snippets: { id: string; folderId: string | null; name: string }[],
): { id: string; path: string }[] {
  const paths = folderPaths(folders);
  const used = new Set<string>();
  const out: { id: string; path: string }[] = [];
  for (const s of snippets) {
    const dir = ["sql-snippets", ...(s.folderId ? (paths.get(s.folderId) ?? []) : []).map(safeSegment)].join("/");
    const base = safeSegment(s.name);
    let path = `${dir}/${base}.sql`;
    for (let n = 2; used.has(path.toLowerCase()); n++) path = `${dir}/${base} (${n}).sql`;
    used.add(path.toLowerCase());
    out.push({ id: s.id, path });
  }
  return out;
}
