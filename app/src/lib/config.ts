export const config = {
  domain: process.env.DOMAIN ?? "localhost",
  pbImage: process.env.PB_IMAGE ?? "datbase/pocketbase:latest",
  network: process.env.DOCKER_NETWORK ?? "datbase",
  pbMemoryMb: Number(process.env.PB_MEMORY_MB ?? 128),
  pbCpus: Number(process.env.PB_CPUS ?? 0.5),
  usageIntervalMin: Number(process.env.USAGE_INTERVAL_MIN ?? 10),
};

/** How the control plane reaches a project's PocketBase over the private Docker network. */
export const pbInternalUrl = (slug: string) =>
  (process.env.PB_INTERNAL_URL ?? "http://dbp-{slug}:8090").replace("{slug}", slug);

export const projectHost = (slug: string) => `${slug}.${config.domain}`;
/** "https" (default, automatic certificates) or "http" for a home-network test box (e.g. a Raspberry Pi). */
export const publicScheme = () => (process.env.PUBLIC_SCHEME === "http" ? "http" : "https");
export const projectUrl = (slug: string) => `${publicScheme()}://${projectHost(slug)}`;
