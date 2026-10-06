# Tier 1 PR re-review

Reviewed PR #1 at a55f8fcc26d892f1c65a99ecf01d44fb651970c1 against base 89936a42c238337db5a05c491700e0327b4ddcc5.

Initial verdict at a55f8fc: BLOCK. Both findings below were reproduced before correction. See the resolution section for the follow-up fix and verification.

## 1. Critical: bucket traversal deletes another project's storage

`internal/gateway/storage.go:88-98` accepts `.` and `..` as bucket names. The bucket deletion handler passes that value into `os.RemoveAll(filepath.Join(storageRoot, projectID, name))` at line 308. A project's service key can therefore delete the shared storage root, bypassing database isolation and the project storage lock.

Reproduction against disposable PostgreSQL 16 and a temporary storage root:
- Create separate attacker and victim projects.
- Upload a private victim object using the victim service key.
- Create a bucket named `..` using only the attacker service key: HTTP 201.
- Delete `/storage/v1/bucket/%2e%2e` using the attacker service key: HTTP 200.
- The victim's file disappears.

This defect predates the PR and was missed by the earlier audit. Reject dot segments after decoding, verify resolved paths stay inside their intended project/bucket roots before file operations, and account for existing invalid bucket rows.

## 2. High: an unauthorized upload holds every storage mutation in its project

`internal/gateway/storage.go:331-336` acquires the project-wide lock before consuming the request body or evaluating object write permissions. The only early database check runs as the service role and checks bucket existence. A user who cannot update another user's public object can therefore keep a body open and prevent authorized uploads, object deletes, and bucket deletes in that project. The deployed HTTP read timeout permits a request to last five minutes.

Reproduction:
- Upload an object using the project service key.
- Start an upsert using another authenticated user's token and a body that has not reached EOF.
- An authorized owner delete with a 100 ms request deadline cannot acquire the lock and returns HTTP 500 after cancellation.
- Finish the attacker upload: it returns HTTP 403, confirming it never had write permission.

This blocking behavior is introduced by the PR's storage lock. Keep locks around short reservation/finalization operations, bound concurrent staging, and perform permission checks before expensive body processing while retaining a final RLS check.

## Test evidence

Local test file: `internal/gateway/review_adversarial_test.go`.

Command inside the Go/PostgreSQL test container, with PIBASE_TEST_ADMIN_DATABASE_URL pointing at a fresh PostgreSQL 16 container:

```
go test -v -count=1 -timeout=60s -run TestReview ./internal/gateway
```

Result:

```
FAIL TestReviewBucketTraversal
cross-project deletion: dot-dot bucket create=201 delete=200; victim file disappeared

FAIL TestReviewUnauthorizedUploadBlocksOwnerDelete
unauthorized upload blocked owner delete: delete=500; upload eventually=403
```

The previously passing integration, repetition, race, and acceptance tests do not cover these cases. No claim is made here about actual Pi execution or process-crash recovery.

## Resolution (2026-10-06)

Both reproduced blockers are fixed in the follow-up commit:

- Bucket validation rejects `.` and `..` after decoding. Encoded and double-encoded variants are rejected. Filesystem object paths also validate database catalog values before use, preventing legacy unsafe names from being served. Existing invalid bucket rows cannot trigger filesystem deletion.
- Upload authorization is probed in a rolled-back RLS transaction before reading the body, then checked again during finalization. A zero-row update is forbidden at both checks.
- Network reads do not hold the project mutation lock. Each local write checks physical usage under that lock, including staging files; finalization retains the lock through metadata commit and file rollback. Concurrent staging is capped at eight uploads per project, with excess requests returning HTTP 429.
- Regression tests cover an existing malicious bucket row, cross-project file preservation, early rejection without consuming the body, authorized slow uploads alongside owner deletion, and path validation including literal percent signs.

Verification:
- Targeted PostgreSQL regressions and the existing storage integration suite passed.
- Ten repetitions with Go's race detector passed for the adversarial and storage integration tests (including concurrent quota enforcement).
- The full Docker gate passed, including formatting, vet, unit tests, PostgreSQL integration tests, production image build, and Linux ARM64 compilation.
- Supabase JavaScript M6a and M5b realtime acceptance suites passed against the rebuilt production image.

These checks use disposable PostgreSQL and temporary storage directories. Physical Pi execution and process-crash recovery remain unverified. Quota accounting scans project files during local writes; large object inventories may require further performance work. The lock and upload cap assume the documented single gateway process.
