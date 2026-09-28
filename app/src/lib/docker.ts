import "server-only";
import Docker from "dockerode";
import { config, pbInternalUrl, projectHost, publicScheme } from "./config";
import { controlSecret } from "./crypto";

// Talks to the host's Docker daemon through the mounted /var/run/docker.sock.
const docker = new Docker();

const nameFor = (slug: string) => `dbp-${slug}`;
const ignore = (...codes: number[]) => (e: { statusCode?: number }) => {
  if (!codes.includes(e.statusCode ?? 0)) throw e;
};

/** Start a fresh PocketBase container (+ its own data volume) for a project. */
export async function startInstance(slug: string, projectId: string) {
  const name = nameFor(slug);
  await removeContainer(slug); // clean up leftovers from a failed attempt; keeps the volume
  await docker.createVolume({ Name: name, Labels: { "datbase.project": projectId } });

  const container = await docker.createContainer({
    name,
    Image: config.pbImage,
    Env: [`DATBASE_CONTROL_SECRET=${controlSecret(slug)}`],
    Labels: {
      "datbase.project": projectId,
      caddy: publicScheme() === "http" ? `http://${projectHost(slug)}` : projectHost(slug),
      "caddy.reverse_proxy": "{{upstreams 8090}}",
      // Customers use the DATBase dashboard; keep PocketBase's own admin UI off the public URL.
      "caddy.@pbadmin.path": "/_/*",
      "caddy.respond": "@pbadmin 404",
    },
    HostConfig: {
      NetworkMode: config.network,
      Binds: [`${name}:/pb/pb_data`],
      Memory: config.pbMemoryMb * 1024 * 1024,
      NanoCpus: Math.round(config.pbCpus * 1e9),
      RestartPolicy: { Name: "unless-stopped" },
    },
  });
  await container.start();
}

/** Wait until the instance answers /api/health (reached over the private Docker network). */
export async function waitHealthy(slug: string, timeoutMs = 30_000) {
  const url = `${pbInternalUrl(slug)}/api/health`;
  const until = Date.now() + timeoutMs;
  while (Date.now() < until) {
    try {
      const r = await fetch(url, { signal: AbortSignal.timeout(2000) });
      if (r.ok) return;
    } catch {}
    await new Promise((r) => setTimeout(r, 1000));
  }
  throw new Error("Instance did not become healthy in time");
}

/** Create or reset the instance's superuser via the PocketBase CLI inside the container. */
export async function upsertSuperuser(slug: string, email: string, password: string) {
  const c = docker.getContainer(nameFor(slug));
  const exec = await c.exec({
    Cmd: ["/pb/pocketbase", "superuser", "upsert", email, password, "--dir=/pb/pb_data"],
    AttachStdout: true,
    AttachStderr: true,
  });
  const stream = await exec.start({});
  await new Promise<void>((resolve, reject) => {
    stream.on("end", () => resolve());
    stream.on("error", reject);
    stream.resume();
  });
  const { ExitCode } = await exec.inspect();
  if (ExitCode !== 0) throw new Error(`superuser upsert failed (exit ${ExitCode})`);
}

/** Does the project's container exist on this server? */
export async function instanceExists(slug: string) {
  try {
    await docker.getContainer(nameFor(slug)).inspect();
    return true;
  } catch (e) {
    if ((e as { statusCode?: number }).statusCode === 404) return false;
    throw e;
  }
}

export async function stopInstance(slug: string) {
  await docker.getContainer(nameFor(slug)).stop().catch(ignore(304, 404));
}

export async function resumeInstance(slug: string) {
  await docker.getContainer(nameFor(slug)).start().catch(ignore(304));
}

async function removeContainer(slug: string) {
  await docker.getContainer(nameFor(slug)).remove({ force: true }).catch(ignore(404));
}

/** Permanently remove the container AND its data volume. */
export async function destroyInstance(slug: string) {
  await removeContainer(slug);
  await docker.getVolume(nameFor(slug)).remove().catch(ignore(404));
}

/** Id of the project image new containers get (after `docker compose build`, this changes). */
export async function currentImageId() {
  return (await docker.getImage(config.pbImage).inspect()).Id;
}

/** Id of the image a project's container runs, or null if it has no container. */
export async function instanceImageId(slug: string) {
  try {
    return (await docker.getContainer(nameFor(slug)).inspect()).Image;
  } catch (e) {
    if ((e as { statusCode?: number }).statusCode === 404) return null;
    throw e;
  }
}
