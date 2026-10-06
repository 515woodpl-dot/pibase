# M3 review

**Commit reviewed:** `b7379e7e05f46aba33e182338a0252c29a0d88f2` on branch `pibase/m0`
**Environment:** Linux amd64, Go 1.24.7, PostgreSQL 16.13 native cluster (no Docker), Node 22.
**Verdict:** **BLOCK** (re-review required)

Path-traversal defence, cross-project file isolation, and signed-URL integrity are all solid and I could not break them. But downloads are served as active content in the API origin (stored XSS), every object is hard-capped at 1 MiB so the milestone cannot meet its own streaming requirement, and deleting a project leaves its files on disk. These are must-fix.

## What I ran (live, real projects A/B + authenticated user1)

- `go test ./internal/gateway` → `TestM3StorageRLSAndSignedURLs` **PASS**. VERIFIED.
- service created `pub` (public) and `priv` (private) buckets (201). Authenticated upload/download/list/delete + signed URL exercised live.

**Path traversal (all blocked → only the legit file on disk):**
| Object name (url-encoded) | Result |
|---|---|
| `../../../etc/passwd` | 400 invalid object path |
| `../evil` | 400 |
| `%2e%2e%2fx` | 400 |
| `a/../../b` | 400 |
| `/etc/passwd` (absolute) | 400 |

`find` under the storage root showed only `…/26ed9c49/pub/evil.html` and the legit `priv/mine.txt`. `internal/gateway/storage.go` `storageObjectName` rejects NUL/backslash/leading-slash and requires `filepath.Clean(name)==name`. **Traversal: not exploitable.** VERIFIED.

**Access control / isolation:**
| Attempt | Result |
|---|---|
| anon upload | 401 (writes need auth) |
| B's service key reads A's `priv/mine.txt` | 404 (project bound to key; B has no such object) |
| signed URL for A object used with B's key | 401 (B's secret rejects the token) |
| signed URL for object X reused for object Y | 401 (token bound to bucket+path) |
| signed URL with one byte flipped | 401 (HMAC) |

**Signed URLs** are HS256 over the project secret, carry `bucket`/`path`/`exp`, and are object-bound, tamper-evident, cross-project-safe, and expiring. VERIFIED (live + `keys.VerifyJWT`).

## Findings

**M3-01 (High — stored XSS / no safe download headers).** Downloads are served with the **stored `Content-Type`** and **no** `X-Content-Type-Options: nosniff` and **no** `Content-Disposition`. Live: I uploaded `<script>alert(document.domain)</script>` as `text/html`; `GET …/object/pub/evil.html` returned:
```
HTTP/1.1 200 OK
Content-Type: text/html
(no X-Content-Type-Options, no Content-Disposition)
<script>alert(document.domain)</script>
```
Because objects are served from the **API origin** (same origin as `/rest`, `/auth`), a stored HTML/SVG file executes as active content there. The playbook M3 explicitly requires `nosniff` + correct `Content-Disposition` and that "HTML/SVG uploads can't execute script in the API origin." Fix: send `X-Content-Type-Options: nosniff` and `Content-Disposition: attachment` on downloads (and/or serve objects from a separate origin, and/or restrict served content types). `internal/gateway/storage.go` `serveStorageObject`.

**M3-02 (High — 1 MiB object cap; streaming gate unmet).** `Handler()` wraps the whole mux in `http.MaxBytesHandler(mux, 1<<20)` and `maxStorageObjectBytes = 1<<20`, so **every** object is ≤1 MiB. The playbook M3 requires a **1 GB** streaming upload/download that doesn't load the file into RAM — impossible here. Live: a >1 MiB upload → **500** (the outer `MaxBytesHandler` error surfaces as an unhandled 500 rather than 413). The report declares large-object streaming "deferred," but a 1 MiB ceiling on all objects fails a core M3 acceptance item ("buckets, bytes on disk") and the streaming/RAM check. Fix: stream request bodies to disk with a real per-object/quotas limit instead of a 1 MiB whole-request cap; return 413 cleanly. `server.go:200`, `storage.go:23,306-314`.

**M3-03 (Medium — project delete leaves files).** `control.DeleteProject` drops the database and roles but does **not** remove `/data/files/<project>`. The playbook M3 requires "deleting a project removes its files and database." VERIFIED (READ: `internal/control/project.go` `DeleteProject` has no filesystem cleanup; contrast `storageDeleteBucket`, which does `os.RemoveAll`). Fix: remove the project's storage directory on delete.

**M3-04 (Medium — "public"/signed URLs aren't self-contained).** Public-bucket downloads and signed-URL GETs still require the anon `apikey` header (`storagePrincipal`/`resolveAuthProject` read it). Live: public download without apikey → 401; signed URL without apikey → 401; with apikey → 200. In Supabase these are shareable links usable without an API key. The `supabase-js` acceptance passes only because the JS client auto-sends the apikey header. Fix: resolve the project from the signed token (and from a public route) without requiring the apikey header.

**M3-05 (Low — over-permissive bucket RLS).** `storage_bucket_read` policy is `USING (public OR owner_id = auth.uid() OR auth.uid() IS NOT NULL)`; the final clause lets **any** authenticated user list **all** buckets' metadata. Also `storage.buckets.owner_id` is never set on creation, so the owner-based clause is dead. `internal/control/project.go:156`. Tighten to `public OR owner_id = auth.uid()` (and set `owner_id`), or intend buckets to be project-global and drop the dead clause.

**M3-06 (Low — robustness).** Storage endpoints are not rate-limited or usage-counted (REST/RPC only). `os.Remove` failures on object delete are ignored, leaving orphan bytes (still counted by the quota walk). Oversized upload returns 500 not 413 (see M3-02).

**Verified-good (for the record):** quota is computed by walking the project dir and is checked **before** the commit/rename (`storageUploadObject`), uploads use an atomic tmp-write + `rename` with cleanup on failure, bucket admin is service-key-only, RLS on `storage.objects` isolates users (owner policy; anon limited to public), and deleting a *bucket* removes its directory.

## Claims in the engineer's report I could not confirm
- "Streaming … doesn't load the file into RAM" — not applicable/untestable given the 1 MiB cap (M3-02); the upload handler in fact `io.ReadAll`s the (capped) body into memory. UNVERIFIED as a streaming claim.
- `bin/pibase-linux-arm64` SHA-256 — artifact not committed.

## Scope creep
None beyond X-01.

## Fix list for Codex (must-fix are blocking)
1. **(blocking)** Add `X-Content-Type-Options: nosniff` + `Content-Disposition: attachment` (or a separate serving origin) to downloads. (M3-01)
2. **(blocking)** Support real object sizes via streamed uploads/downloads to disk; return 413 (not 500) over the limit; then meet the 1 GB streaming check. (M3-02)
3. **(blocking)** Remove `/data/files/<project>` when a project is deleted. (M3-03)
4. Make public/signed downloads work without the apikey header. (M3-04)
5. Tighten `storage_bucket_read`; set `owner_id`; rate-limit/usage-count storage; handle delete failures. (M3-05/06)

## Questions for the planner
- Is large-object/streaming storage in the M3 contract or genuinely deferred? The playbook's 1 GB test says in-scope; the report says deferred — this decides whether M3-02 is blocking or a documented limitation.
