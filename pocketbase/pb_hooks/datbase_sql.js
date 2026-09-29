// Read-only SQL for the dashboard SQL editor ("DATBase SQL"), used by POST /api/datbase/sql in
// datbase.pb.js (loaded with require; PocketBase runs each hook handler in isolation).
//
// Everything here is plain JavaScript over strings and arrays, so it is unit-tested in Node
// (app/tests/unit/sql-guard.test.mjs). The engine-facing part lives in datbase.pb.js.
//
// Layers (each one alone should stop the attack it names; together they must all fail for a leak):
//   1. check(): a real SQLite tokenizer, not regexes over the text. Every identifier in every
//      spelling SQLite accepts (bare, "x", `x`, [x], and 'x' — SQLite takes a single-quoted string
//      wherever a name is allowed) is checked against the internal-name rules. One statement only,
//      SELECT / WITH / VALUES / EXPLAIN only, no parameters, balanced parentheses.
//   2. The user's query becomes a subquery: SELECT … FROM (<query>) LIMIT 1001. A subquery can only
//      be a SELECT, so DML/DDL is a syntax error even if (1) missed it, and the row cap always holds.
//   3. Shadow CTEs: every table that has hidden fields (auth tables: password, tokenKey) is
//      replaced by a CTE of the same name that lists only its visible columns. SQLite resolves an
//      unqualified name to the CTE, so the hidden columns are unreachable ("main." is rejected).
//   4. accessProblem(): the engine's own EXPLAIN of the final statement lists every table and index
//      it opens; anything internal ("_…", "sqlite_…"), any write cursor or any other schema fails.
//   5. datbase.pb.js runs it on one pinned connection with PRAGMA query_only = ON inside a
//      transaction that is always rolled back, and interrupts it when the caller disconnects.

var ROW_CAP = 1000;
var MAX_SQL = 100 * 1024;

// Statement keywords that never belong in a read-only query (checked on unquoted words only, so a
// column quoted as "delete" is still fine). REPLACE and END are left out: replace() and CASE…END.
var WRITE_WORDS = {
  attach: 1, detach: 1, pragma: 1, vacuum: 1, reindex: 1, analyze: 1, insert: 1, update: 1, delete: 1,
  create: 1, drop: 1, alter: 1, begin: 1, commit: 1, rollback: 1, savepoint: 1, release: 1, returning: 1,
};
// Functions and virtual tables that read outside the user's tables or touch files, in any spelling.
// (dbstat and sqlite_dbpage are compiled into the engine; sqlite_dbpage returns raw database pages.)
var BLOCKED_NAMES = {
  load_extension: 1, fts3_tokenizer: 1, dbstat: 1, readfile: 1, writefile: 1, edit: 1, zipfile: 1,
  fsdir: 1, sqlar_compress: 1, sqlar_uncompress: 1,
};
var ALLOWED_SQLITE_FUNCS = { sqlite_version: 1, sqlite_source_id: 1 };

function isInternalName(lower) {
  return lower.charAt(0) === "_" || lower.indexOf("sqlite_") === 0 || lower.indexOf("pragma_") === 0 || BLOCKED_NAMES[lower] === 1;
}

/** SQLite tokenizer. Returns { tokens } or { error }. Token: { t: "id"|"str"|"num"|"blob"|"op", v, quoted, pos, end }. */
function tokenize(sql) {
  var toks = [];
  var i = 0;
  var n = sql.length;
  var idStart = /[A-Za-z_\u0080-￿]/;
  var idPart = /[A-Za-z0-9_$\u0080-￿]/;
  function quoted(open, close, kind, what) {
    var j = i + 1;
    var v = "";
    for (;;) {
      if (j >= n) return { error: what + " is not closed." };
      var ch = sql.charAt(j);
      if (ch === close) {
        if (close !== "]" && sql.charAt(j + 1) === close) {
          v += close;
          j += 2;
          continue;
        }
        break;
      }
      v += ch;
      j++;
    }
    toks.push({ t: kind, v: v, quoted: true, pos: i, end: j + 1 });
    i = j + 1;
    return null;
  }
  while (i < n) {
    var c = sql.charAt(i);
    var err = null;
    if (/\s/.test(c)) {
      i++;
    } else if (c === "-" && sql.charAt(i + 1) === "-") {
      var nl = sql.indexOf("\n", i);
      i = nl < 0 ? n : nl + 1;
    } else if (c === "/" && sql.charAt(i + 1) === "*") {
      var close = sql.indexOf("*/", i + 2);
      if (close < 0) return { error: "A /* comment is not closed." };
      i = close + 2;
    } else if (c === "'") {
      err = quoted("'", "'", "str", "A 'text' value");
    } else if (c === '"') {
      err = quoted('"', '"', "id", 'A "quoted name"');
    } else if (c === "`") {
      err = quoted("`", "`", "id", "A `quoted name`");
    } else if (c === "[") {
      err = quoted("[", "]", "id", "A [quoted name]");
    } else if ((c === "x" || c === "X") && sql.charAt(i + 1) === "'") {
      i++;
      err = quoted("'", "'", "blob", "A x'…' value");
      if (!err) toks[toks.length - 1].pos--;
    } else if (idStart.test(c)) {
      var j = i + 1;
      while (j < n && idPart.test(sql.charAt(j))) j++;
      toks.push({ t: "id", v: sql.slice(i, j), quoted: false, pos: i, end: j });
      i = j;
    } else if (/[0-9]/.test(c) || (c === "." && /[0-9]/.test(sql.charAt(i + 1)))) {
      var k = i + 1;
      while (k < n) {
        var d = sql.charAt(k);
        if (/[0-9A-Za-z_.]/.test(d)) k++;
        else if ((d === "+" || d === "-") && /[eE]/.test(sql.charAt(k - 1)) && !/^0[xX]/.test(sql.slice(i, k))) k++;
        else break;
      }
      toks.push({ t: "num", v: sql.slice(i, k), pos: i, end: k });
      i = k;
    } else if (c === "?" || c === ":" || c === "@" || c === "$" || c === "#") {
      return { error: "Parameters (?, :name, @name, $name) aren't supported here: write the values into the query." };
    } else {
      var three = sql.substr(i, 3);
      var two = sql.substr(i, 2);
      var op = three === "->>" ? three : ["||", "<=", ">=", "==", "!=", "<>", "<<", ">>", "->"].indexOf(two) >= 0 ? two : "()+-*/%<>=&|~,.;".indexOf(c) >= 0 ? c : "";
      if (!op) return { error: 'Unexpected character "' + c + '".' };
      toks.push({ t: "op", v: op, pos: i, end: i + op.length });
      i += op.length;
    }
    if (err) return err;
  }
  return { tokens: toks };
}

var READ_ONLY_HINT = "The SQL editor only reads data: start with SELECT, WITH or EXPLAIN. Change data in Tables; add tables with Import schema.";

/**
 * Validate one read-only query. Returns { error } or { mode: ""|"code"|"plan", body } where body is the
 * query text (without EXPLAIN and the trailing semicolon) to be wrapped by dataSql()/probeSql().
 */
function check(sql) {
  sql = String(sql == null ? "" : sql);
  if (!sql.trim()) return { error: "No SQL provided." };
  if (sql.length > MAX_SQL) return { error: "Query too large (max 100 KB)." };
  // The engine's query builder rewrites these anywhere in the text (even inside quotes).
  if (/\{\{|\}\}|\[\[|\]\]|\{:/.test(sql)) return { error: "Double brackets ([[ ]]) and braces ({{ }}, {:…}) aren't supported in DATBase SQL." };

  var r = tokenize(sql);
  if (r.error) return { error: r.error };
  var toks = r.tokens;
  while (toks.length && toks[toks.length - 1].v === ";" && toks[toks.length - 1].t === "op") toks.pop();
  if (!toks.length) return { error: "No SQL provided." };

  var depth = 0;
  for (var i = 0; i < toks.length; i++) {
    var tk = toks[i];
    var prev = toks[i - 1];
    var next = toks[i + 1];
    var nextIsDot = !!next && next.t === "op" && next.v === ".";
    var prevIsDot = !!prev && prev.t === "op" && prev.v === ".";
    if (tk.t === "op") {
      if (tk.v === ";") return { error: "Only one statement is allowed per run." };
      if (tk.v === "(") depth++;
      if (tk.v === ")" && --depth < 0) return { error: "A closing parenthesis has no opening one." };
      continue;
    }
    if (tk.t === "id") {
      var lower = tk.v.toLowerCase();
      if (!tk.quoted && WRITE_WORDS[lower] === 1) return { error: READ_ONLY_HINT };
      if (isInternalName(lower) && !(ALLOWED_SQLITE_FUNCS[lower] === 1 && !tk.quoted && next && next.v === "(")) {
        return { error: "Internal tables and names starting with \"_\", \"sqlite_\" or \"pragma_\" are not accessible." };
      }
      if ((lower === "main" || lower === "temp" || lower === "temporary") && nextIsDot) {
        return { error: "Write table names without a schema prefix (no main. or temp.)." };
      }
      continue;
    }
    if (tk.t === "str") {
      // SQLite accepts 'text' wherever a name is allowed ('users', 'main'.'users', 'pragma_x'(…)).
      if (next && next.t === "op" && next.v === "(") return { error: "A 'quoted' value can't be used as a function or table name." };
      if (nextIsDot || prevIsDot) return { error: "Use \"double quotes\" (not 'single quotes') around table and column names." };
      var sv = tk.v.toLowerCase();
      if (/^[a-z0-9_$\u0080-￿]+$/.test(sv) && isInternalName(sv)) {
        return { error: "Internal tables and names starting with \"_\", \"sqlite_\" or \"pragma_\" are not accessible." };
      }
    }
  }
  if (depth !== 0) return { error: "An opening parenthesis is not closed." };

  var first = toks[0];
  var mode = "";
  var start = 0;
  if (first.t === "id" && !first.quoted && first.v.toLowerCase() === "explain") {
    mode = "code";
    start = 1;
    var q = toks[1];
    var p = toks[2];
    if (q && q.t === "id" && q.v.toLowerCase() === "query" && p && p.t === "id" && p.v.toLowerCase() === "plan") {
      mode = "plan";
      start = 3;
    }
  }
  var lead = toks[start];
  if (!lead || lead.t !== "id" || lead.quoted || ["select", "with", "values"].indexOf(lead.v.toLowerCase()) < 0) {
    return { error: READ_ONLY_HINT };
  }
  var body = sql.slice(lead.pos, toks[toks.length - 1].end);
  return { mode: mode, body: body };
}

/** "name" with embedded quotes doubled. */
function quoteId(name) {
  return '"' + String(name).replace(/"/g, '""') + '"';
}

/**
 * WITH clause that shadows every table with hidden fields by a CTE of the same name exposing only
 * its visible columns. tables: [{ name, visible: [column names] }]. Returns "" when there are none.
 */
function shadowWith(tables) {
  var parts = [];
  for (var i = 0; i < (tables || []).length; i++) {
    var t = tables[i];
    if (!t || !t.name) continue;
    var visible = t.visible || [];
    // A table whose every field is hidden is still shadowed (by an empty table), never left readable.
    parts.push(
      quoteId(t.name) +
        " AS (SELECT " +
        (visible.length ? visible.map(quoteId).join(", ") + " FROM main." + quoteId(t.name) : 'NULL AS "id" WHERE 0') +
        ")",
    );
  }
  return parts.length ? "WITH " + parts.join(", ") + "\n" : "";
}

/** Learns the result columns without reading rows (the newline keeps a trailing -- comment harmless). */
function probeSql(shadow, body) {
  return shadow + "SELECT * FROM (\n" + body + "\n) LIMIT 0";
}

/**
 * One JSON array per row (keeps types and NULLs; binary values become a short note), capped at
 * ROW_CAP + 1 rows so the caller can tell whether there were more. Columns are chunked because
 * functions take a bounded number of arguments.
 */
function dataSql(shadow, body, columns) {
  var cells = columns.map(function (c) {
    var q = quoteId(c);
    return "CASE WHEN typeof(" + q + ") = 'blob' THEN '(binary, ' || length(" + q + ") || ' bytes)' ELSE " + q + " END";
  });
  var chunks = [];
  for (var i = 0; i < cells.length; i += 100) chunks.push("json_array(" + cells.slice(i, i + 100).join(", ") + ") AS r" + chunks.length);
  return shadow + "SELECT " + chunks.join(", ") + " FROM (\n" + body + "\n) LIMIT " + (ROW_CAP + 1);
}

/** The user's query as it really runs (shadows + subquery + row cap). */
function wrappedSql(shadow, body) {
  return shadow + "SELECT * FROM (\n" + body + "\n) LIMIT " + (ROW_CAP + 1);
}

/** What EXPLAIN [QUERY PLAN] shows: the wrapped query, so the plan matches what really runs. */
function explainSql(mode, shadow, body) {
  return (mode === "plan" ? "EXPLAIN QUERY PLAN " : "EXPLAIN ") + wrappedSql(shadow, body);
}

/**
 * Engine-level access check. opens: EXPLAIN rows [{ opcode, p2, p3 }]; roots: { [rootpage]: table name }
 * from sqlite_schema. Returns an error message, or "" when the statement only reads user tables.
 */
function accessProblem(opens, roots) {
  for (var i = 0; i < opens.length; i++) {
    var o = opens[i];
    if (o.opcode === "OpenWrite") return "The SQL editor is read-only.";
    if (o.opcode !== "OpenRead" && o.opcode !== "ReopenIdx") continue;
    if (Number(o.p3) !== 0) return "Only this project's tables can be read.";
    var name = Number(o.p2) === 1 ? "sqlite_schema" : roots[String(o.p2)];
    if (!name) return "Only this project's tables can be read.";
    if (isInternalName(String(name).toLowerCase())) return "Internal tables are not accessible.";
  }
  return "";
}

/** Rows of JSON-array chunks → { rows, truncated }; long text is shortened so a response stays small. */
function collectRows(chunkRows, chunkCount) {
  var rows = [];
  var bytes = 0;
  var truncated = false;
  for (var i = 0; i < chunkRows.length; i++) {
    if (rows.length >= ROW_CAP) {
      truncated = true;
      break;
    }
    var row = [];
    for (var c = 0; c < chunkCount; c++) {
      var part = JSON.parse(chunkRows[i]["r" + c] || "[]");
      for (var k = 0; k < part.length; k++) {
        var v = part[k];
        if (typeof v === "string" && v.length > 10000) v = v.slice(0, 10000) + "… (" + (v.length - 10000) + " more characters)";
        row.push(v);
      }
    }
    bytes += JSON.stringify(row).length;
    if (bytes > 5 * 1024 * 1024) {
      truncated = true;
      break;
    }
    rows.push(row);
  }
  return { rows: rows, truncated: truncated };
}

/** Engine error → a short message without the engine's internals. */
function friendlyError(err) {
  var msg = String((err && err.message) || err || "Unknown error");
  if (/context canceled|interrupted/i.test(msg)) return "The query was stopped (cancelled or over the 10 s limit).";
  if (/readonly database|read-only/i.test(msg)) return "The SQL editor is read-only.";
  msg = msg.replace(/^GoError:\s*/i, "").replace(/^SQL logic error:\s*/i, "").replace(/\s*\(\d+\)\s*$/, "");
  if (/no such table: _|no such table: sqlite_/i.test(msg)) return "Internal tables are not accessible.";
  return msg.slice(0, 500);
}

module.exports = {
  ROW_CAP: ROW_CAP,
  tokenize: tokenize,
  check: check,
  quoteId: quoteId,
  shadowWith: shadowWith,
  probeSql: probeSql,
  dataSql: dataSql,
  wrappedSql: wrappedSql,
  explainSql: explainSql,
  accessProblem: accessProblem,
  collectRows: collectRows,
  friendlyError: friendlyError,
};
