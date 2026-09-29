import { sql } from "drizzle-orm";
import { type AnyPgColumn, bigint, boolean, index, integer, pgTable, primaryKey, text, timestamp, uniqueIndex, varchar } from "drizzle-orm/pg-core";
export * from "./auth-schema";
import { user } from "./auth-schema";

// creating → active ⇄ paused ; active → restoring → active ; any → error ; deleting → (row removed)
// suspended: stopped by the operator (abuse); only the operator can lift it.
export const projectStatus = ["creating", "active", "paused", "error", "deleting", "restoring", "suspended"] as const;
export type ProjectStatus = (typeof projectStatus)[number];

export const project = pgTable(
  "project",
  {
    id: text("id").primaryKey(),
    ownerId: text("owner_id")
      .notNull()
      .references(() => user.id, { onDelete: "cascade" }),
    name: text("name").notNull(),
    slug: text("slug").notNull().unique(), // subdomain + container name; server-generated only
    status: text("status", { enum: projectStatus }).notNull().default("creating"),
    statusMessage: text("status_message"),
    adminEmail: text("admin_email"),
    adminPasswordEnc: text("admin_password_enc"), // AES-256-GCM, see lib/crypto.ts
    createdAt: timestamp("created_at").notNull().defaultNow(),
    updatedAt: timestamp("updated_at").notNull().defaultNow(),
  },
  (t) => [index("project_owner_idx").on(t.ownerId)],
);

export type Project = typeof project.$inferSelect;

/** Latest usage snapshot per project + which limits are currently enforced. */
export const projectUsage = pgTable("project_usage", {
  projectId: text("project_id")
    .primaryKey()
    .references(() => project.id, { onDelete: "cascade" }),
  dbBytes: bigint("db_bytes", { mode: "number" }).notNull().default(0),
  storageBytes: bigint("storage_bytes", { mode: "number" }).notNull().default(0),
  users: integer("users").notNull().default(0),
  logsUntil: timestamp("logs_until"), // request logs counted up to here
  blockAll: boolean("block_all").notNull().default(false),
  blockWrites: boolean("block_writes").notNull().default(false),
  blockSignups: boolean("block_signups").notNull().default(false),
  checkedAt: timestamp("checked_at").notNull().defaultNow(),
});

/** API requests per project per calendar month (UTC), kept for history and billing. */
export const usageMonth = pgTable(
  "usage_month",
  {
    projectId: text("project_id")
      .notNull()
      .references(() => project.id, { onDelete: "cascade" }),
    month: text("month").notNull(), // "2026-09"
    requests: integer("requests").notNull().default(0),
  },
  (t) => [primaryKey({ columns: [t.projectId, t.month] })],
);

export type ProjectUsage = typeof projectUsage.$inferSelect;

/** Off-server project backups (encrypted objects in the backup bucket). */
export const backupKinds = ["auto", "manual", "pre-restore"] as const;
export const backup = pgTable(
  "backup",
  {
    id: text("id").primaryKey(),
    projectId: text("project_id")
      .notNull()
      .references(() => project.id, { onDelete: "cascade" }),
    kind: text("kind", { enum: backupKinds }).notNull(),
    status: text("status", { enum: ["running", "done", "failed"] }).notNull().default("running"),
    storageKey: text("storage_key"),
    sizeBytes: bigint("size_bytes", { mode: "number" }),
    error: text("error"),
    createdAt: timestamp("created_at").notNull().defaultNow(),
    finishedAt: timestamp("finished_at"),
  },
  (t) => [
    index("backup_project_idx").on(t.projectId, t.createdAt),
    // At most one running backup per project, enforced by the database (no double-starts).
    uniqueIndex("backup_one_running_idx").on(t.projectId).where(sql`status = 'running'`),
  ],
);
export type Backup = typeof backup.$inferSelect;

/**
 * Server keys: long-lived tokens for the customer's own servers (bypass access rules, count toward the plan).
 * Each key is a dedicated internal admin account inside the project; revoking deletes that account,
 * which invalidates the token immediately. The token itself is never stored.
 */
export const serverKey = pgTable(
  "server_key",
  {
    id: text("id").primaryKey(),
    projectId: text("project_id")
      .notNull()
      .references(() => project.id, { onDelete: "cascade" }),
    name: text("name").notNull(),
    pbUserId: text("pb_user_id").notNull(),
    hint: text("hint").notNull(), // last characters, to recognise a key
    createdAt: timestamp("created_at").notNull().defaultNow(),
    expiresAt: timestamp("expires_at").notNull(),
  },
  (t) => [index("server_key_project_idx").on(t.projectId)],
);
export type ServerKey = typeof serverKey.$inferSelect;

/** Monitoring: one row per problem the monitor has seen (open until resolved), for alert de-duplication. */
export const alertState = pgTable("alert_state", {
  key: text("key").primaryKey(), // e.g. "project-down:<id>", "disk"
  message: text("message").notNull(),
  since: timestamp("since").notNull().defaultNow(),
  resolvedAt: timestamp("resolved_at"), // null = still open
});

/** Plan-limit warning emails already sent (once per project, month, metric and level). */
export const usageNotice = pgTable(
  "usage_notice",
  {
    projectId: text("project_id")
      .notNull()
      .references(() => project.id, { onDelete: "cascade" }),
    month: text("month").notNull(), // "2026-09"
    metric: text("metric").notNull(), // requests | database | storage | users
    level: integer("level").notNull(), // 80 or 100
    sentAt: timestamp("sent_at").notNull().defaultNow(),
  },
  (t) => [primaryKey({ columns: [t.projectId, t.month, t.metric, t.level] })],
);

/* ---------- SQL editor: snippets and folders ---------- */

/**
 * Folders for organising SQL snippets. Nested up to 5 levels (checked in the actions).
 * Cascade: deleting a project removes its folders; deleting a folder removes its subfolders
 * (self-reference) and, through sql_snippet.folder_id, every snippet inside them.
 */
export const sqlFolder = pgTable(
  "sql_folder",
  {
    id: text("id").primaryKey(),
    projectId: text("project_id")
      .notNull()
      .references(() => project.id, { onDelete: "cascade" }),
    parentId: text("parent_id").references((): AnyPgColumn => sqlFolder.id, { onDelete: "cascade" }), // null = top level
    name: varchar("name", { length: 80 }).notNull(),
    createdAt: timestamp("created_at").notNull().defaultNow(),
  },
  (t) => [
    index("sql_folder_project_idx").on(t.projectId),
    index("sql_folder_parent_idx").on(t.parentId),
    // Folder names are unique (ignoring case) within the same parent, top level included
    // (a plain index would let two top-level folders share a name, because NULLs differ).
    uniqueIndex("sql_folder_unique_name_idx").on(t.projectId, sql`coalesce(${t.parentId}, '')`, sql`lower(${t.name})`),
  ],
);
export type SqlFolder = typeof sqlFolder.$inferSelect;

/**
 * SQL snippets: saved queries, per project, optionally in a folder.
 * sql_text is the raw SQL (max 100 KB enforced in application code).
 */
export const sqlSnippet = pgTable(
  "sql_snippet",
  {
    id: text("id").primaryKey(),
    projectId: text("project_id")
      .notNull()
      .references(() => project.id, { onDelete: "cascade" }),
    folderId: text("folder_id")
      .references(() => sqlFolder.id, { onDelete: "cascade" }),
    name: varchar("name", { length: 200 }).notNull(),
    sqlText: text("sql_text").notNull(),
    favorite: boolean("favorite").notNull().default(false),
    createdAt: timestamp("created_at").notNull().defaultNow(),
    updatedAt: timestamp("updated_at").notNull().defaultNow(),
  },
  (t) => [
    index("sql_snippet_project_idx").on(t.projectId),
    index("sql_snippet_folder_idx").on(t.folderId),
    index("sql_snippet_fav_idx").on(t.projectId, t.favorite),
  ],
);
export type SqlSnippet = typeof sqlSnippet.$inferSelect;

/**
 * Run history: last 20 runs per project (rolling; we delete old ones in the action).
 * No result rows are stored — just metadata.
 */
export const sqlRun = pgTable(
  "sql_run",
  {
    id: text("id").primaryKey(),
    projectId: text("project_id")
      .notNull()
      .references(() => project.id, { onDelete: "cascade" }),
    sql: text("sql").notNull(),
    elapsedMs: integer("elapsed_ms"),
    rowCount: integer("row_count"),
    error: text("error"),
    createdAt: timestamp("created_at").notNull().defaultNow(),
  },
  (t) => [index("sql_run_project_idx").on(t.projectId, t.createdAt)],
);
export type SqlRun = typeof sqlRun.$inferSelect;

/**
 * One row per project once the SQL editor has been opened: the starter snippets are created in the
 * same transaction as this row (insert … on conflict do nothing), so they appear exactly once even
 * when two tabs open the editor at the same moment, and deleting them all doesn't bring them back.
 */
export const sqlEditorState = pgTable("sql_editor_state", {
  projectId: text("project_id")
    .primaryKey()
    .references(() => project.id, { onDelete: "cascade" }),
  startersCreatedAt: timestamp("starters_created_at").notNull().defaultNow(),
});
