// Checks the browser sign-in client (/client/v1/pibase-auth.js) in Chromium
// with two tabs of one site. Run by TestBrowserAuthClient, which starts the
// gateway, a page server on another origin, and passes their addresses:
//   PIBASE_TEST_URL, PIBASE_TEST_PAGE_URL, PIBASE_TEST_ANON_KEY,
//   PIBASE_TEST_SERVICE_KEY, PIBASE_TEST_EMAIL_PREFIX, PIBASE_TEST_PASSWORD
// Needs the playwright package (NODE_PATH may point at it).
const assert = require("node:assert/strict");
const { chromium } = require("playwright");

const url = process.env.PIBASE_TEST_URL;
const pageURL = process.env.PIBASE_TEST_PAGE_URL;
const anonKey = process.env.PIBASE_TEST_ANON_KEY;
const serviceKey = process.env.PIBASE_TEST_SERVICE_KEY;
const prefix = process.env.PIBASE_TEST_EMAIL_PREFIX;
const password = process.env.PIBASE_TEST_PASSWORD;
assert(url && pageURL && anonKey && serviceKey && prefix && password, "test addresses and keys are required");

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function waitFor(fn, what, ms = 5000) {
  const until = Date.now() + ms;
  for (;;) {
    const v = await fn();
    if (v) return v;
    if (Date.now() > until) throw new Error("timed out waiting for " + what);
    await sleep(50);
  }
}

async function admin(method, path, body) {
  const res = await fetch(url + path, {
    method,
    headers: { apikey: serviceKey, Authorization: "Bearer " + serviceKey, "Content-Type": "application/json" },
    body: body && JSON.stringify(body),
  });
  assert(res.ok, `${method} ${path}: ${res.status}`);
  return res.json();
}

async function userStatus(token) {
  const res = await fetch(url + "/auth/v1/user", { headers: { apikey: anonKey, Authorization: "Bearer " + token } });
  return res.status;
}

async function run(browser, locks) {
  const label = locks ? "navigator.locks" : "localStorage lock";
  const email = `${prefix}-${locks ? "locks" : "nolocks"}@example.test`;
  const created = await admin("POST", "/auth/v1/admin/users", { email, password, email_confirm: true });
  const userID = created.id || created.user.id;
  const context = await browser.newContext();
  const errors = [];
  let refreshes = 0;
  let delayRefresh = 0;
  await context.route("**/auth/v1/token?grant_type=refresh_token", async (route) => {
    refreshes++;
    const response = await route.fetch();
    if (delayRefresh) await sleep(delayRefresh);
    await route.fulfill({ response });
  });
  const open = async () => {
    const page = await context.newPage();
    page.on("pageerror", (e) => errors.push(e.message));
    // The browser logs every refused request (the ban step makes some on
    // purpose); only other console errors count.
    page.on("console", (m) => { if (m.type() === "error" && !m.text().startsWith("Failed to load resource")) errors.push(m.text()); });
    const q = new URLSearchParams({ url, key: anonKey, locks: locks ? "1" : "0" });
    await page.goto(`${pageURL}/?${q}`);
    await page.waitForFunction(() => window.auth);
    assert.equal(await page.evaluate(() => window.usesLocks), locks, `${label}: lock mode`);
    return page;
  };
  const a = await open();
  const b = await open();
  const events = (p) => p.evaluate(() => window.events.slice());
  const session = (p) => p.evaluate(() => window.auth.session());
  // Make the stored access token look due, as it would be an hour later.
  const expire = (p) => p.evaluate(() => {
    const k = window.storageKey;
    const s = JSON.parse(localStorage.getItem(k));
    s.expires_at = Math.floor(Date.now() / 1000) - 5;
    localStorage.setItem(k, JSON.stringify(s));
  });

  // 1. Signing in in one tab signs in the other.
  await a.evaluate(([e, pw]) => window.auth.signIn(e, pw), [email, password]);
  await waitFor(async () => (await events(b)).includes("SIGNED_IN"), `${label}: SIGNED_IN in tab B`);
  assert.equal((await session(b)).user.email, email);

  // 2. When the token is due and both tabs need it at once, one refresh
  // happens and every caller gets its result.
  const before = (await session(a)).access_token;
  await expire(a);
  refreshes = 0;
  delayRefresh = 300;
  const tokens = await Promise.all([a, b, a, b, a, b].map((p) => p.evaluate(() => window.auth.accessToken())));
  delayRefresh = 0;
  assert.equal(refreshes, 1, `${label}: refresh requests for 6 concurrent callers in 2 tabs`);
  assert(tokens.every((t) => t && t === tokens[0]), `${label}: all callers share one new token`);
  assert.notEqual(tokens[0], before);
  assert.equal(await userStatus(tokens[0]), 200);
  await waitFor(async () => (await events(b)).includes("TOKEN_REFRESHED"), `${label}: TOKEN_REFRESHED in tab B`);
  assert.equal((await session(b)).access_token, tokens[0]);

  // 3. fetch() adds the key and token.
  const me = await b.evaluate(async () => {
    const res = await window.auth.fetch("/auth/v1/user");
    return { status: res.status, body: await res.json() };
  });
  assert.equal(me.status, 200);
  assert.equal(me.body.email, email);

  // 4. A sign-in the server ends (here a ban) signs out every tab on the next
  // request: the 401 makes one refresh, which the server refuses.
  await admin("PUT", `/auth/v1/admin/users/${userID}`, { ban_duration: "1h" });
  const banned = await a.evaluate(async () => (await window.auth.fetch("/auth/v1/user")).status);
  assert.equal(banned, 401);
  await waitFor(async () => (await session(b)) === null, `${label}: tab B signed out after ban`);
  assert((await events(b)).includes("SIGNED_OUT"));
  await admin("PUT", `/auth/v1/admin/users/${userID}`, { ban_duration: "none" });

  // 5. Signing out in one tab signs out the other, and the server refuses
  // the old token.
  await b.evaluate(([e, pw]) => window.auth.signIn(e, pw), [email, password]);
  await waitFor(async () => (await session(a)) !== null, `${label}: tab A signed in again`);
  const old = (await session(a)).access_token;
  await a.evaluate(() => { window.events.length = 0; });
  await b.evaluate(() => window.auth.signOut());
  await waitFor(async () => (await events(a)).includes("SIGNED_OUT"), `${label}: SIGNED_OUT in tab A`);
  assert.equal(await session(a), null);
  assert.equal(await userStatus(old), 401);

  // 6. A refresh still on its way back when the user signs out can't sign
  // them back in: the server has already ended the token it returns, and
  // the client drops it.
  await a.evaluate(([e, pw]) => window.auth.signIn(e, pw), [email, password]);
  await waitFor(async () => (await session(b)) !== null, `${label}: tab B signed in for late refresh`);
  await expire(a);
  await a.evaluate(() => { window.events.length = 0; });
  await b.evaluate(() => { window.events.length = 0; });
  delayRefresh = 1200;
  refreshes = 0;
  const late = a.evaluate(() => window.auth.accessToken());
  await waitFor(async () => refreshes === 1, `${label}: refresh started`);
  await sleep(200);
  await b.evaluate(() => window.auth.signOut());
  assert.equal(await late, null, `${label}: late refresh result was used`);
  delayRefresh = 0;
  await sleep(300);
  for (const p of [a, b]) {
    assert.equal(await session(p), null, `${label}: session came back after sign-out`);
    assert.deepEqual(await events(p), ["SIGNED_OUT"], `${label}: events after sign-out`);
  }

  assert.deepEqual(errors, [], `${label}: page errors`);
  await context.close();
  console.log(`auth client (${label}): ok`);
}

(async () => {
  const browser = await chromium.launch(process.env.PIBASE_TEST_CHROMIUM ? { executablePath: process.env.PIBASE_TEST_CHROMIUM } : {});
  try {
    await run(browser, true);
    await run(browser, false);
  } finally {
    await browser.close();
  }
})().catch((e) => {
  console.error(e);
  process.exit(1);
});
