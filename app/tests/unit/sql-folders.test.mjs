// SQL snippet folder rules (src/app/dashboard/[id]/sql/folders.ts): depth, cycles, export paths.
import { test } from "node:test";
import assert from "node:assert/strict";
import { loadTs } from "./load-ts.mjs";

const F = await loadTs("src/app/dashboard/[id]/sql/folders.ts");
const f = (id, parentId, name = id) => ({ id, parentId, name });
// a > b > c > d ; e (top) ; x (orphan: parent gone)
const tree = [f("a", null, "Reports"), f("b", "a", "Monthly"), f("c", "b"), f("d", "c"), f("e", null, "Other"), f("x", "gone")];

test("depth and subtree height", () => {
  assert.equal(F.depthOf(tree, null), 0);
  assert.equal(F.depthOf(tree, "a"), 1);
  assert.equal(F.depthOf(tree, "d"), 4);
  assert.equal(F.subtreeHeight(tree, "a"), 4);
  assert.equal(F.subtreeHeight(tree, "d"), 1);
  assert.deepEqual(F.descendants(tree, "a").sort(), ["b", "c", "d"]);
});

test("moves: no cycles, no deeper than 5 levels", () => {
  assert.match(F.moveProblem(tree, "a", "a"), /into itself/);
  assert.match(F.moveProblem(tree, "a", "d"), /its own subfolders/); // would make a cycle
  assert.match(F.moveProblem(tree, "b", "c"), /its own subfolders/);
  assert.equal(F.moveProblem(tree, "d", null), null);
  assert.equal(F.moveProblem(tree, "e", "d"), null); // e becomes level 5
  assert.match(F.moveProblem([...tree, f("e2", "e")], "e", "d"), /up to 5 levels/); // e2 would be level 6
  assert.equal(F.moveProblem(tree, "a", "e"), null); // a's 4 levels under top-level e: exactly 5
  assert.match(F.moveProblem([...tree, f("e2", "e")], "a", "e2"), /up to 5 levels/); // 2 + 4 = 6
});

test("a cycle already in the data can't hang anything", () => {
  const loop = [f("p", "q"), f("q", "p")];
  assert.equal(F.depthOf(loop, "p"), 2);
  assert.ok(F.subtreeHeight(loop, "p") <= 2);
  assert.deepEqual([...F.folderPaths(loop).keys()], []);
});

test("names", () => {
  assert.deepEqual(F.checkName("  Monthly   reports ", "Folder"), { name: "Monthly reports" });
  assert.match(F.checkName("", "Folder").error, /required/);
  assert.match(F.checkName("a/b", "Folder").error, /can't contain/);
  assert.match(F.checkName("..", "Snippet").error, /isn't allowed/);
  assert.match(F.checkName("x".repeat(81), "Folder").error, /at most 80/);
  assert.deepEqual(F.checkName("x".repeat(81), "Snippet"), { name: "x".repeat(81) });
});

test("export paths: nesting by parent (not creation order), safe and unique", () => {
  // "late" was created after "early" but is its parent: paths must still nest correctly.
  const folders = [f("early", "late", "Child"), f("late", null, "Parent"), f("dots", null, "..")];
  const out = F.snippetPaths(folders, [
    { id: "1", folderId: "early", name: "Q" },
    { id: "2", folderId: "early", name: "q" },
    { id: "3", folderId: null, name: "a/b:c" },
    { id: "4", folderId: "dots", name: ".." },
    { id: "5", folderId: "missing", name: "Lost" },
  ]);
  assert.deepEqual(out.map((o) => o.path), [
    "sql-snippets/Parent/Child/Q.sql",
    "sql-snippets/Parent/Child/q (2).sql",
    "sql-snippets/a_b_c.sql",
    "sql-snippets/_/_.sql",
    "sql-snippets/Lost.sql",
  ]);
  for (const { path } of out) assert.ok(!path.split("/").some((s) => s === ".." || s === "."), path);
});
