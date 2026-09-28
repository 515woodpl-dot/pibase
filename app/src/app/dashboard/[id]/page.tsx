import { aiPrompt } from "@/lib/ai-guide";
import { projectUrl } from "@/lib/config";
import { pageProject } from "@/lib/projects";
import { listServerKeys } from "@/lib/server-keys";
import { CopyButton, CreateServerKeyForm, DeleteForm, RenameForm, RevokeKeyButton } from "../ui";

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex flex-col gap-1 sm:flex-row sm:items-center sm:justify-between">
      <span className="text-sm text-muted">{label}</span>
      <div className="flex min-w-0 items-center gap-2">
        <code className="truncate rounded bg-border/50 px-2 py-1 text-sm">{value}</code>
        <CopyButton value={value} />
      </div>
    </div>
  );
}

function Code({ children, wrap = false }: { children: string; wrap?: boolean }) {
  return (
    <pre
      className={`rounded-md bg-border/40 p-3 text-xs leading-relaxed ${wrap ? "whitespace-pre-wrap" : "overflow-x-auto"}`}
    >
      <code>{children}</code>
    </pre>
  );
}

export default async function ProjectOverview({ params }: PageProps<"/dashboard/[id]">) {
  const { id } = await params;
  const p = await pageProject(id);
  const url = projectUrl(p.slug);
  const keys = await listServerKeys(p.id);

  return (
    <div className="space-y-6">
      {p.status === "creating" && (
        <div className="card text-sm text-muted">Setting up your backend… this usually takes under 30 seconds.</div>
      )}
      {p.status === "error" && (
        <div className="card border-red-500/40 text-sm">
          <b className="text-red-600">Setup failed.</b> {p.statusMessage ?? "Unknown error."}
        </div>
      )}
      {p.status === "deleting" && (
        <div className="card text-sm text-muted">{p.statusMessage ?? "Deleting this project…"}</div>
      )}
      {p.status === "suspended" && (
        <div className="card border-red-500/40 text-sm" data-testid="suspended">
          <b className="text-red-600">Suspended by DATBase.</b> {p.statusMessage ?? ""} Its API is offline. Contact support if you think this is a mistake.
        </div>
      )}
      {p.status === "paused" && (
        <div className="card text-sm text-muted">This project is paused. Its API is offline until you resume it.</div>
      )}

      {p.adminEmail && (
        <section className="card space-y-4">
          <div>
            <h2 className="font-medium">Build with AI</h2>
            <p className="text-sm text-muted">
              Using Claude, Codex, Cursor or another AI to build your website or app? Give it this project&apos;s guide.
              It describes every table, field and access rule, plus a ready-made client, so the AI connects correctly
              the first time.
            </p>
          </div>
          <ol className="list-decimal space-y-2 pl-5 text-sm">
            <li>
              Download <b>DATBASE.md</b> and put it in your app&apos;s project folder.
            </li>
            <li>Paste this prompt into your AI tool:</li>
          </ol>
          <Code wrap>{aiPrompt()}</Code>
          <div className="flex flex-wrap gap-2">
            {p.status === "active" ? (
              <a href={`/dashboard/${p.id}/datbase-md`} className="btn" download>
                Download DATBASE.md
              </a>
            ) : (
              <span className="text-sm text-muted">Available while the project is active.</span>
            )}
            <CopyButton value={aiPrompt()} />
            {p.status === "active" && (
              <a href={`/dashboard/${p.id}/datbase-md?view`} target="_blank" rel="noreferrer" className="btn-ghost">
                Preview
              </a>
            )}
          </div>
          <p className="text-xs text-muted">
            After you change tables, fields or access, download it again so your AI stays in sync.
          </p>
        </section>
      )}

      {p.adminEmail && (
        <section className="card space-y-3">
          <h2 className="font-medium">Connect your app</h2>
          <Row label="API URL" value={url} />
          <p className="text-sm text-muted">
            Read records from a table (e.g. <code>products</code>). DATBASE.md has the full reference.
          </p>
          <Code>{`const res = await fetch("${url}/api/collections/products/records");
const { items } = await res.json();`}</Code>
          <p className="text-sm text-muted">
            Who can read or change each table is set under <b>Tables → Settings → Access</b>.
          </p>
        </section>
      )}

      {p.adminEmail && (
        <section className="card space-y-4">
          <div>
            <h2 className="font-medium">Server keys</h2>
            <p className="text-sm text-muted">
              For your own servers (backend, scripts, cron jobs): a key can read and change everything, ignoring
              access rules, so <b>never put it in a website or mobile app</b>. Requests made with a key count toward
              your plan. Revoke a key at once if it leaks.
            </p>
          </div>
          {keys.length > 0 && (
            <ul className="divide-y divide-border rounded-md border border-border">
              {keys.map((k) => {
                const expired = k.expiresAt.getTime() < Date.now();
                return (
                  <li key={k.id} data-key-id={k.id} className="flex flex-wrap items-center justify-between gap-2 px-3 py-2 text-sm">
                    <div className="min-w-0">
                      <div className="font-medium">{k.name}</div>
                      <div className="text-xs text-muted">
                        …{k.hint} · created {k.createdAt.toISOString().slice(0, 10)} ·{" "}
                        {expired ? <span className="text-red-600">expired</span> : <>expires {k.expiresAt.toISOString().slice(0, 10)}</>}
                      </div>
                    </div>
                    <RevokeKeyButton projectId={p.id} keyId={k.id} name={k.name} />
                  </li>
                );
              })}
            </ul>
          )}
          {p.status === "active" ? (
            <CreateServerKeyForm id={p.id} />
          ) : (
            <p className="text-sm text-muted">Keys can be created while the project is active.</p>
          )}
        </section>
      )}

      <section className="card space-y-3">
        <h2 className="font-medium">Settings</h2>
        <RenameForm id={p.id} name={p.name} />
      </section>

      {p.status !== "deleting" && (
        <section className="card space-y-3 border-red-500/40">
          <h2 className="font-medium text-red-600">Danger zone</h2>
          <DeleteForm id={p.id} name={p.name} />
        </section>
      )}
    </div>
  );
}
