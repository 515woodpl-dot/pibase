import { sql } from "drizzle-orm";
import { bigint, boolean, index, integer, pgTable, primaryKey, text, timestamp, uniqueIndex } from "drizzle-orm/pg-core";
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
