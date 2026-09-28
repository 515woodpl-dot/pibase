import "server-only";
import * as nodeModule from "node:module";
import { findOwnerField, ownerRule, RULE_PRESETS, uniqueRules, type RuleOp } from "@/app/dashboard/[id]/tables/shared";
import type { Collection, Field } from "./pb";

/*
 * DATBASE.md: a per-project guide for AI coding assistants (and people).
 * Generated from the project's live schema, so it always matches reality.
 * It documents the API in DATBase terms only: plain HTTPS + JSON, no engine names.
 * Every behaviour described here is covered by an automated test (see TESTING-AI.md).
 */

const OPS: RuleOp[] = ["list", "view", "create", "update", "delete"];
const OP_WORDS: Record<RuleOp, string> = {
  list: "List / search",
  view: "View one",
  create: "Create",
  update: "Edit",
  delete: "Delete",
};

function describeRule(rule: string | null, op: RuleOp, ownerField: string | null, isAuth: boolean) {
  if (rule === RULE_PRESETS.admin) return "🔒 Server only (never from a browser or app)";
  if (rule === RULE_PRESETS.public) return isAuth && op === "create" ? "Anyone can sign up" : "Anyone, no sign-in";
  if (rule === RULE_PRESETS.signedIn) return "Any signed-in user (all records, including other users')";
  if (rule === RULE_PRESETS.verified) return "Signed-in users whose email is confirmed (`verified: true`)";
  if (ownerField && rule === ownerRule(op, ownerField))
    return op === "create"
      ? `Signed-in users, and \`${ownerField}\` must be their own user id`
      : `Only the record's owner (\`${ownerField}\` = signed-in user)`;
  if (isAuth && rule === "id = @request.auth.id") return "Only the signed-in user, for their own account";
  return `Custom rule: \`${rule}\``;
}

function tsType(f: Field, byId: Map<string, Collection>): string {
  const many = (f.maxSelect ?? 1) > 1;
  switch (f.type) {
    case "number":
      return "number";
    case "bool":
      return "boolean";
    case "json":
      return "unknown";
    case "select": {
      const u = ((f.values as string[]) ?? []).map((v) => JSON.stringify(v)).join(" | ") || "string";
      return many ? `(${u})[]` : `${u} | ""`;
    }
    case "file":
    case "relation":
      return many ? "string[]" : "string";
    default:
      return "string";
  }
}

/** "whole number from 1 to 5 (0 = empty)" etc. Plain words, used in the field table and type comments. */
function numberRange(f: Field) {
  const min = f.min == null ? null : Number(f.min);
  const max = f.max == null ? null : Number(f.max);
  const kind = f.onlyInt ? "whole number" : "number";
  const range =
    min != null && max != null ? ` from ${min} to ${max}` : min != null ? `, at least ${min}` : max != null ? `, at most ${max}` : "";
  // For an optional number, 0 means "empty" and skips the range check.
  const zero = !f.required && ((min != null && min > 0) || (max != null && max < 0)) ? " (0 = empty; send a value in range)" : "";
  return `${kind}${range}${zero}`;
}

function describeField(f: Field, byId: Map<string, Collection>): string {
  const many = (f.maxSelect ?? 1) > 1;
  switch (f.type) {
    case "text":
      return "text";
    case "editor":
      return "rich text (HTML string)";
    case "number":
      return numberRange(f);
    case "bool":
      return "true / false";
    case "email":
      return "email address";
    case "url":
      return "URL";
    case "date":
      return 'date-time (UTC), e.g. `"2026-09-27 14:30:00.000Z"`';
    case "autodate":
      return "date-time, set automatically (read-only)";
    case "json":
      return "any JSON value";
    case "select":
      return `${many ? "one or more of" : "one of"}: ${((f.values as string[]) ?? []).map((v) => `\`${v}\``).join(", ")}`;
    case "file": {
      const mb = f.maxSize ? `, max ${Math.round(Number(f.maxSize) / 1024 / 1024)} MB each` : "";
      return `${many ? `up to ${f.maxSelect} files` : "1 file"}${mb} (stored value = file name)`;
    }
    case "relation": {
      const t = byId.get(f.collectionId ?? "")?.name ?? "another table";
      return many ? `ids of records in \`${t}\` (array)` : `id of a record in \`${t}\``;
    }
    case "password":
      return "password (write-only)";
    default:
      return f.type;
  }
}

function exampleValue(f: Field, byId: Map<string, Collection>): unknown {
  const many = (f.maxSelect ?? 1) > 1;
  switch (f.type) {
    case "number": {
      // An in-range example (review AI-04): never an arbitrary value that breaks the field's limits.
      const min = f.min == null ? null : Number(f.min);
      const max = f.max == null ? null : Number(f.max);
      if (min != null) return min;
      if (max != null) return Math.min(max, 1);
      return 1;
    }
    case "bool":
      return true;
    case "email":
      return "someone@example.com";
    case "url":
      return "https://example.com";
    case "date":
      return "2026-09-27 14:30:00.000Z";
    case "json":
      return { any: "json" };
    case "select": {
      const v = (f.values as string[]) ?? [];
      return many ? v.slice(0, 2) : (v[0] ?? "");
    }
    case "file":
      return many ? ["photo_x7k2p.jpg"] : "photo_x7k2p.jpg";
    case "relation":
      return many ? ["RECORD_ID_1", "RECORD_ID_2"] : "RECORD_ID";
    default:
      return `example ${f.name}`;
  }
}

const pascal = (s: string) =>
  s
    .split(/[^a-zA-Z0-9]+/)
    .filter(Boolean)
    .map((w) => w[0].toUpperCase() + w.slice(1))
    .join("")
    .replace(/s$/, "") || "Row";

export function buildGuide(input: { projectName: string; url: string; collections: Collection[]; generatedAt: Date; email?: boolean }) {
  const { projectName, url, collections, generatedAt } = input;
  const byId = new Map(collections.map((c) => [c.id, c]));
  const authIds = collections.filter((c) => c.type === "auth").map((c) => c.id);
  const usersTable = collections.find((c) => c.type === "auth" && c.name === "users") ?? collections.find((c) => c.type === "auth");
  const dataTables = collections.filter((c) => c.type !== "auth");
  const date = generatedAt.toISOString().slice(0, 16).replace("T", " ") + " UTC";

  const typeNames = new Map(collections.map((c) => [c.id, pascal(c.name)]));
  const expandType = (r: Field) => {
    const t = typeNames.get(r.collectionId ?? "");
    return t ? ((r.maxSelect ?? 1) > 1 ? `${t}[]` : t) : "unknown";
  };

  const visible = (c: Collection) =>
    c.fields.filter((f) => !f.hidden && f.type !== "password" && f.name !== "tokenKey");

  /* ---------- per-table sections ---------- */
  const tableSection = (c: Collection) => {
    const isAuth = c.type === "auth";
    const owner = isAuth ? null : findOwnerField(c.fields, authIds);
    const rules: Record<RuleOp, string | null> = {
      list: c.listRule,
      view: c.viewRule,
      create: c.createRule,
      update: c.updateRule,
      delete: c.deleteRule,
    };
    const fields = visible(c);
    const writable = fields.filter((f) => !f.system && f.type !== "autodate");
    const typeName = pascal(c.name);
    const example: Record<string, unknown> = { id: "RECORD_ID", collectionId: "TABLE_ID", collectionName: c.name };
    for (const f of fields) if (f.name !== "id") example[f.name] = f.type === "autodate" ? "2026-09-27 14:30:00.000Z" : exampleValue(f, byId);

    const createBody: Record<string, unknown> = {};
    for (const f of writable.filter((x) => x.type !== "file")) {
      if (owner && f.name === owner) continue;
      createBody[f.name] = exampleValue(f, byId);
    }
    const hasFiles = writable.some((f) => f.type === "file");
    const relations = writable.filter((f) => f.type === "relation");

    const unique = uniqueRules(c.indexes);
    const uniqueSingle = new Set(unique.filter((u) => u.fields.length === 1).map((u) => u.fields[0]));
    const uniqueMulti = unique.filter((u) => u.fields.length > 1);

    const lines: string[] = [];
    lines.push(`### \`${c.name}\`${isAuth ? " (your app's users)" : ""}`, "");
    lines.push("| Action | Who is allowed |", "|---|---|");
    for (const op of OPS) lines.push(`| ${OP_WORDS[op]} | ${describeRule(rules[op], op, owner, isAuth)} |`);
    lines.push("");
    lines.push("| Field | Type | Required | Notes |", "|---|---|---|---|");
    lines.push("| `id` | text (15 chars) | auto | Set by DATBase. Never send it when creating. |");
    for (const f of fields) {
      if (f.name === "id") continue;
      const notes =
        f.type === "autodate"
          ? "Read-only"
          : isAuth && f.name === "email"
            ? "Unique. Hidden from other users unless `emailVisibility` is true"
            : isAuth && f.name === "verified"
              ? "Read-only for app users"
              : owner && f.name === owner
                ? "**Owner.** On create, set it to the signed-in user's id"
                : "";
      const allNotes = [notes, !isAuth && uniqueSingle.has(f.name) ? "**Unique**: no two records may have the same value" : ""]
        .filter(Boolean)
        .join(". ");
      lines.push(`| \`${f.name}\` | ${describeField(f, byId)} | ${f.required ? "yes" : "no"} | ${allNotes} |`);
    }
    if (isAuth) lines.push("| `password` | text, min 8 characters | on create | Write-only. Never returned |");
    lines.push("");
    for (const u of uniqueMulti)
      lines.push(
        `**Unique together:** ${u.fields.map((f) => `\`${f}\``).join(" + ")}. Only one record may have each combination; creating or editing into a duplicate fails with \`400\` and \`validation_not_unique\` on those fields. Enforced by the database, even for simultaneous requests.`,
        "",
      );

    lines.push("```ts");
    lines.push(`export interface ${typeName} {`);
    lines.push("  id: string;");
    lines.push("  collectionId: string;");
    lines.push("  collectionName: string;");
    for (const f of fields) {
      if (f.name === "id") continue;
      if (f.type === "number" && (f.min != null || f.max != null || f.onlyInt)) lines.push(`  /** ${numberRange(f)} */`);
      lines.push(`  ${f.name}: ${tsType(f, byId)};`);
    }
    if (relations.length)
      lines.push(`  /** Linked records, present when requested with \`expand\` */`, `  expand?: { ${relations.map((r) => `${r.name}?: ${expandType(r)}`).join("; ")} };`);
    lines.push("}");
    lines.push("```", "");

    if (!isAuth) {
      lines.push("Examples (with the `datbase.ts` client below):", "", "```ts");
      lines.push(`import { list, getOne, create, update, remove${owner ? ", currentUser" : ""} } from "./datbase";`);
      lines.push("");
      const sortField = fields.some((f) => f.name === "created") ? "-created" : "";
      lines.push(
        `const page = await list<${typeName}>("${c.name}", { perPage: 20${sortField ? `, sort: "${sortField}"` : ""}${relations[0] ? `, expand: "${relations[0].name}"` : ""} });`,
      );
      lines.push(`const one = await getOne<${typeName}>("${c.name}", page.items[0].id);`);
      const body = owner
        ? `{ ...${JSON.stringify(createBody)}, ${owner}: currentUser()!.id }`
        : JSON.stringify(createBody);
      lines.push(`const created = await create<${typeName}>("${c.name}", ${body});`);
      const firstText = writable.find((f) => f.type === "text" || f.type === "number");
      if (firstText)
        lines.push(
          `await update<${typeName}>("${c.name}", created.id, { ${firstText.name}: ${JSON.stringify(exampleValue(firstText, byId))} });`,
        );
      lines.push(`await remove("${c.name}", created.id);`);
      if (hasFiles) {
        const ff = writable.find((f) => f.type === "file")!;
        lines.push("");
        lines.push("// With a file: send FormData (don't set Content-Type yourself)");
        lines.push("const form = new FormData();");
        for (const [k, v] of Object.entries(createBody).slice(0, 2))
          lines.push(`form.append("${k}", ${JSON.stringify(typeof v === "string" ? v : JSON.stringify(v))});`);
        if (owner) lines.push(`form.append("${owner}", currentUser()!.id);`);
        lines.push(`form.append("${ff.name}", fileInput.files[0]);`);
        lines.push(`const withFile = await create<${typeName}>("${c.name}", form);`);
        lines.push(`const imageSrc = fileUrl(withFile, ${(ff.maxSelect ?? 1) > 1 ? `withFile.${ff.name}[0]` : `withFile.${ff.name}`});`);
      }
      lines.push("```", "");
    }

    lines.push("<details><summary>Example record as returned by the API</summary>", "", "```json");
    lines.push(JSON.stringify(example, null, 2));
    lines.push("```", "</details>", "");
    return lines.join("\n");
  };

  /* ---------- the document ---------- */
  const md: string[] = [];
  md.push(`# DATBase backend: ${projectName}`, "");
  md.push(
    `> Generated ${date} from the live project. **If tables, fields or access change in the DATBase dashboard, download this file again.**`,
    "",
  );
  md.push(
    "This file is written for AI coding assistants and for developers. It is the complete, authoritative description of this app's backend: database tables, user accounts, file storage and the HTTP API. If you are an AI assistant, **read the whole file before writing code** and follow the rules below.",
    "",
  );

  md.push("## Rules for AI assistants", "");
  md.push(
    [
      "1. **Use only what this file documents.** Table names, field names, types and access rules below are exact. Don't invent tables, fields or endpoints.",
      "2. **You cannot change the schema or access rules.** If the app needs a new table, field or different access, stop and tell the human exactly what to add in the DATBase dashboard (Tables → the table → Settings). Then continue once they confirm, using a freshly downloaded DATBASE.md.",
      "3. **Never put a server key in browser, mobile or public code.** Server keys ignore all access rules and are for the app owner's servers only (see *Server-side access*). App users sign in with their own email and password (see *Users & sign-in*).",
      "4. **Respect the access table of each table.** If an action is \"Server only\", the website can't do it directly. If it says \"Any signed-in user\", send the user's token.",
      "5. **Use the `datbase.ts` client at the end of this file.** Copy it into the project (for example `src/lib/datbase.ts`). It has no dependencies and works in browsers, Node 18+, Next.js, Vite, React Native and Deno.",
      "6. **Build filters with the `q()` helper** for any user-typed value, so it's quoted safely.",
      "7. **Show API error messages to the user.** They are human-readable (see *Errors*).",
    ].join("\n"),
    "",
  );

  md.push("## Connection", "");
  md.push(`- **API base URL:** \`${url}\``);
  md.push("- **Format:** JSON over HTTPS. File uploads use `multipart/form-data`.");
  md.push("- **CORS:** requests are allowed from any website origin.");
  md.push("- **Auth header (when signed in):** `Authorization: Bearer <token>`.");
  md.push("- **No API key is needed** for actions allowed to *Anyone*.", "");

  md.push("## Tables", "");
  md.push(
    dataTables.length
      ? `This project has ${dataTables.length} data table${dataTables.length > 1 ? "s" : ""}${usersTable ? " plus the users table" : ""}. Every record also has \`id\`, \`collectionId\` and \`collectionName\`.`
      : "This project has no data tables yet. Ask the human to create them in the DATBase dashboard (Tables → New table), then download DATBASE.md again.",
    "",
  );
  for (const c of dataTables) md.push(tableSection(c));
  if (usersTable) md.push(tableSection(usersTable));

  md.push("## What \"not allowed\" looks like", "");
  md.push(
    [
      "Access rules don't always produce an error. Know these cases, or you'll chase phantom bugs:",
      "",
      "| Situation | Response |",
      "|---|---|",
      "| List a table the user may not see (signed-out, or not the owner) | **`200` with only the allowed records, which may be an empty `items: []`**. No error. |",
      "| View / edit / delete a record the user may not access | `404` *\"The requested resource wasn't found.\"* (same as a missing record) |",
      "| Create where the rule isn't met (e.g. not signed in, or `owner` isn't yourself) | `400` *\"Failed to create record.\"* with empty `data` |",
      "| Any action marked *Server only* | `403` *\"Only superusers can perform this action.\"* |",
      "| No token sent | Treated as **signed out**: lists get emptier, protected creates fail. |",
      "| Expired, invalid or revoked token (user session or server key) | `401` *\"The token is invalid, expired or revoked…\"* on **every** call. Never an empty result. The client signs the user out automatically; show the sign-in screen. |",
    ].join("\n"),
    "",
  );
  md.push(
    "So: an empty list on a signed-in-only table usually means **the token wasn't sent**. A 404 right after creating usually means **the user isn't allowed to view it**.",
    "",
  );

  if (usersTable) {
    const u = usersTable.name;
    md.push("## Users & sign-in", "");
    md.push(
      [
        `App users live in the \`${u}\` table. They're separate from the DATBase dashboard account.`,
        "",
        "| Task | HTTP | Client |",
        "|---|---|---|",
        `| Sign up | \`POST /api/collections/${u}/records\` with \`{email, password, passwordConfirm, ...fields}\` | \`signUp({ email, password, name })\` (also signs in) |`,
        `| Sign in | \`POST /api/collections/${u}/auth-with-password\` with \`{identity: email, password}\` → \`{token, record}\` | \`signIn(email, password)\` |`,
        `| Check / renew session | \`POST /api/collections/${u}/auth-refresh\` with the token → \`{token, record}\`, or \`401\` if invalid | \`refreshSession()\` |`,
        "| Sign out | Forget the token (nothing to call) | `signOut()` |",
        `| Current user | The \`record\` from sign-in or refresh | \`currentUser()\` |`,
        `| Change own details | \`PATCH /api/collections/${u}/records/{id}\` | \`update("${u}", id, {...})\` |`,
        `| Change own password | \`PATCH …/records/{id}\` with \`{oldPassword, password, passwordConfirm}\` (then sign in again) | \`update("${u}", id, {...})\` |`,
        ...(input.email
          ? [
              `| Forgot password | \`POST /api/collections/${u}/request-password-reset\` with \`{email}\` → \`204\` (also for unknown emails) | \`requestPasswordReset(email)\` |`,
              `| Confirm email | \`POST /api/collections/${u}/request-verification\` with \`{email}\` → \`204\` | \`requestVerification(email)\` (\`signUp()\` already sends it) |`,
              `| Change email | Signed in: \`POST /api/collections/${u}/request-email-change\` with \`{newEmail}\` → \`204\`; a link goes to the new address | \`request("/api/collections/${u}/request-email-change", { method: "POST", body: JSON.stringify({ newEmail }) })\` |`,
            ]
          : []),
      ].join("\n"),
      "",
    );
    md.push(
      "- Tokens last about **7 days**. `refreshSession()` renews them, so call it on app start. Any call with an expired token returns `401`, and the client then signs out by itself.",
      "- Sign-in errors are deliberately vague (*\"Failed to authenticate.\"*). Show a generic \"wrong email or password\".",
      "- Sign-up errors are per field, e.g. `data.email.message = \"Value must be unique.\"`, `data.password.message = \"Must be at least 8 character(s).\"`.",
      ...(input.email
        ? [
            "- **Emails:** DATBase sends them (from its own address, with the app's name) and the links open a **page hosted by DATBase** where the user sets the new password or confirms. You don't build those pages. Afterwards the user signs in to your app normally. In your UI, a \"Forgot password?\" form that calls `requestPasswordReset(email)` and then says *\"If that email has an account, we sent a link\"* is all you need.",
            "- **Confirmed email:** the user record has `verified: true` once confirmed. To limit a table to confirmed users, ask the human to pick *Signed-in users with a confirmed email* for it. Your UI can show a \"Confirm your email\" hint with a resend button when `currentUser()?.verified` is false (call `refreshSession()` first to get the latest value).",
            "- Each project can send a limited number of emails per hour; extra requests still answer `204` but no email goes out.",
            "- **Not available:** social login (Google, GitHub) and one-time-password login. Don't build UI for them.",
          ]
        : ["- **Not available yet:** password-reset emails, email verification and social login (Google, GitHub). Don't build UI for them."]),
      "- Browsers: the client keeps the token in `localStorage`. For server-side rendering, pass `{ token }` explicitly (see the client).",
      "",
    );
  }

  md.push("## Reading data", "");
  md.push(
    [
      "`GET /api/collections/{table}/records` returns a page:",
      "",
      "```json",
      '{ "page": 1, "perPage": 30, "totalItems": 57, "totalPages": 2, "items": [ /* records */ ] }',
      "```",
      "",
      "| Query parameter | Meaning | Example |",
      "|---|---|---|",
      "| `page` | Page number, starts at 1 | `page=2` |",
      "| `perPage` | Items per page (default 30, max 1000) | `perPage=50` |",
      "| `sort` | Comma-separated fields; `-` for descending. Only existing fields (unknown → `400`). | `sort=-created,name` |",
      "| `filter` | Filter expression (below) | `filter=(price > 10 && name ~ \"mug\")` |",
      "| `expand` | Include linked records (link fields), comma-separated, nested with `.` | `expand=product` |",
      "| `fields` | Return only these fields | `fields=id,name,price` |",
      "| `skipTotal` | `1` = faster, but `totalItems`/`totalPages` become `-1` | `skipTotal=1` |",
      "",
      "One record: `GET /api/collections/{table}/records/{id}` (also accepts `expand` and `fields`).",
      "",
      "**Filter syntax.** Compare fields with `=` `!=` `>` `>=` `<` `<=`. `~` means *contains* (case-insensitive) and `!~` *doesn't contain*. Combine with `&&` `||` and parentheses. Text and dates go in double quotes. Examples:",
      "",
      "```",
      'status = "paid" && total >= 100',
      'name ~ "mug" || description ~ "mug"',
      'created >= "2026-09-01 00:00:00"',
      'owner = "USER_ID"',
      "product.name ~ \"mug\"          (filter through a link field)",
      "```",
      "",
      "Always build filters with user input through `q()`: `` `name ~ ${q(search)}` ``. URL-encode the whole filter (the client does it).",
      "",
      "**Linked records.** With `expand=product`, each item gets `item.expand.product` (the full linked record, if the user may view it).",
      "",
    ].join("\n"),
  );

  md.push("## Writing data", "");
  md.push(
    [
      "| Action | HTTP | Success |",
      "|---|---|---|",
      "| Create | `POST /api/collections/{table}/records` with JSON (or FormData for files) | `200` + the new record |",
      "| Edit | `PATCH /api/collections/{table}/records/{id}` with only the changed fields | `200` + the updated record |",
      "| Delete | `DELETE /api/collections/{table}/records/{id}` | `204`, no body |",
      "",
      "- **Dates:** send ISO strings (`\"2026-09-27T14:30:00Z\"` or `\"2026-09-27\"`). Offsets are converted to UTC. They come back as `\"2026-09-27 14:30:00.000Z\"` (space, not `T`); use `toDate()` from the client to parse them (Safari can't parse the space form directly).",
      "- **Numbers** may be sent as numbers or numeric strings. **Choices** must be one of the listed values.",
      "- **Links:** send the linked record's id (or an array of ids for multiple).",
      "- **Files:** use `FormData`. Append each non-file field as a string (JSON values as `JSON.stringify(...)`) and each file under its field name. Don't set `Content-Type`; the browser does. A stored file value is its **file name**. Build the URL with `fileUrl(record, name)` → `" +
        url +
        "/api/files/{collectionId}/{recordId}/{fileName}`. Images work directly in `<img src>`.",
      "- **Remove a file:** `update(table, id, { field: \"\" })` for single-file fields. For multi-file fields, `update(table, id, { \"field-\": [\"name.jpg\"] })` removes specific files, and appending files under `field+` in FormData adds files without replacing the existing ones (appending under `field` replaces them all).",
      "- **IDs** are generated for you (15 lowercase letters/digits).",
      "",
    ].join("\n"),
  );

  md.push("## Errors", "");
  md.push(
    [
      "Every error has this shape:",
      "",
      "```json",
      '{ "status": 400, "message": "Failed to create record.", "data": { "name": { "code": "validation_required", "message": "Cannot be blank." } } }',
      "```",
      "",
      "| Status | Meaning | What to do |",
      "|---|---|---|",
      "| `400` | Invalid input (see `data.<field>.message`), a create not allowed by the rules, a bad filter/sort, or a sign-in that failed | Show field messages next to inputs |",
      "| `403` | *Server only* action. Or **plan limit**: *\"…reached its storage limit…\"* / *\"…reached its user limit…\"* | Show the message. Storage full still allows reading and deleting |",
      "| `404` | Record missing **or not visible to this user**, or unknown table | Check the table's access rules |",
      "| `429` | **Monthly API request limit reached**: *\"…used all API requests included in its plan this month.\"* | Show a friendly \"service temporarily unavailable\"; it resets next month |",
      "| `5xx` / network error | Temporary problem | Retry once after a short delay |",
      "",
      "Common `data.<field>.code` values: `validation_required`, `validation_not_unique` (duplicate of a unique field or combination), `validation_min_number_constraint` / `validation_max_number_constraint` (number out of range), `validation_only_int_constraint` (decimal in a whole-number field), `validation_invalid_value` (not one of the choices).",
      "",
      "The client throws `DatbaseError` with `.status`, `.message` and `.fieldErrors` (`{ field: message }`).",
      "",
    ].join("\n"),
  );

  md.push("## Server-side access (server keys)", "");
  md.push(
    [
      "For code that runs **only on the app owner's servers** (a backend, an admin script, a cron job, a Next.js server route), use a **server key**. It can read and change every table, **ignoring the access rules above**, so it must never reach a browser, mobile app or public repository.",
      "",
      "- **Getting one:** the human creates it in the DATBase dashboard (project → Overview → *Server keys* → Create key) and gives it to the server as the environment variable `DATBASE_SERVER_KEY`. If it isn't set, **stop and ask the human** to create one. Never try other ways to obtain admin access.",
      "- **Using it:** send it like a user token: `Authorization: Bearer <key>`. With the client, pass it per call:",
      "",
      "```ts",
      "// server-only file (never imported by browser code)",
      'import { checkServerKey, listAll } from "./datbase";',
      "await checkServerKey(process.env.DATBASE_SERVER_KEY); // throws (401/403) if missing, expired or revoked",
      "const asServer = { token: process.env.DATBASE_SERVER_KEY! };",
      `const everything = await listAll("${dataTables[0]?.name ?? "your_table"}", { sort: "-created" }, 5000, asServer);`,
      "```",
      "",
      "- **Check the key first and fail loudly.** Start every server job with `await checkServerKey(process.env.DATBASE_SERVER_KEY)`. It throws unless the key is a live server key. Don't catch errors and fall back to zeros or empty lists: let the job exit non-zero.",
      "- Without the client: `GET /api/session` with `Authorization: Bearer <key>` returns `200 {\"type\":\"server\"}` for a live key and `401` otherwise.",
      "- Keys **expire** (30 days to 1 year, shown in the dashboard) and can be **revoked** at any time. After that, **every** call with the key returns `401`. You never get a silent empty result.",
      "- Requests made with a key **count toward the project's plan** like any other.",
      "- Don't call `signIn()` with a key. Keys aren't user accounts and don't need refreshing.",
      "",
    ].join("\n"),
  );

  md.push("## Not available (don't build against these)", "");
  md.push(
    `- Creating or changing tables, fields or access rules through the API (use the dashboard).\n- Realtime subscriptions, server functions or webhooks.\n${input.email ? "- Social login and one-time-password login.\n- Sending your own emails through DATBase (only sign-in related emails exist).\n" : "- Password reset, email verification and social login (no email provider yet).\n"}- Server keys in client code. There is no \"public API key\" to add either: public access is controlled by each table's access rules.\n`,
  );

  md.push("## `datbase.ts`: the client", "");
  md.push("Copy this file into the project unchanged, for example as `src/lib/datbase.ts`. For plain JavaScript, remove the type annotations.", "");
  md.push("```ts", clientSource(url, usersTable?.name ?? null, !!input.email), "```", "");
  md.push(
    "<details><summary>Plain JavaScript version (for pages without a build step: <code>&lt;script type=\"module\"&gt;</code>)</summary>",
    "",
    "Same client with the types removed. Save it as `datbase.js` and `import { list } from \"./datbase.js\"`.",
    "",
    "```js",
    plainJs(clientSource(url, usersTable?.name ?? null, !!input.email)),
    "```",
    "</details>",
    "",
  );

  md.push("## Troubleshooting checklist", "");
  md.push(
    [
      "- Empty list but data exists? → The table needs sign-in or ownership, and no token was sent (check `getToken()`).",
      "- `401` → the session or server key expired or was revoked. Users: sign in again. Servers: ask the human for a new key.",
      "- `400 Failed to create record.` with empty `data` → the create rule isn't met (not signed in, or the owner field isn't the current user's id).",
      "- `404` on a record you just saw → the view rule differs from the list rule, or the user isn't the owner.",
      "- `400` on list → a typo in `filter` or `sort` (field names are case-sensitive).",
      "- Images not showing → use `fileUrl(record, record.field)`, not the bare file name.",
      "- Field or table \"doesn't exist\" → this file is out of date. Download it again from the dashboard.",
    ].join("\n"),
    "",
  );

  return md.join("\n");
}

/** The copy-paste prompt shown in the dashboard next to the download button. */
export function aiPrompt() {
  return [
    "Use DATBase as the backend for this app (database, user sign-in and file storage).",
    "Before writing any code, read DATBASE.md in the project root completely. It describes the API, every table and field, who may do what, and a ready-made client (datbase.ts) to copy into the project.",
    "Use only what DATBASE.md documents. If the app needs a table, field or access change that isn't in it, stop and tell me exactly what to add in the DATBase dashboard.",
    "Never put admin or server credentials in browser code.",
  ].join("\n");
}

/* ------------------------------------------------------------------ */
/* The generated client. Plain fetch, no dependencies. Tested verbatim.  */
/* ------------------------------------------------------------------ */

export function clientSource(url: string, usersTable: string | null, email = false) {
  const users = usersTable ?? "users";
  return `// datbase.ts: DATBase client. No dependencies. Works in browsers, Node 18+, Next.js, Vite, React Native, Deno.
export const DATBASE_URL = ${JSON.stringify(url)};
const USERS_TABLE = ${JSON.stringify(users)};
const EMAIL_ENABLED = ${email}; // this project can send sign-in emails (reset password, confirm email)

export interface BaseRecord {
  id: string;
  collectionId: string;
  collectionName: string;
  [field: string]: unknown;
}
export interface ListResult<T> {
  page: number;
  perPage: number;
  totalItems: number;
  totalPages: number;
  items: T[];
}
export interface ListOptions {
  page?: number;
  perPage?: number;
  sort?: string;
  filter?: string;
  expand?: string;
  fields?: string;
  skipTotal?: boolean;
}
/** Pass { token } to act as a specific user (e.g. server-side rendering). Defaults to the signed-in user. */
export interface RequestOptions {
  token?: string | null;
  signal?: AbortSignal;
}

export class DatbaseError extends Error {
  status: number;
  fieldErrors: Record<string, string>;
  constructor(status: number, message: string, fieldErrors: Record<string, string> = {}) {
    super(message);
    this.name = "DatbaseError";
    this.status = status;
    this.fieldErrors = fieldErrors;
  }
}

/* ---------- session storage (localStorage in browsers, memory elsewhere) ---------- */
const memory = new Map<string, string>();
const storage = {
  get(k: string): string | null {
    try {
      return globalThis.localStorage?.getItem(k) ?? memory.get(k) ?? null;
    } catch {
      return memory.get(k) ?? null;
    }
  },
  set(k: string, v: string) {
    memory.set(k, v);
    try {
      globalThis.localStorage?.setItem(k, v);
    } catch {}
  },
  remove(k: string) {
    memory.delete(k);
    try {
      globalThis.localStorage?.removeItem(k);
    } catch {}
  },
};
const TOKEN_KEY = "datbase_token";
const USER_KEY = "datbase_user";

export function getToken(): string | null {
  return storage.get(TOKEN_KEY);
}
export function currentUser<T extends BaseRecord = BaseRecord>(): T | null {
  const raw = storage.get(USER_KEY);
  return raw ? (JSON.parse(raw) as T) : null;
}
function saveSession(token: string, user: BaseRecord) {
  storage.set(TOKEN_KEY, token);
  storage.set(USER_KEY, JSON.stringify(user));
}
export function signOut() {
  storage.remove(TOKEN_KEY);
  storage.remove(USER_KEY);
}

/* ---------- low-level request ---------- */
export async function request<T>(path: string, init: RequestInit = {}, opts: RequestOptions = {}): Promise<T> {
  const token = opts.token === undefined ? getToken() : opts.token;
  const headers = new Headers(init.headers);
  if (token) headers.set("Authorization", "Bearer " + token);
  if (init.body && !(init.body instanceof FormData) && !headers.has("Content-Type")) {
    headers.set("Content-Type", "application/json");
  }
  const res = await fetch(DATBASE_URL + path, { ...init, headers, signal: opts.signal });
  if (res.status === 401 && opts.token === undefined) signOut(); // stored session expired or revoked
  if (res.status === 204) return undefined as T;
  const body = await res.json().catch(() => null);
  if (!res.ok) {
    const fieldErrors: Record<string, string> = {};
    for (const [k, v] of Object.entries((body?.data ?? {}) as Record<string, { message?: string }>)) {
      if (v?.message) fieldErrors[k] = v.message;
    }
    throw new DatbaseError(res.status, body?.message ?? "Request failed (" + res.status + ")", fieldErrors);
  }
  return body as T;
}

const tablePath = (table: string) => "/api/collections/" + encodeURIComponent(table) + "/records";
const bodyOf = (data: Record<string, unknown> | FormData) => (data instanceof FormData ? data : JSON.stringify(data));

/* ---------- records ---------- */
export function list<T = BaseRecord>(table: string, o: ListOptions = {}, opts?: RequestOptions) {
  const q = new URLSearchParams();
  if (o.page) q.set("page", String(o.page));
  if (o.perPage) q.set("perPage", String(o.perPage));
  if (o.sort) q.set("sort", o.sort);
  if (o.filter) q.set("filter", o.filter);
  if (o.expand) q.set("expand", o.expand);
  if (o.fields) q.set("fields", o.fields);
  if (o.skipTotal) q.set("skipTotal", "1");
  const qs = q.toString();
  return request<ListResult<T>>(tablePath(table) + (qs ? "?" + qs : ""), {}, opts);
}

/** Every matching record (fetches page by page, up to maxItems). */
export async function listAll<T = BaseRecord>(table: string, o: Omit<ListOptions, "page" | "perPage"> = {}, maxItems = 5000, opts?: RequestOptions) {
  const out: T[] = [];
  for (let page = 1; out.length < maxItems; page++) {
    const r = await list<T>(table, { ...o, page, perPage: 200, skipTotal: true }, opts);
    out.push(...r.items);
    if (r.items.length < 200) break;
  }
  return out.slice(0, maxItems);
}

export function getOne<T = BaseRecord>(table: string, id: string, o: { expand?: string; fields?: string } = {}, opts?: RequestOptions) {
  const q = new URLSearchParams();
  if (o.expand) q.set("expand", o.expand);
  if (o.fields) q.set("fields", o.fields);
  const qs = q.toString();
  return request<T>(tablePath(table) + "/" + encodeURIComponent(id) + (qs ? "?" + qs : ""), {}, opts);
}

export function create<T = BaseRecord>(table: string, data: Record<string, unknown> | FormData, opts?: RequestOptions) {
  return request<T>(tablePath(table), { method: "POST", body: bodyOf(data) }, opts);
}

export function update<T = BaseRecord>(table: string, id: string, data: Record<string, unknown> | FormData, opts?: RequestOptions) {
  return request<T>(tablePath(table) + "/" + encodeURIComponent(id), { method: "PATCH", body: bodyOf(data) }, opts);
}

export function remove(table: string, id: string, opts?: RequestOptions) {
  return request<void>(tablePath(table) + "/" + encodeURIComponent(id), { method: "DELETE" }, opts);
}

/* ---------- users & sessions ---------- */
type AuthResponse = { token: string; record: BaseRecord };

export async function signIn<T extends BaseRecord = BaseRecord>(email: string, password: string): Promise<T> {
  const r = await request<AuthResponse>(
    "/api/collections/" + USERS_TABLE + "/auth-with-password",
    { method: "POST", body: JSON.stringify({ identity: email, password }) },
    { token: null },
  );
  saveSession(r.token, r.record);
  return r.record as T;
}

/** Create an account, then sign in. Extra fields (e.g. name) are saved on the user record. */
export async function signUp<T extends BaseRecord = BaseRecord>(
  fields: { email: string; password: string } & Record<string, unknown>,
): Promise<T> {
  await request(
    tablePath(USERS_TABLE),
    { method: "POST", body: JSON.stringify({ ...fields, passwordConfirm: fields.password }) },
    { token: null },
  );
  if (EMAIL_ENABLED) await requestVerification(fields.email).catch(() => {}); // "confirm your email" link
  return signIn<T>(fields.email, fields.password);
}

/** Email a "choose a new password" link. Resolves the same way whether or not the account exists. */
export async function requestPasswordReset(email: string): Promise<void> {
  if (!EMAIL_ENABLED) throw new DatbaseError(400, "Password reset emails are not available for this project.");
  await request<void>("/api/collections/" + USERS_TABLE + "/request-password-reset", { method: "POST", body: JSON.stringify({ email }) }, { token: null });
}

/** Email a "confirm your email" link (again). */
export async function requestVerification(email: string): Promise<void> {
  if (!EMAIL_ENABLED) throw new DatbaseError(400, "Email confirmation is not available for this project.");
  await request<void>("/api/collections/" + USERS_TABLE + "/request-verification", { method: "POST", body: JSON.stringify({ email }) }, { token: null });
}

/** Validate and renew the stored session. Returns the user, or null (and signs out) if it's no longer valid. */
export async function refreshSession<T extends BaseRecord = BaseRecord>(): Promise<T | null> {
  if (!getToken()) return null;
  try {
    const r = await request<AuthResponse>("/api/collections/" + USERS_TABLE + "/auth-refresh", { method: "POST" });
    saveSession(r.token, r.record);
    return r.record as T;
  } catch (e) {
    if (e instanceof DatbaseError && (e.status === 401 || e.status === 403 || e.status === 404)) signOut();
    return null;
  }
}

/** Server code only: throws unless \`key\` is a live server key (401 if expired/revoked, 403 if it's a user token). */
export async function checkServerKey(key: string | undefined): Promise<void> {
  if (!key) throw new DatbaseError(401, "DATBASE_SERVER_KEY is not set.");
  const me = await request<{ type: string }>("/api/session", {}, { token: key });
  if (me.type !== "server") throw new DatbaseError(403, "This is a user token, not a server key.");
}

/* ---------- helpers ---------- */
/** URL of a stored file, usable directly in <img src>. */
export function fileUrl(record: Pick<BaseRecord, "id" | "collectionId">, fileName: string): string {
  if (!fileName) return "";
  return DATBASE_URL + "/api/files/" + record.collectionId + "/" + record.id + "/" + encodeURIComponent(fileName);
}

/** Safely quote a value for use inside a filter: \`name ~ \${q(search)}\`. */
export function q(value: string | number | boolean | null): string {
  if (value === null) return "null";
  if (typeof value !== "string") return String(value);
  return '"' + value.replace(/\\\\/g, "\\\\\\\\").replace(/"/g, '\\\\"') + '"';
}

/** Parse a DATBase date string ("2026-09-27 14:30:00.000Z") into a Date. Works in all browsers. */
export function toDate(value: string): Date | null {
  return value ? new Date(value.replace(" ", "T")) : null;
}
`;
}

/** The client without TypeScript types, for plain `<script type="module">` pages (gap 7). */
function plainJs(ts: string) {
  // Node 22+ built-in (the runtime image is node:22); declared locally because @types/node lags behind.
  const strip = (nodeModule as unknown as { stripTypeScriptTypes: (code: string) => string }).stripTypeScriptTypes;
  return strip(ts)
    .replace("// datbase.ts:", "// datbase.js:")
    .split("\n")
    .map((l: string) => {
      // Stripped types leave runs of spaces: "list                (table        , o" → "list(table, o"
      l = l.replace(/\s+$/, "");
      if (/^\s*(\/\/|\/\*|\*)/.test(l)) return l; // comments stay as written
      const kw = "(?!if\\b|for\\b|while\\b|switch\\b|catch\\b|return\\b|await\\b|typeof\\b)";
      return l
        .replace(/(\S) {2,}/g, "$1 ")
        .replace(/ ([,);])/g, "$1")
        .replace(new RegExp("\\b" + kw + "(\\w+) \\((?=[^\\s)])", "g"), "$1(")
        .replace(new RegExp("\\b" + kw + "(\\w+) \\(\\)", "g"), "$1()");
    })
    .join("\n")
    .replace(/\n{3,}/g, "\n\n")
    .trim();
}
