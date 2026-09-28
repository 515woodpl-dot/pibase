#!/usr/bin/env bash
# Run every end-to-end suite against a production build. Prints a summary; exits non-zero if any fail.
set -uo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"; WORK="${TEST_WORK:-/tmp/datbase-e2e}"; mkdir -p "$WORK"
declare -a results; fail=0
run() { # name, command...
  local name="$1"; shift
  echo "━━ $name"
  if "$@" > "$WORK/$name.out" 2>&1; then r="PASS"; else r="FAIL"; fail=1; fi
  grep -E "^✔|FAIL|Error" "$WORK/$name.out" | sed 's/^/   /'
  results+=("$r  $name ($(grep -c '^✔' "$WORK/$name.out") checks)")
}
run tables            "$HERE/harness.sh" tables.mjs
run review-findings   "$HERE/harness.sh" review-findings.mjs
run ai-guide          "$HERE/harness.sh" ai-guide.mjs
run usage-limits      "$HERE/harness.sh" usage-limits.mjs 'PLAN_LIMITS_OVERRIDE={"requestsPerMonth":20,"users":2,"storageBytes":300000}'
run backups           env BACKUP_FIRST_DELAY_SEC=45 BACKUP_CHECK_MIN=1 "$HERE/run-backups.sh" backups.mjs
run backup-fixes      "$HERE/run-backups.sh" backup-fixes.mjs
run backup-scheduler  env BACKUP_FIRST_DELAY_SEC=20 BACKUP_CHECK_MIN=1 "$HERE/run-backups.sh" backup-scheduler.mjs
run ai-fixes          "$HERE/run-backups.sh" ai-fixes.mjs
run email-monitor     "$HERE/run-email.sh" email-monitor.mjs
run admin-ops         env WITH_MAIL=1 'PLAN_LIMITS_OVERRIDE={"requestsPerMonth":20}' "$HERE/run-backups.sh" admin-ops.mjs
run pb-stress         "$HERE/harness.sh" pb-stress.mjs
echo; echo "━━ Summary"; printf '%s\n' "${results[@]}"
exit $fail
