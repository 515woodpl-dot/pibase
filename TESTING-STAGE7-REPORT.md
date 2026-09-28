# DATBase v7 — admin operations verification

**Result: PASS on macOS/Docker Desktop.** The complete test set passed: **11 suites, 138 checks**.

## Automated acceptance run

Run from `app` with the installed PostgreSQL 16 tools and SMTP test server available:

```sh
PATH='/tmp/datbase-v5-moto-venv/bin':$PATH \
PG_BIN=/opt/homebrew/opt/postgresql@16/bin \
TEST_WORK=/tmp/datbase-v7-e2e \
TEST_VERBOSE=1 \
CHROMIUM_PATH='/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' \
npm run test:e2e
```

| Suite | Result | Checks |
| --- | ---: | ---: |
| tables | PASS | 18 |
| review-findings | PASS | 13 |
| ai-guide | PASS | 22 |
| usage-limits | PASS | 12 |
| backups | PASS | 14 |
| backup-fixes | PASS | 6 |
| backup-scheduler | PASS | 4 |
| ai-fixes | PASS | 20 |
| email-monitor | PASS | 20 |
| admin-ops | PASS | 8 |
| pb-stress | PASS | 1 |
| **Total** | **PASS** | **138** |

The `admin-ops` suite was run through `WITH_MAIL=1 run-backups.sh`, as wired into `run-all.sh`. Its eight checks verify the following:

- Exactly one owner email at each 80% and 100% request-limit threshold, with the correct numbers, effect, and usage link.
- Outdated-image detection, **Update all projects**, recreation of every project, reapplication of limits, and preservation of paused state.
- Project suspension reason, stopped container, no owner resume control, and successful lift.
- Account-session revocation, blocked sign-in message, stopping all projects, and restoration of the prior active/paused states after lift.
- Downloaded export ZIP contents: table records (including quoted data), byte-identical uploaded file, no engine/internal tables or password schema, and a 404 for a different account.
- Account deletion confirmation guard, then deletion of projects, containers, volumes, and backups; goodbye email; and failed subsequent sign-in.

The stress suite additionally completed **382,982 requests in 45 seconds from 60 concurrent clients with zero errors**.

## Real Docker/Desktop validation

I then deployed v7 into the existing local Docker stack with:

```sh
docker-compose --profile mailpit up -d --build
```

(`docker compose` is not installed on this Mac; its compatible `docker-compose` command was used.) The rebuild completed in 66.42 seconds.

Two pre-existing v6 project containers began on image `f03609e1…`. The admin Server status page reported **“2 of 2 projects run an older version.”** Selecting **Update all projects** completed with **“Last update: 2 of 2 done”** and **“All 2 projects run the current version.”** Both containers now run image `1aa15a5…`.

The named data volumes retained their exact identities across recreation. The active project was back online (`/api/health` returned HTTP 200); the project deliberately paused before the upgrade remained stopped afterward.

Live dashboard checks also passed:

- Suspending a project stopped it and displayed the supplied reason to its owner, with no owner-side resume control. Lifting it restarted it.
- Suspending the account signed the owner out, rejected a new login with **“This account is suspended. Contact support.”**, and stopped both projects.
- Lifting the account restored the previously active project while leaving the previously paused project stopped.

The machine-readable record is [live-suspension.json](/Users/george/Documents/Playground/datbase-v7-review/datbase/review-evidence/live-suspension.json). The reusable live UI check is [live-suspension.mjs](/Users/george/Documents/Playground/datbase-v7-review/datbase/review-evidence/live-suspension.mjs).

## Remaining environment-specific work

This Mac/Docker Desktop pass does **not** replace the still-open production-environment checks:

- Raspberry Pi performance, Docker storage disk path, and real Caddy behavior on the Pi/router.
- A real SMTP provider, TLS settings, deliverability/spam placement, and domain DNS records.
- Loading the Home Assistant package into an actual Home Assistant installation.
- The optional large-export benchmark (for example, a 50 MB upload); the verified export test proves exact bytes for an uploaded file but was not a 50 MB throughput test.
- Measuring the brief Caddy interruption during a real project-container rebuild.

No customer project data or accounts were deleted during the live validation. The temporary suspension was lifted before completion.
