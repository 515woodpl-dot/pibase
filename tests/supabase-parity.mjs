// Supabase-parity acceptance test against a running pibase. Every check
// states what Supabase does, then exercises pibase the way a person or an app
// would: the dashboard SQL tab and Users page over HTTP, and the app through
// @supabase/supabase-js. It creates one temporary project and deletes it.
//
//   PIBASE_TEST_EMAIL=you@example.com PIBASE_TEST_PASSWORD=... \
//     node tests/supabase-parity.mjs
//
// PIBASE_DASHBOARD_URL and PIBASE_API_URL default to localhost :18081/:18080.
import { createClient } from '@supabase/supabase-js'

const DASH = process.env.PIBASE_DASHBOARD_URL || 'http://127.0.0.1:18081'
const API = process.env.PIBASE_API_URL || 'http://127.0.0.1:18080'
const results = []
let cookie = ''

async function dash(path, opt = {}) {
  const r = await fetch(DASH + path, { ...opt, headers: { 'Content-Type': 'application/json', Cookie: cookie, ...(opt.headers || {}) } })
  const set = r.headers.get('set-cookie'); if (set) cookie = set.split(';')[0]
  const t = await r.text()
  if (!r.ok) throw new Error(`${r.status} ${t.trim()}`)
  return t ? JSON.parse(t) : null
}
const post = (path, v, method = 'POST') => dash(path, { method, body: JSON.stringify(v) })

async function check(name, supabase, fn) {
  try { const note = await fn(); results.push({ ok: true, name, supabase, note: note || '' }) }
  catch (e) { results.push({ ok: false, name, supabase, note: String(e.message || e).slice(0, 300) }) }
}
function expect(cond, msg) { if (!cond) throw new Error(msg) }
async function rejects(fn, re) {
  try { await fn() } catch (e) { if (re && !re.test(e.message)) throw new Error('wrong error: ' + e.message); return 'refused: ' + e.message.slice(0, 90) }
  throw new Error('was NOT refused')
}

if (!process.env.PIBASE_TEST_EMAIL || !process.env.PIBASE_TEST_PASSWORD) throw new Error('set PIBASE_TEST_EMAIL and PIBASE_TEST_PASSWORD to a dashboard account')
await post('/_pibase/auth/login', { email: process.env.PIBASE_TEST_EMAIL, password: process.env.PIBASE_TEST_PASSWORD })
const project = await post('/_pibase/projects', { name: 'Parity test ' + Date.now() })
const P = '/_pibase/projects/' + project.id
const keys = await dash(P + '/keys')
const sql = (text) => post(P + '/sql', { sql: text })
const anon = createClient(API, keys.anon_key, { auth: { persistSession: false } })
const service = createClient(API, keys.service_key, { auth: { persistSession: false, autoRefreshToken: false } })

// ---------- SQL editor ----------
await check('SQL editor runs a multi-statement script', 'Supabase SQL editor runs a whole script and shows the last result',
  async () => {
    const r = await sql(`create table todos (id bigint generated always as identity primary key, title text not null, done boolean default false, owner uuid default auth.uid());
insert into todos(title) values ('one'),('two');
select id, title, done from todos order by id;`)
    expect(r.rows && r.rows.length === 2, 'expected 2 rows, got ' + JSON.stringify(r))
    expect(r.rows[0].id === 1 && r.rows[0].done === false, 'types not preserved: ' + JSON.stringify(r.rows[0]))
    return `${r.rows.length} rows, id is ${typeof r.rows[0].id}`
  })
await check('GRANT on a table to anon / authenticated', 'Allowed in Supabase SQL editor',
  async () => { const r = await sql(`grant select on todos to anon; grant select, insert, update, delete on todos to authenticated;`); return r.command })
await check('REVOKE on a table', 'Allowed in Supabase SQL editor',
  async () => { const r = await sql(`revoke delete on todos from anon`); return r.command })
await check('DO $$ … $$ block', 'Allowed in Supabase SQL editor',
  async () => {
    await sql(`do $$ begin
  if not exists (select 1 from pg_policies where tablename='todos' and policyname='anon read') then
    create policy "anon read" on todos for select to anon using (true);
  end if;
end $$;`)
    const r = await sql(`select policyname from pg_policies where tablename='todos'`)
    expect(r.rows.some(x => x.policyname === 'anon read'), 'policy missing')
  })
await check('RLS policy with auth.uid()', 'Standard Supabase pattern',
  async () => { await sql(`create policy "own rows" on todos for all to authenticated using (owner = auth.uid()) with check (owner = auth.uid())`) })
await check('ALTER DEFAULT PRIVILEGES', 'Allowed in Supabase SQL editor',
  async () => { await sql(`alter default privileges in schema public grant select on tables to anon`) })
await check('Statement error rolls the whole script back', 'Supabase runs the script as one transaction',
  async () => {
    await rejects(() => sql(`create table half_done(id int); select 1/0;`), /division by zero/)
    const r = await sql(`select to_regclass('public.half_done') as t`)
    expect(r.rows[0].t === null, 'half_done table was left behind')
  })

// ---------- still blocked (security) ----------
await check('SET ROLE is refused', 'Supabase: not possible for project users', () => rejects(() => sql(`set role postgres`)))
await check('CREATE ROLE is refused', 'Supabase: not possible for project users', () => rejects(() => sql(`create role evil`)))
await check('Role membership GRANT is refused', 'Supabase: not possible for project users', () => rejects(() => sql(`grant postgres to anon`)))
await check('COMMIT escape out of the editor transaction is refused', 'n/a (pibase safety)', () => rejects(() => sql(`commit; select current_user`)))
await check('Dynamic EXECUTE inside DO is refused', 'n/a (pibase safety)', () => rejects(() => sql(`do $$ begin execute 'reset role'; end $$`)))

// ---------- app through supabase-js ----------
await check('anon key reads table through a policy', 'supabase.from(t).select()',
  async () => { const { data, error } = await anon.from('todos').select('title').order('id'); if (error) throw error; expect(data.length === 2, JSON.stringify(data)); return data.map(x => x.title).join(',') })
await check('REVOKE from anon really removes the right', 'Supabase: revoke delete on t from anon makes anon deletes fail',
  async () => {
    await sql(`create table guestbook (id int primary key, msg text);
create policy "open" on guestbook for all to anon using (true) with check (true);
insert into guestbook values (1,'a'),(2,'b');`)
    const before = await anon.from('guestbook').delete().eq('id', 1).select()
    if (before.error) throw new Error('delete before revoke failed: ' + before.error.message)
    expect(before.data.length === 1, 'anon delete before revoke removed ' + before.data.length)
    await sql(`revoke delete on guestbook from anon`)
    const after = await anon.from('guestbook').delete().eq('id', 2).select()
    expect(after.error, 'anon could still delete after REVOKE')
    const ins = await anon.from('guestbook').insert({ id: 3, msg: 'c' })
    if (ins.error) throw new Error('insert should still work: ' + ins.error.message)
    return after.error.message
  })
await check('Migration with GRANT to anon works', 'supabase db push / migrations',
  async () => {
    await post(P + '/migrations', { name: '0001_events', sql: `create table events (id int primary key, title text);
revoke all on events from anon;
grant select on events to anon;
create policy "read" on events for select to anon using (true);
insert into events values (1, 'Sunday Service');` })
    const { data, error } = await anon.from('events').select('title')
    if (error) throw error
    const ins = await anon.from('events').insert({ id: 2, title: 'x' })
    expect(ins.error, 'anon insert allowed after migration revoke')
    return data[0].title
  })

// ---------- users ----------
let dashUser
await check('Dashboard: add user with metadata and app role', 'Supabase dashboard "Add user" + raw_app_meta_data role',
  async () => {
    dashUser = await post(P + '/users', { email: 'pastor@oasis.test', password: 'Sermon-2026!', email_confirm: true, user_metadata: { full_name: 'Pastor Test' }, app_metadata: { role: 'admin' } })
    expect(dashUser.id, JSON.stringify(dashUser))
  })
await check('That user signs in from the app with role in the token', 'supabase.auth.signInWithPassword',
  async () => {
    const c = createClient(API, keys.anon_key, { auth: { persistSession: false } })
    const { data, error } = await c.auth.signInWithPassword({ email: 'pastor@oasis.test', password: 'Sermon-2026!' })
    if (error) throw error
    expect(data.user.app_metadata.role === 'admin', 'app_metadata=' + JSON.stringify(data.user.app_metadata))
    expect(data.user.user_metadata.full_name === 'Pastor Test', 'user_metadata=' + JSON.stringify(data.user.user_metadata))
    const ins = await c.from('todos').insert({ title: 'mine' }).select().single()
    if (ins.error) throw ins.error
    expect(ins.data.owner === data.user.id, 'owner not auth.uid()')
    await c.auth.signOut()
    return 'role=admin, inserted own row'
  })
await check('Dashboard: reset a user password', 'Supabase dashboard "Send password recovery"/admin update',
  async () => {
    await post(P + '/users/' + dashUser.id, { password: 'NewPass-2026!' }, 'PATCH')
    const c = createClient(API, keys.anon_key, { auth: { persistSession: false } })
    const { error } = await c.auth.signInWithPassword({ email: 'pastor@oasis.test', password: 'NewPass-2026!' })
    if (error) throw error
  })
await check('Dashboard: ban and unban a user', 'Supabase admin ban_duration',
  async () => {
    await post(P + '/users/' + dashUser.id, { banned: true }, 'PATCH')
    const c = createClient(API, keys.anon_key, { auth: { persistSession: false } })
    const a = await c.auth.signInWithPassword({ email: 'pastor@oasis.test', password: 'NewPass-2026!' })
    expect(a.error, 'banned user signed in')
    await post(P + '/users/' + dashUser.id, { banned: false }, 'PATCH')
    const b = await c.auth.signInWithPassword({ email: 'pastor@oasis.test', password: 'NewPass-2026!' })
    if (b.error) throw b.error
  })
await check('Dashboard: change app role of a user', 'Supabase admin updateUserById app_metadata',
  async () => {
    await post(P + '/users/' + dashUser.id, { app_metadata: { role: 'editor' } }, 'PATCH')
    const c = createClient(API, keys.anon_key, { auth: { persistSession: false } })
    const { data, error } = await c.auth.signInWithPassword({ email: 'pastor@oasis.test', password: 'NewPass-2026!' })
    if (error) throw error
    expect(data.user.app_metadata.role === 'editor', JSON.stringify(data.user.app_metadata))
  })
await check('Server code: service key admin.createUser', 'supabase.auth.admin.createUser',
  async () => {
    const { data, error } = await service.auth.admin.createUser({ email: 'volunteer@oasis.test', password: 'Serve-2026!', email_confirm: true, app_metadata: { role: 'editor' } })
    if (error) throw error
    return data.user.id
  })
await check('App sign-up appears in dashboard Users', 'supabase.auth.signUp',
  async () => {
    const { error } = await anon.auth.signUp({ email: 'guest@oasis.test', password: 'Guest-2026!' })
    if (error) throw error
    const users = await dash(P + '/users')
    expect(users.some(u => u.email === 'guest@oasis.test'), 'not listed')
    return users.length + ' users listed'
  })
await check('Dashboard Users page lists username-only users', 'n/a (pibase username sign-in)',
  async () => {
    await sql(`select auth.admin_create_user('deacon_joe', null, 'Deacon-2026!', '{"role":"editor"}')`).catch(async e => {
      // Owner SQL may not call it; fall back to the dashboard form.
      await post(P + '/users', { username: 'deacon_joe', password: 'Deacon-2026!' })
    })
    const users = await dash(P + '/users')
    expect(users.some(u => u.username === 'deacon_joe'), 'username user missing: ' + JSON.stringify(users.map(u => u.email || u.username)))
  })
await post(P, { confirm: project.id }, 'DELETE')
const pass = results.filter(r => r.ok).length
for (const r of results) console.log(`${r.ok ? 'PASS' : 'FAIL'}  ${r.name}\n      Supabase: ${r.supabase}${r.note ? '\n      ' + r.note : ''}`)
console.log(`\n${pass}/${results.length} passed`)
process.exitCode = pass === results.length ? 0 : 1
