const MB = 1024 * 1024;

export type Plan = {
  name: string;
  projects: number;
  dbBytes: number;
  storageBytes: number;
  users: number;
  requestsPerMonth: number;
};

export const PLANS = {
  free: {
    name: "Free",
    projects: 2,
    dbBytes: 500 * MB,
    storageBytes: 1024 * MB,
    users: 5000,
    requestsPerMonth: 100_000,
  },
} satisfies Record<string, Plan>;

/**
 * The plan for an account. Everyone is on Free until billing (a later stage) adds paid plans.
 * PLAN_LIMITS_OVERRIDE (JSON, e.g. {"requestsPerMonth":500000}) lets you adjust limits without a code change.
 */
export function planFor(_userId: string): Plan {
  let override: Partial<Plan> = {};
  try {
    override = JSON.parse(process.env.PLAN_LIMITS_OVERRIDE || "{}");
  } catch {}
  return { ...PLANS.free, ...override };
}
