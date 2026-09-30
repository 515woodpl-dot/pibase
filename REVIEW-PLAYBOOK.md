# pibase Review Playbook

This document defines the audit methodology for pibase milestone reviews. Every
auditor must follow it exactly. The contract is `PLAN.md`; this playbook says
*how* to verify it.

---

## Ground rules

1. **Run the code.** Every security claim must be VERIFIED (you ran it) or READ
   (you read the source). UNVERIFIED claims must be labelled as such.
2. **Attack live.** Spin up real projects, hit the running gateway over HTTP.
   Do not mock the database or stub the network.
3. **RAM numbers need raw evidence.** A memory figure is only "measured" if the
   command and its raw output appear in the review or in the repo under
   `reports/raw/`. State the host architecture next to every number.
4. **Tag every claim.** One of: `VERIFIED` · `READ` · `UNVERIFIED`.
5. **Do not modify product source.** Build the code as committed. Note any
   build-environment workarounds.
6. **Gate discipline.** Reviews are written per milestone in milestone order.
   Stop and deliver the review before the next milestone begins. Do not review
   code that belongs to a later milestone.

---

## Severity levels

| Level | Definition |
|---|---|
| **Critical** | Data leak across projects, auth bypass, cross-project DB/file read, SQL injection exploitable |
| **High** | Exploitable DoS on the 4 GB Pi target, stored XSS in the API origin, user enumeration via timing/response, missing required contract item, false claim in gate report |
| **Medium** | Security-relevant gap that requires unusual conditions, missing hardening, Supabase-compat gap |
| **Low** | Defence-in-depth gap, code hygiene, latent risk |

A single Critical finding → **BLOCK**.
Two or more High findings in one milestone → **BLOCK**.
One High finding → **APPROVE WITH FIXES** (fix before M4 or before shipping).
Medium/Low only → **APPROVE WITH FIXES** or **APPROVE** at reviewer discretion.

---

## Environment setup

```bash
# Required
go 1.22+
PostgreSQL 16 (native cluster or Docker)
Node 22 (for supabase-js acceptance)

# Build
go build ./...
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags="-s -w" -o pibase ./cmd/pibase

# Unit + integration tests (set PIBASE_TEST_ADMIN_DATABASE_URL)
go test ./...
```

Docker-based scripts (`test.sh`, `measure-m0.sh`) are the canonical path; a
native-cluster reproduction is acceptable when Docker is unavailable, but label
every measurement with the actual host and architecture.

---

## M0 — Foundation & measurement

### Contract items to verify
- [ ] `pibase bootstrap` and `pibase project-create` succeed with no `sudo`.
- [ ] Per-project DB with `REVOKE CONNECT … FROM PUBLIC`; cross-project connect denied.
- [ ] `authenticator` role: `LOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE`.
- [ ] `service_role`: `BYPASSRLS NOLOGIN NOSUPERUSER`.
- [ ] Per-project JWTs signed with per-project secrets; secrets AES-256-GCM at rest.
- [ ] Secrets/keys absent from logs.
- [ ] Cross-compile arm64 (`file` output confirms static ARM64 ELF).
- [ ] 20 active projects fit in 4 GB headroom (PSS metric, not summed RSS).

### Measurement check
- Raw CSV and environment file committed (not gitignored).
- Metric is PSS or `docker stats` — not summed per-backend RSS.
- Host, architecture, and "estimated vs measured" labels present.

### Attack checklist
- Connect as a freshly created LOGIN role with no CONNECT grant → must fail.
- As `authenticator` connected to project B, attempt to `SET ROLE` to a project-A role → must fail.

---

## M1 — Data API & SQL

### Contract items to verify
- [ ] REST: select, insert, update, upsert, delete, filter operators (`eq gt gte lt lte neq like ilike in is`, `or`, `not`), order, limit, offset, embedding.
- [ ] RPC: `POST /rest/v1/rpc/<fn>`.
- [ ] Row cap (1 000), body cap (1 MiB), statement timeout (5 s).
- [ ] `@supabase/supabase-js` acceptance test passes against real Postgres (no mocks).
- [ ] Migrations: idempotent, checksum-protected, advisory-locked, atomic rollback.

### SQL injection checklist (all must return 400 or empty — table must survive)
```
select=nonexistent_col
order=title;drop table todos
order=(select 1)
evilcol=eq.1
title=eq.x'||(SELECT secret FROM ...)||'
title=in.(x,y');DROP TABLE todos;--)
POST ?on_conflict=title)--
relation todos;drop table todos
GET /rest/v1/auth.users   (anon)
```
After all attempts: confirm the table still returns its original rows.

### JWT / auth context checklist
```
alg:none token              → 401
HS256 with wrong secret     → 401
Expired token               → 401
Project-A JWT to project B  → 401
Host-header tricks          → project bound to key, not Host
Service key in error body   → must be absent
```

### Owner SQL (M1-04 gate for M4)
- Verify `adminsql.Run` connects as superuser then `SET LOCAL ROLE`.
- Note: `RESET ROLE` in submitted SQL re-escalates. Mark as latent Critical if
  the M4 SQL editor will expose this path to end users.

---

## M2 — Auth

### Contract items to verify
- [ ] Signup, `token?grant_type=password`, token refresh, verify (confirm + recovery), `GET/PATCH /auth/v1/user`, admin CRUD, **sign-out** (`/auth/v1/logout`).
- [ ] Passwords: argon2id, minimum length ≥ 6, maximum length ≤ 1024, constant-time verify.
- [ ] Refresh rotation: old token revoked; replay rejected.
- [ ] Confirm/reset tokens: SHA-256 hashed, single-use, expiring.
- [ ] `auth.uid()`: `SECURITY DEFINER`, `SET search_path=pg_catalog,auth`, `REVOKE … FROM PUBLIC`.
- [ ] Cross-project sign-in denied (project-A JWT rejected by project B).

### Auth attack checklist

**Rate limiting**
- Send 30 rapid `POST /auth/v1/token` requests → at least one must return 429.
- Send 30 rapid `POST /auth/v1/signup` requests → at least one must return 429.

**Timing oracle**
- Measure median login latency: known user (wrong password) vs unknown email.
- Ratio must be < 2×. Record raw timings.

**Argon2 concurrency**
- Confirm a semaphore (or equivalent) caps concurrent `HashPassword` calls.
- `grep` for the limiter; state file and line.
- Estimate peak RAM at max concurrency: `64 MiB × cap`. Must fit within budget.

**User enumeration**
- Signup with existing email → must not return 409 (or must be documented trade-off).
- Recovery with unknown email → must return 200.

**Token integrity**
- `alg:none` access token → 401.
- Forged/expired access token → 401.
- Wrong `aud` → 401 (if playbook requires aud check).

---

## M3 — Storage

### Contract items to verify
- [ ] Upload, download, list, delete objects; public and private buckets.
- [ ] Signed URLs: HMAC'd, object-bound, expiring, cross-project-safe.
- [ ] 1 GB streaming upload/download without loading the file into RAM.
- [ ] Quota enforced before commit.
- [ ] Deleting a project removes its files and database.
- [ ] Public-bucket and signed-URL downloads work without the `apikey` header.

### Path traversal checklist (all must return 400)
```
../../../etc/passwd
../evil
%2e%2e%2fx
a/../../b
/etc/passwd   (absolute)
```
After: `find` under storage root confirms only the legitimately uploaded file exists.

### Access control checklist
```
Anon upload to private bucket          → 401
Project-B service key reads A's object → 404
Signed URL for A used with B's key     → 401
Signed URL for object X reused for Y   → 401
Signed URL with one byte flipped       → 401
```

### Safe download headers checklist
- Upload `<script>alert(document.domain)</script>` as `text/html`.
- `GET` the object and inspect response headers:
  - `X-Content-Type-Options: nosniff` must be present.
  - `Content-Disposition: attachment` must be present (or object served from separate origin).
- If either is absent → **High** finding (stored XSS).

### Streaming / size checklist
- Attempt upload > 1 MiB → must succeed (not 500 or 413 at 1 MiB).
- Download a multi-MiB object while monitoring gateway RSS → RSS must not grow proportionally.

---

## Review file format

```
# Mx review

**Commit reviewed:** <sha> on branch <branch>
**Environment:** <OS, Go version, PG version, Docker/native>
**Verdict:** APPROVE | APPROVE WITH FIXES | BLOCK

<one-paragraph summary>

## What I ran
<table of commands, results, and VERIFIED/READ/UNVERIFIED tags>

## Contract checks
<checklist items, each tagged>

## Findings
<Mx-NN (Severity — title). Body with evidence and fix.>

## Claims in the engineer's report I could not confirm
<list, each UNVERIFIED with reason>

## Scope creep
<anything committed that belongs to a later milestone>

## Fix list for Codex (priority order)
1. **(blocking)** …
2. …

## Questions for the planner
- …
```

---

## SUMMARY.md format

```
# Review summary — pibase M0–M3

**Commit reviewed:** …
**Reviewer environment:** …

## Verdicts
| Milestone | Verdict | Open Critical | Open High |
|---|---|---|---|

## Open High findings
- **Mx-NN** — …

## Notable Medium findings
- …

## Cross-cutting / process
- …

## What was independently verified as sound
- …

## Safe to proceed to M4?
<verdict with rationale>

### Top of the fix queue
1. …
```
