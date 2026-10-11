import assert from 'node:assert/strict'
import { createRequire } from 'node:module'

const require = createRequire(import.meta.url)
const PibaseAuth = require('../internal/gateway/client/pibase-auth.js')
const realFetch = globalThis.fetch
let cookie = ''
globalThis.window = { get localStorage() { throw new Error('cookie client accessed localStorage') } }
globalThis.BroadcastChannel = undefined
globalThis.fetch = async (url, options = {}) => {
  const headers = new Headers(options.headers || {})
  if (cookie && options.credentials === 'include') headers.set('Cookie', cookie)
  const response = await realFetch(url, { ...options, headers })
  const setCookie = response.headers.get('set-cookie')
  if (setCookie) cookie = /Max-Age=0|Expires=Thu, 01 Jan 1970/.test(setCookie) ? '' : setCookie.split(';')[0]
  return response
}

const settings = { url: process.env.PIBASE_TEST_URL, anonKey: process.env.PIBASE_TEST_ANON_KEY, autoRefresh: false }
let auth = PibaseAuth.createCookieClient(settings)
const session = await auth.signIn(process.env.PIBASE_TEST_EMAIL, process.env.PIBASE_TEST_PASSWORD)
assert.ok(session.access_token)
assert.equal(session.refresh_token, undefined)
assert.ok(cookie.includes('pibase_refresh_'))
auth.destroy()

auth = PibaseAuth.createCookieClient(settings)
assert.equal(auth.session(), null)
assert.ok(await auth.accessToken())
const userResponse = await auth.fetch('/auth/v1/user')
assert.equal(userResponse.status, 200)
await auth.signOut()
assert.equal(cookie, '')
auth.destroy()
console.log('cookie client session restored and signed out without localStorage')
