/*
 * Backup retention: a pure function, unit-tested in tests/unit/retention.test.mjs.
 *
 * What we keep (and promise in the UI):
 *  - Daily:  the newest backup of each of the 7 most recent UTC days that have a backup.
 *  - Weekly: the newest backup of each of the 4 most recent weeks before those days.
 *  - Extras: manual and "before restore" backups from the last 7 days are kept on top
 *            (newest 10), so "Back up now" or a restore never pushes out daily restore points.
 *  - Always: the newest backup, and any backup currently in use (e.g. being restored).
 * Everything else expires.
 */

export const KEEP_DAILY = 7;
export const KEEP_WEEKLY = 4;
export const KEEP_EXTRA = 10;
export const EXTRA_DAYS = 7;

const DAY = 86_400_000;
const WEEK = 7 * DAY;

export type RetentionRow = { id: string; kind: "auto" | "manual" | "pre-restore"; createdAt: Date };

const dayOf = (d: Date) => Math.floor(d.getTime() / DAY); // UTC day number

export function pickExpired<T extends RetentionRow>(rows: T[], now = new Date(), protectedIds: Iterable<string> = []): T[] {
  const sorted = [...rows].sort((a, b) => b.createdAt.getTime() - a.createdAt.getTime());
  const keep = new Set<string>(protectedIds);
  if (sorted[0]) keep.add(sorted[0].id);

  // Daily tier: newest backup per UTC day, for the 7 most recent days that have one.
  const days: number[] = [];
  for (const r of sorted) {
    const d = dayOf(r.createdAt);
    if (days.includes(d)) continue;
    if (days.length >= KEEP_DAILY) break;
    days.push(d);
    keep.add(r.id); // sorted newest-first, so the first seen per day is that day's newest
  }

  // Weekly tier: newest backup per 7-day bucket, counting back from the oldest kept day.
  if (days.length) {
    const edge = days[days.length - 1] * DAY; // start of the oldest daily day
    const weeks = new Set<number>();
    for (const r of sorted) {
      const t = r.createdAt.getTime();
      if (t >= edge) continue;
      const w = Math.floor((edge - 1 - t) / WEEK); // 0 = the week just before the daily tier
      if (w >= KEEP_WEEKLY || weeks.has(w)) continue;
      weeks.add(w);
      keep.add(r.id);
    }
  }

  // Extras: recent manual and "before restore" backups (bounded, so a burst can't grow forever).
  sorted
    .filter((r) => r.kind !== "auto" && now.getTime() - r.createdAt.getTime() < EXTRA_DAYS * DAY)
    .slice(0, KEEP_EXTRA)
    .forEach((r) => keep.add(r.id));

  return sorted.filter((r) => !keep.has(r.id));
}
