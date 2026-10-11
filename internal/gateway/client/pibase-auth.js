/*
 * pibase browser sign-in client.
 *
 * Keeps one sign-in shared by every tab of a site: signing in or out in one
 * tab does the same in the others, and when the access token is due only one
 * tab refreshes it while the rest wait for and reuse its result.
 *
 *   <script src="https://api.example.com/client/v1/pibase-auth.js"></script>
 *   const auth = PibaseAuth.createClient({ url: "https://api.example.com", anonKey })
 *   await auth.signIn(email, password)
 *   const res = await auth.fetch("/rest/v1/notes?select=*")
 *   auth.onAuthStateChange((event, session) => { ... })
 *
 * No dependencies. Works as a plain script (window.PibaseAuth) or a CommonJS
 * module.
 */
(function (root, factory) {
  if (typeof module === "object" && module.exports) module.exports = factory();
  else root.PibaseAuth = factory();
})(typeof self !== "undefined" ? self : this, function () {
  "use strict";

  var REFRESH_MARGIN_MS = 60 * 1000;
  var LOCK_EXPIRY_MS = 10 * 1000;

  function sleep(ms) {
    return new Promise(function (resolve) { setTimeout(resolve, ms); });
  }

  function randomID() {
    if (typeof crypto !== "undefined" && crypto.randomUUID) return crypto.randomUUID();
    return Date.now().toString(36) + Math.random().toString(36).slice(2);
  }

  // Storage that falls back to memory when localStorage is unavailable
  // (blocked cookies, some private windows). Without it tabs can't share a
  // sign-in, but the current tab still works.
  function makeStorage() {
    var memory = {};
    var local = null;
    try {
      local = window.localStorage;
      var probe = "__pibase_probe__";
      local.setItem(probe, probe);
      local.removeItem(probe);
    } catch (e) {
      local = null;
    }
    return {
      shared: !!local,
      get: function (k) {
        if (local) { try { return local.getItem(k); } catch (e) { /* fall through */ } }
        return Object.prototype.hasOwnProperty.call(memory, k) ? memory[k] : null;
      },
      set: function (k, v) {
        if (local) { try { local.setItem(k, v); return; } catch (e) { /* fall through */ } }
        memory[k] = v;
      },
      remove: function (k) {
        if (local) { try { local.removeItem(k); } catch (e) { /* fall through */ } }
        delete memory[k];
      },
    };
  }

  function AuthError(message, status) {
    var err = new Error(message);
    err.name = "AuthError";
    err.status = status;
    return err;
  }

  async function errorFrom(res) {
    var message = res.status + " " + res.statusText;
    try {
      var body = await res.json();
      message = body.message || body.msg || body.error_description || body.error || message;
    } catch (e) { /* not JSON */ }
    return AuthError(message, res.status);
  }

  function createClient(options) {
    if (!options || !options.url || !options.anonKey) throw new Error("PibaseAuth.createClient needs url and anonKey");
    var url = String(options.url).replace(/\/+$/, "");
    var anonKey = options.anonKey;
    var key = options.storageKey || "pibase.auth:" + url;
    var lockKey = key + ":refresh-lock";
    var useLocks = options.locks !== false && typeof navigator !== "undefined" && navigator.locks && typeof navigator.locks.request === "function";
    var autoRefresh = options.autoRefresh !== false;
    var store = makeStorage();
    var tabID = randomID();
    var listeners = [];
    var known = read(); // the session this tab last told its listeners about
    var inflight = null; // this tab's refresh in progress
    var timer = null;
    var channel = null;

    function read() {
      var raw = store.get(key);
      if (!raw) return null;
      try {
        var s = JSON.parse(raw);
        return s && s.access_token && s.refresh_token ? s : null;
      } catch (e) {
        return null;
      }
    }

    function sessionFrom(body) {
      return {
        access_token: body.access_token,
        refresh_token: body.refresh_token,
        token_type: body.token_type || "bearer",
        expires_in: body.expires_in,
        expires_at: body.expires_at || Math.floor(Date.now() / 1000) + (body.expires_in || 3600),
        user: body.user || null,
      };
    }

    function write(session) {
      if (session) store.set(key, JSON.stringify(session));
      else store.remove(key);
      if (channel) { try { channel.postMessage({ from: tabID }); } catch (e) { /* closed */ } }
      sync();
    }

    // sync compares storage with what this tab last reported and fires one
    // event for the difference. Every change, from this tab or another, goes
    // through here, so each tab reports each change exactly once.
    function sync() {
      var now = read();
      var before = known;
      known = now;
      schedule();
      var event = null;
      if (!before && now) event = "SIGNED_IN";
      else if (before && !now) event = "SIGNED_OUT";
      else if (before && now && before.refresh_token !== now.refresh_token) {
        event = before.user && now.user && before.user.id !== now.user.id ? "SIGNED_IN" : "TOKEN_REFRESHED";
      }
      if (event) emit(event, now);
    }

    function emit(event, session) {
      listeners.slice().forEach(function (cb) {
        try { cb(event, session); } catch (e) { setTimeout(function () { throw e; }); }
      });
    }

    function schedule() {
      if (timer) { clearTimeout(timer); timer = null; }
      if (!autoRefresh || !known) return;
      // A little jitter so tabs opened together don't all wake at once; the
      // lock makes sure only one of them calls the server anyway.
      var due = known.expires_at * 1000 - REFRESH_MARGIN_MS - Date.now() + Math.random() * 5000;
      timer = setTimeout(function () {
        refresh().catch(function () { /* retried on next use */ });
      }, Math.max(1000, Math.min(due, 0x7fffffff)));
    }

    // Only one tab at a time refreshes. navigator.locks is exact; the
    // localStorage fallback can very rarely let two tabs in, which is safe:
    // the server accepts a refresh token twice within a short window, and the
    // second tab then finds the first one's result in storage.
    async function withLock(fn) {
      if (useLocks) return navigator.locks.request(lockKey, fn);
      if (!store.shared) return fn();
      var token = tabID + ":" + randomID();
      for (;;) {
        var held = null;
        try { held = JSON.parse(store.get(lockKey) || "null"); } catch (e) { held = null; }
        if (!held || held.until < Date.now()) {
          store.set(lockKey, JSON.stringify({ token: token, until: Date.now() + LOCK_EXPIRY_MS }));
          await sleep(25);
          try { held = JSON.parse(store.get(lockKey) || "null"); } catch (e) { held = null; }
          if (held && held.token === token) break;
        }
        await sleep(50 + Math.random() * 50);
      }
      try {
        return await fn();
      } finally {
        try {
          var current = JSON.parse(store.get(lockKey) || "null");
          if (current && current.token === token) store.remove(lockKey);
        } catch (e) { /* ignore */ }
      }
    }

    function fresh(session) {
      return session && session.expires_at * 1000 - REFRESH_MARGIN_MS > Date.now();
    }

    // refresh gets a new access token, or reuses one another tab just got.
    // rejected names an access token the server refused; a stored token that
    // differs from it is used instead of refreshing again.
    function refresh(rejected) {
      if (inflight) return inflight;
      inflight = withLock(async function () {
        var current = read();
        if (!current) return null;
        if (rejected ? current.access_token !== rejected : fresh(current)) {
          sync();
          return current;
        }
        var res;
        try {
          res = await fetch(url + "/auth/v1/token?grant_type=refresh_token", {
            method: "POST",
            headers: { apikey: anonKey, "Content-Type": "application/json" },
            body: JSON.stringify({ refresh_token: current.refresh_token }),
          });
        } catch (e) {
          throw AuthError("could not reach the server to refresh the session", 0);
        }
        // While the request ran this tab or another may have signed out or
        // signed in as someone else. Only the session we refreshed may be
        // replaced; anything else in storage stays as it is.
        var latest = read();
        var ours = latest && latest.refresh_token === current.refresh_token;
        if (!res.ok) {
          if ((res.status === 400 || res.status === 401) && ours) {
            write(null); // the sign-in ended on the server (signed out, banned, expired)
            return null;
          }
          if (!ours) return latest;
          throw await errorFrom(res);
        }
        var next = sessionFrom(await res.json());
        if (!ours) return latest;
        if (!next.user) next.user = current.user;
        write(next);
        return next;
      });
      var p = inflight;
      p.then(clear, clear);
      function clear() { if (inflight === p) inflight = null; }
      return p;
    }

    async function post(path, body, bearer) {
      var headers = { apikey: anonKey, "Content-Type": "application/json" };
      if (bearer) headers.Authorization = "Bearer " + bearer;
      var res;
      try {
        res = await fetch(url + path, { method: "POST", headers: headers, body: JSON.stringify(body || {}) });
      } catch (e) {
        throw AuthError("could not reach the server", 0);
      }
      if (!res.ok) throw await errorFrom(res);
      return res.status === 204 ? null : res.json();
    }

    var client = {
      // signUp creates an account. Unless the project confirms emails
      // automatically, the user must confirm before signing in, and the
      // returned session is null.
      signUp: async function (email, password, data) {
        var body = await post("/auth/v1/signup", { email: email, password: password, data: data || {} });
        if (body && body.access_token) {
          var s = sessionFrom(body);
          write(s);
          return { user: s.user, session: s };
        }
        return { user: body && body.user, session: null };
      },

      signIn: async function (email, password) {
        var body = await post("/auth/v1/token?grant_type=password", { email: email, password: password });
        var s = sessionFrom(body);
        write(s);
        return s;
      },
      signInUsername: async function (username, password) {
        var body = await post("/auth/v1/token?grant_type=password", { username: username, password: password });
        var s = sessionFrom(body);
        write(s);
        return s;
      },

      // signOut ends the sign-in in every tab at once, then on the server,
      // which stops every token of it working and closes its realtime
      // connections. A refresh still running somewhere can't bring it back.
      signOut: async function () {
        var s = read();
        write(null);
        if (!s) return;
        try {
          await post("/auth/v1/logout", {}, s.access_token);
        } catch (e) {
          if (e.status !== 401) throw e;
          // The access token ran out; trade the refresh token for one that
          // can authorize the sign-out. Both belong to the same sign-in.
          var next;
          try {
            next = await post("/auth/v1/token?grant_type=refresh_token", { refresh_token: s.refresh_token });
          } catch (e2) {
            if (e2.status === 400 || e2.status === 401) return; // already ended
            throw e2;
          }
          await post("/auth/v1/logout", {}, next.access_token);
        }
      },

      session: function () { return read(); },

      user: function () {
        var s = read();
        return s ? s.user : null;
      },

      // accessToken returns a valid access token, refreshing it first when it
      // is about to expire, or null when nobody is signed in.
      accessToken: async function () {
        var s = read();
        if (!s) return null;
        if (fresh(s)) return s.access_token;
        var next = await refresh();
        return next ? next.access_token : null;
      },

      // fetch calls the gateway with the project key and the signed-in user's
      // token. If the server refuses the token it refreshes once and retries.
      fetch: async function (path, init) {
        init = Object.assign({}, init || {});
        var target = /^https?:\/\//.test(path) ? path : url + path;
        async function send(token) {
          var headers = new Headers(init.headers || {});
          headers.set("apikey", anonKey);
          headers.set("Authorization", "Bearer " + (token || anonKey));
          return fetch(target, Object.assign({}, init, { headers: headers }));
        }
        var token = await client.accessToken();
        var res = await send(token);
        if (res.status !== 401 || !token) return res;
        var next = await refresh(token);
        if (!next || next.access_token === token) return res;
        return send(next.access_token);
      },

      refreshSession: function () {
        var s = read();
        return refresh(s ? s.access_token : undefined);
      },

      // onAuthStateChange calls cb(event, session) for SIGNED_IN, SIGNED_OUT
      // and TOKEN_REFRESHED, whichever tab caused them. It returns a function
      // that stops the calls.
      onAuthStateChange: function (cb) {
        listeners.push(cb);
        return function () {
          listeners = listeners.filter(function (l) { return l !== cb; });
        };
      },

      // destroy stops timers and cross-tab listening, for single-page apps
      // that create and drop clients.
      destroy: function () {
        if (timer) clearTimeout(timer);
        timer = null;
        autoRefresh = false;
        if (channel) channel.close();
        channel = null;
        if (typeof window !== "undefined") window.removeEventListener("storage", onStorage);
        listeners = [];
      },
    };

    function onStorage(e) {
      if (e.key === key || e.key === null) sync();
    }
    if (typeof window !== "undefined" && window.addEventListener) window.addEventListener("storage", onStorage);
    if (typeof BroadcastChannel !== "undefined") {
      try {
        channel = new BroadcastChannel(key);
        channel.onmessage = function (e) { if (!e.data || e.data.from !== tabID) sync(); };
      } catch (e) { channel = null; }
    }
    schedule();
    return client;
  }

  // Cookie mode keeps the refresh token in an httpOnly cookie and the access
  // token only in this tab's memory. It never touches localStorage.
  function createCookieClient(options) {
    if (!options || !options.url || !options.anonKey) throw new Error("PibaseAuth.createCookieClient needs url and anonKey");
    var url = String(options.url).replace(/\/+$/, "");
    var anonKey = options.anonKey;
    var session = null;
    var listeners = [];
    var inflight = null;
    var timer = null;
    var channel = null;
    var tabID = randomID();
    var generation = 0;

    function notify(event) { listeners.slice().forEach(function (cb) { cb(event, session); }); }
    function save(next, event) {
      generation++;
      session = next;
      if (timer) clearTimeout(timer);
      timer = null;
      if (next && options.autoRefresh !== false) {
        timer = setTimeout(function () { refresh().catch(function () {}); },
          Math.max(1000, next.expires_at * 1000 - REFRESH_MARGIN_MS - Date.now()));
      }
      if (event) notify(event);
    }
    function fromBody(body) {
      return { access_token: body.access_token, token_type: body.token_type || "bearer",
        expires_in: body.expires_in, expires_at: Math.floor(Date.now() / 1000) + (body.expires_in || 3600),
        user: body.user || null };
    }
    async function post(path, body, bearer) {
      var headers = { apikey: anonKey, "Content-Type": "application/json", "X-Pibase-Session-Mode": "cookie" };
      if (bearer) headers.Authorization = "Bearer " + bearer;
      var res = await fetch(url + path, { method: "POST", credentials: "include", headers: headers, body: JSON.stringify(body || {}) });
      if (!res.ok) throw await errorFrom(res);
      return res.status === 204 ? null : res.json();
    }
    function refresh() {
      if (inflight) return inflight;
      var previous = session;
      var started = generation;
      inflight = (async function () {
        try {
          var body = await post("/auth/v1/token?grant_type=refresh_token", {});
          // A sign-out made while refresh was in flight must win.
          if (generation !== started) return session;
          var next = fromBody(body);
          save(next, previous ? "TOKEN_REFRESHED" : "SIGNED_IN");
          return next;
        } catch (e) {
          if (e.status === 400 || e.status === 401) {
            if (generation === started && previous) save(null, "SIGNED_OUT");
            return null;
          }
          throw e;
        }
      })();
      var current = inflight;
      current.then(function () { if (inflight === current) inflight = null; },
        function () { if (inflight === current) inflight = null; });
      return current;
    }
    function broadcast(action) { if (channel) channel.postMessage({ from: tabID, action: action }); }
    if (typeof BroadcastChannel !== "undefined") {
      try {
        channel = new BroadcastChannel("pibase.cookie:" + url);
        channel.onmessage = function (message) {
          if (!message.data || message.data.from === tabID) return;
          if (message.data.action === "signout") save(null, session ? "SIGNED_OUT" : null);
          if (message.data.action === "signin") refresh().catch(function () {});
        };
      } catch (e) { channel = null; }
    }
    var client = {
      signIn: async function (email, password) {
        var next = fromBody(await post("/auth/v1/token?grant_type=password", { email: email, password: password }));
        save(next, "SIGNED_IN"); broadcast("signin"); return next;
      },
      signInUsername: async function (username, password) {
        var next = fromBody(await post("/auth/v1/token?grant_type=password", { username: username, password: password }));
        save(next, "SIGNED_IN"); broadcast("signin"); return next;
      },
      signOut: async function () {
        var current = session || await refresh();
        save(null, current ? "SIGNED_OUT" : null);
        broadcast("signout");
        if (current) await post("/auth/v1/logout", {}, current.access_token);
      },
      session: function () { return session; },
      user: function () { return session ? session.user : null; },
      accessToken: async function () {
        if (session && session.expires_at * 1000 - REFRESH_MARGIN_MS > Date.now()) return session.access_token;
        var next = await refresh(); return next ? next.access_token : null;
      },
      refreshSession: refresh,
      fetch: async function (path, init) {
        init = Object.assign({}, init || {});
        var target = /^https?:\/\//.test(path) ? path : url + path;
        async function send(token) {
          var headers = new Headers(init.headers || {});
          headers.set("apikey", anonKey);
          headers.set("Authorization", "Bearer " + (token || anonKey));
          return fetch(target, Object.assign({}, init, { headers: headers, credentials: "include" }));
        }
        var token = await client.accessToken();
        var res = await send(token);
        if (res.status !== 401 || !token) return res;
        var next = await refresh();
        return next ? send(next.access_token) : res;
      },
      onAuthStateChange: function (cb) {
        listeners.push(cb);
        return function () { listeners = listeners.filter(function (item) { return item !== cb; }); };
      },
      destroy: function () {
        if (timer) clearTimeout(timer);
        if (channel) channel.close();
        listeners = [];
      },
    };
    return client;
  }

  return { createClient: createClient, createCookieClient: createCookieClient, version: "1" };
});
