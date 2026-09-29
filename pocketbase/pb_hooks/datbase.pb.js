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
//   POST /api/datbase/sql            read-only query for the dashboard SQL editor {sql} (see datbase_sql.js)
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

// POST /api/datbase/sql {sql} — read-only "DATBase SQL" for the owner's dashboard SQL editor.
// Reachable only with the control header (the routerUse gate above returns 404 to server keys and app
// users). The checks, rewriting and their reasons live in datbase_sql.js (unit-tested in Node); this
// handler adds the engine-level parts:
//   - one pinned read connection (a transaction that is always rolled back) with PRAGMA query_only = ON,
//     reset before the connection goes back to the pool;
//   - EXPLAIN of each statement before it runs: every table/index it opens must be a user table;
//   - the query is interrupted when the caller disconnects (the dashboard aborts at 10 s or on Cancel).
// Not counted as an API request (control calls skip the counter). Result: {columns, rows, rowCount,
// truncated, elapsedMs} or {error, elapsedMs}.
routerAdd("POST", "/api/datbase/sql", (e) => {
  const S = require(`${__hooks}/datbase_sql.js`);
  const started = Date.now();
  const checked = S.check((e.requestInfo().body || {}).sql);
  if (checked.error) return e.json(400, { error: checked.error, elapsedMs: Date.now() - started });

  // Tables with hidden fields (every auth table: password, tokenKey) are replaced by CTEs that list
  // only their visible columns, so no spelling of the query can read a hidden column.
  const shadows = [];
  for (const c of e.app.findAllCollections()) {
    if (c.name.charAt(0) === "_") continue;
    let hidden = false;
    const visible = [];
    for (const f of c.fields) {
      if (f.getHidden()) hidden = true;
      else visible.push(f.getName());
    }
    if (hidden) shadows.push({ name: c.name, visible });
  }
  const shadow = S.shadowWith(shadows);
  const ctx = e.request.context();
  const tx = e.app.concurrentDB().begin();
  let status = 200;
  let out;
  try {
    tx.newQuery("PRAGMA query_only = ON").execute();
    try {
      const roots = {};
      const schema = arrayOf(new DynamicModel({ tbl_name: "", rootpage: 0 }));
      tx.newQuery("SELECT tbl_name, rootpage FROM sqlite_schema WHERE rootpage > 0").all(schema);
      for (const r of schema) roots[String(r.rootpage)] = r.tbl_name;
      const denied = (sql) => {
        const ops = arrayOf(new DynamicModel({ opcode: "", p2: 0, p3: 0 }));
        tx.newQuery("EXPLAIN " + sql).withContext(ctx).all(ops);
        return S.accessProblem(ops, roots);
      };

      if (checked.mode) {
        const problem = denied(S.wrappedSql(shadow, checked.body));
        if (problem) {
          status = 400;
          out = { error: problem };
        } else {
          const plan = checked.mode === "plan";
          const columns = plan ? ["id", "parent", "detail"] : ["addr", "opcode", "p1", "p2", "p3", "p5"];
          const shape = {};
          for (const c of columns) shape[c] = c === "detail" || c === "opcode" ? "" : 0;
          const list = arrayOf(new DynamicModel(shape));
          tx.newQuery(S.explainSql(checked.mode, shadow, checked.body)).withContext(ctx).all(list);
          const rows = list.slice(0, S.ROW_CAP).map((r) => columns.map((c) => r[c]));
          out = { columns, rows, rowCount: rows.length, truncated: list.length > S.ROW_CAP };
        }
      } else {
        const probe = S.probeSql(shadow, checked.body);
        let problem = denied(probe);
        let columns = [];
        if (!problem) {
          const rs = tx.newQuery(probe).withContext(ctx).rows();
          try {
            columns = rs.columns();
          } finally {
            rs.close();
          }
        }
        const data = problem ? "" : S.dataSql(shadow, checked.body, columns);
        if (!problem) problem = denied(data);
        if (problem) {
          status = 400;
          out = { error: problem };
        } else {
          const chunks = Math.ceil(columns.length / 100);
          const shape = {};
          for (let i = 0; i < chunks; i++) shape["r" + i] = "";
          const list = arrayOf(new DynamicModel(shape));
          tx.newQuery(data).withContext(ctx).all(list);
          const got = S.collectRows(list, chunks);
          out = { columns, rows: got.rows, rowCount: got.rows.length, truncated: got.truncated };
        }
      }
    } finally {
      tx.newQuery("PRAGMA query_only = OFF").execute();
    }
  } catch (err) {
    status = 400;
    out = { error: S.friendlyError(err) };
    // Operator log (no query text): shows a Cancel or the 10 s limit really stopped the engine.
    if (/context canceled|interrupt/i.test(String(err))) console.log("[datbase-sql] query stopped after " + (Date.now() - started) + " ms");
  } finally {
    try {
      tx.rollback();
    } catch (_) {}
  }
  out.elapsedMs = Date.now() - started;
  return e.json(status, out);
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
