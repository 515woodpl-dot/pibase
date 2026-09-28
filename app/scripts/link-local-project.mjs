// Test helper: link a locally running PocketBase to a DATBase account WITHOUT Docker.
// Creates an "active" project row pointing at PB_INTERNAL_URL, as if provisioning had succeeded.
//
// Usage (from app/, with the same env vars the app uses):
//   node scripts/link-local-project.mjs <owner-email> [project-name] [slug] [pb-admin-email] [pb-admin-password]
//
// Tip: with PB_INTERNAL_URL="http://127.0.0.1:{slug}" you can run several PocketBase instances on
// different ports and use the port as the slug (e.g. slug 8090 and 8091) to test two isolated projects.
import pg from "pg";
import { createCipheriv, randomBytes } from "node:crypto";

const [, , email, name = "Test Project", slug = "8090", pbEmail = "admin@local.test", pbPass = "SuperSecret123"] =
  process.argv;
if (!email) {
  console.error("usage: node scripts/link-local-project.mjs <owner-email> [name] [slug] [pb-admin-email] [pb-admin-password]");
  process.exit(1);
}

const key = Buffer.from(process.env.ENCRYPTION_KEY ?? "", "base64");
if (key.length !== 32) throw new Error("ENCRYPTION_KEY must be set (same value the app uses)");
const iv = randomBytes(12);
const c = createCipheriv("aes-256-gcm", key, iv);
const data = Buffer.concat([c.update(pbPass, "utf8"), c.final()]);
const enc = Buffer.concat([iv, c.getAuthTag(), data]).toString("base64");

const db = new pg.Client({ connectionString: process.env.DATABASE_URL });
await db.connect();
const { rows } = await db.query(`select id from "user" where email = $1`, [email]);
if (!rows.length) throw new Error(`No DATBase account with email ${email}. Sign up in the app first.`);
const id = randomBytes(8).toString("hex");
await db.query(
  `insert into project (id, owner_id, name, slug, status, admin_email, admin_password_enc)
   values ($1, $2, $3, $4, 'active', $5, $6)`,
  [id, rows[0].id, name, slug, pbEmail, enc],
);
await db.end();
console.log(`Linked project "${name}" (slug ${slug}) → http://localhost:3000/dashboard/${id}`);
