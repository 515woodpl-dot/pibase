// Load a TypeScript source file from src/ as an ES module (no build step needed for unit tests).
import { readFileSync, writeFileSync, mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, dirname } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import ts from "typescript";

const appDir = join(dirname(fileURLToPath(import.meta.url)), "..", "..");
export async function loadTs(rel) {
  const src = readFileSync(join(appDir, rel), "utf8");
  const js = ts.transpileModule(src, { compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 } }).outputText;
  const file = join(mkdtempSync(join(tmpdir(), "dbt-")), "m.mjs");
  writeFileSync(file, js);
  return import(pathToFileURL(file).href);
}

/** Like loadTs, for a file that imports sibling modules ("./shared"): all are transpiled side by side. */
export async function loadTsGroup(dirRel, main, siblings) {
  const dir = mkdtempSync(join(tmpdir(), "dbt-"));
  for (const name of [main, ...siblings]) {
    const src = readFileSync(join(appDir, dirRel, `${name}.ts`), "utf8");
    let js = ts.transpileModule(src, { compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 } }).outputText;
    for (const s of siblings) js = js.replaceAll(`from "./${s}"`, `from "./${s}.mjs"`);
    writeFileSync(join(dir, `${name}.mjs`), js);
  }
  return import(pathToFileURL(join(dir, `${main}.mjs`)).href);
}
