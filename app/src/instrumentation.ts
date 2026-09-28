// Runs once when the server starts: migrations, then background jobs.
export async function register() {
  if (process.env.NEXT_RUNTIME !== "nodejs" || process.env.SKIP_MIGRATIONS === "1") return;
  const { migrate } = await import("drizzle-orm/node-postgres/migrator");
  const { db } = await import("./db");
  await migrate(db, { migrationsFolder: "./drizzle" });
  console.log("[db] migrations applied");

  const backups = await import("./lib/backups");
  await backups.cleanupInterrupted();
  // Recovery marks each project busy while it restores it; the start-up email job below skips busy ones.
  const recovering =
    process.env.RECOVER_ON_START === "1"
      ? (console.log("[recover] RECOVER_ON_START=1: rebuilding projects from their latest backups"),
        backups.recoverAll().then(() => console.log("[recover] done. Remove RECOVER_ON_START from .env")))
      : Promise.resolve();
  backups.startBackupLoop();

  const { startUsageLoop } = await import("./lib/usage");
  startUsageLoop();

  // Re-apply email settings to every project shortly after start (SMTP settings may have changed).
  const { configureAllProjectsMail } = await import("./lib/project-mail");
  // After a recovery finishes (it re-applies email settings per project itself), never during it.
  setTimeout(() => void recovering.then(() => configureAllProjectsMail()).then((r) => r.total && console.log(`[mail] email settings applied to ${r.ok}/${r.total} projects`)), 15_000).unref();

  const { startMonitorLoop } = await import("./lib/monitor");
  startMonitorLoop();
}
