/// <reference path="../pb_data/types.d.ts" />
// DATBase hook: runs inside every project's PocketBase.
//
// Control endpoints (only DATBase can call them): they require the header
// X-Datbase-Control = $DATBASE_CONTROL_SECRET, a per-project secret passed in as a container
// environment variable. Admin credentials or server keys are NOT enough, so a customer can't
// read or change their own plan enforcement.
//   GET  /api/datbase/usage          database + file storage size (bytes)
//   POST /api/datbase/requests/take  API requests counted since the last call (then resets to 0)
//   GET  /api/datbase/boot           id of the current process start (to detect restarts)
//   POST /api/datbase/limits         which limits are currently exceeded
//   POST /api/datbase/mail           email sender, link base and hourly cap {from, publicUrl, perHour}
//
// Enforcement:
//   blockAll     → every request except DATBase's own returns 429 (monthly requests used up)
//   blockWrites  → creating/editing records returns 403; reading and deleting still work (storage full)
//   blockSignups → creating app users returns 403 (user limit reached)
// Schema is dashboard-only: POST/PATCH/PUT/DELETE on /api/collections (create, edit, delete, import, truncate)
// and impersonation return 403 unless the request carries the control header, so a leaked server key can
// read and write data but never open up a table or change access.
// Fail closed: a request that sends a token which is invalid, expired or revoked gets 401 instead of
// silently running as "signed out" (which would turn a revoked server key into empty results).
// GET /api/session reports who a token belongs to ({type:"user"|"server"}), or 401.
//
// Email (app users): DATBase writes every email itself. Whatever a project admin changes in the
// settings or templates, emails come from DATBase's sender ($DATBASE_MAIL_FROM), use fixed wording,
// and link only to DATBase's hosted pages (<publicUrl>/<table>/<action>?token=…). The sender, link
// base and hourly cap are set by DATBase through POST /api/datbase/mail (stored in datbase_mail.json).
// Login alerts and one-time-password emails are off; the settings "send test email" endpoint is blocked.
// Counting: every request except DATBase's own and /api/health is counted here, independent of
// PocketBase's request logs (which a project admin could turn off).
// Note: PocketBase runs each handler in isolation, so helpers are defined inside handlers.

onBootstrap((e) => {
  e.next();
  e.app.store().set("datbase_boot", $security.randomString(16));
  try {
    const raw = toString($os.readFile(e.app.dataDir() + "/datbase_limits.json"));
    e.app.store().set("datbase_limits", JSON.parse(raw));
  } catch (_) {
    e.app.store().set("datbase_limits", {});
  }
  try {
    e.app.store().set("datbase_mail_cfg", JSON.parse(toString($os.readFile(e.app.dataDir() + "/datbase_mail.json"))));
  } catch (_) {
    e.app.store().set("datbase_mail_cfg", {});
  }
});

routerAdd("POST", "/api/datbase/mail", (e) => {
  const b = e.requestInfo().body || {};
  const cfg = {
    from: String(b.from || ""),
    publicUrl: String(b.publicUrl || "").replace(/\/+$/, ""),
    perHour: Math.max(0, parseInt(b.perHour || 50, 10) || 0),
  };
  $os.writeFile(e.app.dataDir() + "/datbase_mail.json", JSON.stringify(cfg), 0o644);
  e.app.store().set("datbase_mail_cfg", cfg);
  return e.json(200, cfg);
});

// Gatekeeper for every request: DATBase's own calls pass; everything else is limited and counted.
routerUse((e) => {
  const secret = $os.getenv("DATBASE_CONTROL_SECRET");
  const isControl = secret !== "" && e.request.header.get("X-Datbase-Control") === secret;
  const path = e.request.url.path;

  if (path.startsWith("/api/datbase/") || path.startsWith("/api/settings/test/")) {
    if (!isControl) throw new NotFoundError();
    return e.next();
  }
  if (isControl || path === "/api/health") return e.next();

  const state = e.app.store().get("datbase_limits") || {};
  if (state.blockAll) {
    throw new TooManyRequestsError("This project has used all API requests included in its plan this month.");
  }
  e.app.store().setFunc("datbase_requests", (n) => (n || 0) + 1);

  // Sign-in style endpoints must work even if a stale token is still attached.
  const signInPath = /^\/api\/collections\/[^/]+\/(auth-with-|auth-methods|request-|confirm-)/.test(path);
  const sent = (e.request.header.get("Authorization") || "").replace(/^Bearer\s+/i, "").trim();
  if (sent !== "" && !e.auth && !signInPath) {
    throw new UnauthorizedError("The token is invalid, expired or revoked. Sign in again (or use a valid server key).");
  }

  // Tables, access rules and schema imports are changed only from the DATBase dashboard (which sends the
  // control header). A server key or any other token can't create, alter or delete tables, import a
  // schema, or mint new tokens by impersonation, even though the engine would allow a superuser to.
  //
  // The same gate covers two admin surfaces that are NOT under /api/collections and that a server key
  // (a superuser token) would otherwise be allowed to drive:
  //   - Records of PocketBase's internal system collections (_superusers, _otps, _authOrigins, …):
  //     writing _superusers/records lets a leaked key mint or delete superusers (and then authenticate
  //     as one). Only DATBase creates/revokes key accounts, and it always sends the control header.
  //   - /api/settings state changes: a key could rename the project, repoint URLs, or toggle settings.
  //     DATBase's own mail setup (configureProjectMail) sends these with the control header.
  const method = e.request.method;
  if (method !== "GET" && method !== "HEAD" && method !== "OPTIONS") {
    const schemaWrite =
      path === "/api/collections" ||
      path === "/api/collections/import" ||
      /^\/api\/collections\/[^/]+$/.test(path) ||
      /^\/api\/collections\/[^/]+\/truncate$/.test(path) ||
      /^\/api\/collections\/[^/]+\/impersonate\/[^/]+$/.test(path);
    if (schemaWrite) throw new ForbiddenError("Tables and access rules can only be changed in the DATBase dashboard.");
    // Records of internal system collections (name starts with "_"): _superusers, _otps, _authOrigins, …
    // Matches /api/collections/_x/records and /api/collections/_x/records/<id> (and any deeper action).
    const internalRecordWrite = /^\/api\/collections\/_[^/]+\/records(\/|$)/.test(path);
    if (internalRecordWrite) throw new ForbiddenError("Internal accounts can only be changed in the DATBase dashboard.");
    // Any state-changing call to project settings.
    if (path === "/api/settings" || path.startsWith("/api/settings/")) {
      throw new ForbiddenError("Project settings can only be changed in the DATBase dashboard.");
    }
  }
  return e.next();
});

// POST /api/datbase/sql  — read-only SQL for the owner's dashboard SQL editor.
// Security layers (all must pass):
//   1. routerUse gate above: only requests with the control header can reach /api/datbase/* at all.
//   2. Statement allow-list: only SELECT / WITH / EXPLAIN; single statement; no writes/DDL/attach.
//   3. Engine-level read-only: PRAGMA query_only = ON inside a transaction that is always rolled back.
//   4. Internal table/column filter: any reference to a name starting with "_" is rejected;
//      columns tokenKey and password on the users table are stripped from results.
// Row cap: 1 000 rows. Timeout: 10 s (the JS AbortController is the outer bound; the query itself
// uses SQLite's LIMIT so it doesn't keep scanning after 1 000 rows).
routerAdd("POST", "/api/datbase/sql", (e) => {
  const body = e.requestInfo().body || {};
  const sql = String(body.sql || "").trim();

  // ----- 1. Basic length / empty guard -----
  if (!sql) return e.json(400, { error: "No SQL provided." });
  if (sql.length > 102400) return e.json(400, { error: "Query too large (max 100 KB)." });

  // ----- 2. Statement allow-list -----
  // Strip single-line and multi-line comments to avoid hiding a second statement.
  const stripped = sql
    .replace(/--[^\r\n]*/g, " ")
    .replace(/\/\*[\s\S]*?\*\//g, " ")
    .replace(/\s+/g, " ")
    .trim();

  // Must start with SELECT, WITH, or EXPLAIN (case-insensitive).
  if (!/^(select|with|explain)\b/i.test(stripped)) {
    return e.json(400, {
      error: "The SQL editor is read-only. " +
        "Edit data in Tables, or add tables with Import schema.",
    });
  }

  // Reject anything that could be a second statement (semicolons only inside strings are fine but
  // we reject any bare ; that isn't the optional trailing one to keep things simple and safe).
  const bare = stripped.replace(/'(?:[^'\\]|\\.)*'/g, "''").replace(/"(?:[^"\\]|\\.)*"/g, '""');
  const extraSemicolon = bare.replace(/;\s*$/, "").indexOf(";") !== -1;
  if (extraSemicolon) {
    return e.json(400, { error: "Only one statement is allowed per run." });
  }

  // Reject known dangerous keywords even inside the allowed starters.
  if (/\b(attach|detach|load_extension|vacuum|reindex)\b/i.test(bare)) {
    return e.json(400, { error: "That statement type is not allowed." });
  }

  // Reject any PRAGMA that could write state (read-only pragmas are fine in SELECT / EXPLAIN).
  // We allow PRAGMA inside an EXPLAIN, but block direct PRAGMA statements.
  if (/^\s*pragma\b/i.test(stripped)) {
    return e.json(400, { error: "Direct PRAGMA statements are not allowed." });
  }

  // ----- 3. Internal table / column guard -----
  // Reject any reference to a name starting with "_" (quoted or unquoted, any case).
  // This covers _superusers, _params, sqlite_master etc.
  // Pattern: word boundary + optional quote + underscore at the start.
  const nameRef = /(?:^|[\s,.(["'`])["'`]?_/;
  if (nameRef.test(bare)) {
    return e.json(400, { error: "Internal tables are not accessible." });
  }
  // Also block sqlite_master / sqlite_schema / sqlite_sequence.
  if (/\bsqlite_/i.test(bare)) {
    return e.json(400, { error: "SQLite system tables are not accessible." });
  }
  // Block pragma_* table-valued functions (they can expose schema of internal tables).
  if (/\bpragma_/i.test(bare)) {
    return e.json(400, { error: "PRAGMA table functions are not accessible." });
  }
  // Block main."_something" style quoting.
  if (/\bmain\s*\.\s*["'`]?_/i.test(bare)) {
    return e.json(400, { error: "Internal tables are not accessible." });
  }

  // ----- 4. Wrap in LIMIT + run inside always-rolled-back transaction with query_only -----
  // Inject LIMIT 1001 to cap rows (we return at most 1000 and set a "truncated" flag).
  // For WITH / EXPLAIN we prepend the wrapper.
  let limited = stripped.replace(/;\s*$/, "");
  // Only append LIMIT if there isn't one already and the statement is not EXPLAIN.
  if (!/^\s*explain\b/i.test(limited) && !/\blimit\s+\d/i.test(limited)) {
    limited = limited + " LIMIT 1001";
  }

  const ROW_CAP = 1000;
  let columns = [];
  let rows = [];
  let truncated = false;
  const startMs = Date.now();

  try {
    // PRAGMA query_only: engine refuses any statement that would write, even if our text check missed it.
    e.app.db().newQuery("PRAGMA query_only = ON").execute();
    try {
      // Run in a transaction that is always rolled back as an extra write barrier.
      // (query_only already prevents writes; the rollback is belt-and-suspenders.)
      e.app.db().newQuery("BEGIN").execute();
      try {
        const q = e.app.db().newQuery(limited);
        // DynamicModel lets us read any column set.
        // We collect the first row to learn the columns, then iterate.
        const firstModel = new DynamicModel({});
        try {
          q.one(firstModel);
        } catch (_notFound) {
          // Zero rows — still need columns; run an EXPLAIN QUERY PLAN trick isn't reliable.
          // Instead return empty rows; columns will be empty too (acceptable for empty result sets).
          rows = [];
          columns = [];
          const elapsed = Date.now() - startMs;
          return e.json(200, { columns, rows, rowCount: 0, truncated: false, elapsedMs: elapsed });
        }
        // Re-run to get all rows (JSVM doesn't expose an iterator/all() from newQuery easily).
        const allModel = new DynamicModel({});
        const allRows = [];
        // We use the internal .scan approach: iterate via q.each().
        // PocketBase JSVM exposes q.each(fn) which calls fn for each row.
        e.app.db().newQuery(limited).each(allModel, () => {
          const obj = {};
          // DynamicModel doesn't expose keys; serialize/deserialize via JSON to get them.
          const parsed = JSON.parse(JSON.stringify(allModel));
          for (const k in parsed) obj[k] = parsed[k];
          allRows.push(obj);
          return allRows.length <= ROW_CAP; // returning false stops iteration
        });

        if (allRows.length > 0) {
          columns = Object.keys(allRows[0]);
          // Filter secret columns from users table.
          const secretCols = new Set(["password", "tokenkey", "passwordhash"]);
          columns = columns.filter((c) => !secretCols.has(c.toLowerCase()));
          rows = allRows.slice(0, ROW_CAP).map((r) => columns.map((c) => r[c] ?? null));
          truncated = allRows.length > ROW_CAP;
        }
      } finally {
        e.app.db().newQuery("ROLLBACK").execute();
      }
    } finally {
      e.app.db().newQuery("PRAGMA query_only = OFF").execute();
    }
  } catch (err) {
    const elapsed = Date.now() - startMs;
    const msg = String((err && err.message) || err || "Unknown error");
    // Surface a friendly version of the SQLite error.
    return e.json(400, { error: msg, elapsedMs: elapsed });
  }

  const elapsed = Date.now() - startMs;
  return e.json(200, {
    columns,
    rows,
    rowCount: rows.length,
    truncated,
    elapsedMs: elapsed,
  });
});

routerAdd("GET", "/api/session", (e) => {
  if (!e.auth) throw new UnauthorizedError("Not signed in.");
  if (e.auth.isSuperuser()) return e.json(200, { type: "server" });
  return e.json(200, { type: "user", table: e.auth.collection().name, id: e.auth.id });
});

routerAdd("GET", "/api/datbase/usage", (e) => {
  const dir = e.app.dataDir();
  const size = (path) => {
    try {
      return $os.stat(path).size();
    } catch (_) {
      return 0;
    }
  };
  const walk = (path) => {
    let total = 0;
    let entries = [];
    try {
      entries = $os.readDir(path);
    } catch (_) {
      return 0;
    }
    for (const entry of entries) {
      const full = path + "/" + entry.name();
      total += entry.isDir() ? walk(full) : entry.info().size();
    }
    return total;
  };
  // Space actually used by data. The file itself never shrinks after deletes (SQLite reuses the
  // free pages), so measuring file size would keep a project blocked after the customer cleans up.
  let dbBytes = size(dir + "/data.db");
  try {
    const row = new DynamicModel({ used: 0 });
    e.app
      .db()
      .newQuery(
        "SELECT (p.page_count - f.freelist_count) * s.page_size AS used " +
          "FROM pragma_page_count() p, pragma_freelist_count() f, pragma_page_size() s",
      )
      .one(row);
    dbBytes = row.used;
  } catch (_) {}
  return e.json(200, { dbBytes, storageBytes: walk(dir + "/storage") });
});

routerAdd("POST", "/api/datbase/requests/take", (e) => {
  let taken = 0;
  e.app.store().setFunc("datbase_requests", (n) => {
    taken = n || 0;
    return 0;
  });
  return e.json(200, { requests: taken });
});

routerAdd("GET", "/api/datbase/boot", (e) => e.json(200, { bootId: e.app.store().get("datbase_boot") || "" }));

routerAdd("POST", "/api/datbase/limits", (e) => {
  const body = e.requestInfo().body || {};
  const state = {
    blockAll: body.blockAll === true,
    blockWrites: body.blockWrites === true,
    blockSignups: body.blockSignups === true,
  };
  $os.writeFile(e.app.dataDir() + "/datbase_limits.json", JSON.stringify(state), 0o644);
  e.app.store().set("datbase_limits", state);
  return e.json(200, state);
});

onRecordCreateRequest((e) => {
  const secret = $os.getenv("DATBASE_CONTROL_SECRET");
  // DATBase's own internal accounts (server keys) are exempt; everything else, including records the
  // owner creates in the dashboard, follows the plan limits.
  if (e.collection.name === "_superusers" && secret !== "" && e.requestInfo().headers["x_datbase_control"] === secret) return e.next();
  const state = e.app.store().get("datbase_limits") || {};
  if (state.blockWrites) {
    throw new ForbiddenError("This project has reached its storage limit. Delete data or upgrade the plan.");
  }
  if (state.blockSignups && e.collection.isAuth()) {
    throw new ForbiddenError("This project has reached its user limit. Upgrade the plan to add more users.");
  }
  return e.next();
});

onRecordUpdateRequest((e) => {
  const secret = $os.getenv("DATBASE_CONTROL_SECRET");
  // DATBase's own internal accounts (server keys) are exempt; everything else, including records the
  // owner creates in the dashboard, follows the plan limits.
  if (e.collection.name === "_superusers" && secret !== "" && e.requestInfo().headers["x_datbase_control"] === secret) return e.next();
  const state = e.app.store().get("datbase_limits") || {};
  if (state.blockWrites) {
    throw new ForbiddenError("This project has reached its storage limit. Delete data or upgrade the plan.");
  }
  return e.next();
});

/* ---------- Email ---------- */

// Every outgoing email: fixed sender, hourly cap, and only messages built by the handlers below.
onMailerSend((e) => {
  const cfg = e.app.store().get("datbase_mail_cfg") || {};
  if (!cfg.from || !cfg.publicUrl) throw new Error("Email is not set up for this project.");
  const old = e.message.headers;
  if (!old || old["X-Datbase-Mail"] !== "1") throw new Error("This email type is not available.");
  const hdr = {};
  for (const k in old) if (k !== "X-Datbase-Mail") hdr[k] = old[k];
  e.message.headers = hdr;

  const cap = cfg.perHour;
  const hour = Math.floor(Date.now() / 3600000);
  let allowed = true;
  e.app.store().setFunc("datbase_mail", (v) => {
    const cur = v && v.hour === hour ? v : { hour: hour, n: 0 };
    if (cur.n >= cap) allowed = false;
    return { hour: hour, n: cur.n + (allowed ? 1 : 0) };
  });
  if (!allowed) throw new Error("Hourly email limit reached for this project.");

  const name = (e.app.settings().meta.appName || "").replace(/[\r\n<>"]/g, "").slice(0, 60);
  e.message.from = { address: cfg.from, name: name };
  return e.next();
});

onMailerRecordVerificationSend((e) => {
  require(`${__hooks}/datbase_mail.js`).build(e, "verify", "Confirm your email for {APP}", "Confirm your email address for your {APP} account.", "Confirm email");
  return e.next();
});
onMailerRecordPasswordResetSend((e) => {
  require(`${__hooks}/datbase_mail.js`).build(e, "reset-password", "Reset your {APP} password", "Someone (hopefully you) asked to reset your {APP} password. The link works for a limited time.", "Choose a new password");
  return e.next();
});
onMailerRecordEmailChangeSend((e) => {
  require(`${__hooks}/datbase_mail.js`).build(e, "confirm-email", "Confirm your new email for {APP}", "Confirm the new email address for your {APP} account.", "Confirm new email");
  return e.next();
});
// Not offered: login alerts and one-time passwords (dropped silently / refused).
onMailerRecordAuthAlertSend((e) => {});
onMailerRecordOTPSend((e) => {
  throw new BadRequestError("One-time passwords are not available.");
});
