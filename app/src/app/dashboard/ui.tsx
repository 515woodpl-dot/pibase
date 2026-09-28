"use client";

import { useRouter } from "next/navigation";
import { useActionState, useEffect, useState, useTransition } from "react";
import type { ProjectStatus } from "@/db/schema";
import { authClient } from "@/lib/auth-client";
import {
  backupNowAction,
  createServerKeyAction,
  revokeServerKeyAction,
  createProject,
  deleteProject,
  pauseProject,
  refreshUsage,
  renameProject,
  restoreAction,
  resumeProject,
  retryProject,
} from "./actions";

export function SignOutButton() {
  const router = useRouter();
  return (
    <button
      className="btn-ghost"
      onClick={async () => {
        await authClient.signOut();
        router.push("/login");
        router.refresh();
      }}
    >
      Sign out
    </button>
  );
}

const badge: Record<ProjectStatus, string> = {
  creating: "bg-amber-500/15 text-amber-700 dark:text-amber-400",
  active: "bg-brand-green/20 text-success",
  paused: "bg-zinc-500/15 text-zinc-600 dark:text-zinc-400",
  error: "bg-red-500/15 text-red-700 dark:text-red-400",
  deleting: "bg-red-500/15 text-red-700 dark:text-red-400",
  restoring: "bg-amber-500/15 text-amber-700 dark:text-amber-400",
  suspended: "bg-red-500/15 text-red-700 dark:text-red-400",
};

export function StatusBadge({ status }: { status: ProjectStatus }) {
  return (
    <span className={`rounded-full px-2.5 py-0.5 text-xs font-medium capitalize ${badge[status]}`}>
      {status}
    </span>
  );
}

/** Re-renders the page every 2s while a project is still provisioning. */
export function StatusPoller({ status }: { status: ProjectStatus }) {
  const router = useRouter();
  useEffect(() => {
    if (status !== "creating" && status !== "deleting" && status !== "restoring") return;
    const t = setInterval(() => router.refresh(), 2000);
    return () => clearInterval(t);
  }, [status, router]);
  return null;
}

export function CreateProjectForm() {
  const [state, action, pending] = useActionState(createProject, undefined);
  return (
    <form action={action} className="card space-y-3">
      <h2 className="font-medium">New project</h2>
      <div className="flex flex-col gap-2 sm:flex-row">
        <input name="name" placeholder="My Application" required minLength={2} maxLength={50} className="input" />
        <button className="btn shrink-0" disabled={pending}>
          {pending ? "Creating…" : "Create project"}
        </button>
      </div>
      {state?.error && <p className="text-sm text-red-600">{state.error}</p>}
    </form>
  );
}

export function LifecycleButtons({ id, status }: { id: string; status: ProjectStatus }) {
  const [pending, start] = useTransition();
  const run = (fn: (id: string) => Promise<void>) => () => start(() => fn(id));
  return (
    <div className="flex gap-2">
      {status === "active" && (
        <button className="btn-ghost" disabled={pending} onClick={run(pauseProject)}>
          {pending ? "Pausing…" : "Pause"}
        </button>
      )}
      {status === "paused" && (
        <button className="btn-ghost" disabled={pending} onClick={run(resumeProject)}>
          {pending ? "Resuming…" : "Resume"}
        </button>
      )}
      {status === "error" && (
        <button className="btn-ghost" disabled={pending} onClick={run(retryProject)}>
          {pending ? "Retrying…" : "Retry setup"}
        </button>
      )}
    </div>
  );
}

export function RefreshUsageButton({ id }: { id: string }) {
  const [pending, start] = useTransition();
  return (
    <button className="btn-ghost" disabled={pending} onClick={() => start(() => refreshUsage(id))}>
      {pending ? "Measuring…" : "Refresh"}
    </button>
  );
}

export function CopyButton({ value }: { value: string }) {
  const [done, setDone] = useState(false);
  return (
    <button
      type="button"
      className="btn-ghost shrink-0"
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(value);
        } catch {
          // HTTP (non-secure context): clipboard API unavailable; fall back to execCommand
          const el = document.createElement("textarea");
          el.value = value;
          el.style.position = "fixed";
          el.style.opacity = "0";
          document.body.appendChild(el);
          el.select();
          document.execCommand("copy");
          document.body.removeChild(el);
        }
        setDone(true);
        setTimeout(() => setDone(false), 1500);
      }}
    >
      {done ? "Copied" : "Copy"}
    </button>
  );
}

export function RenameForm({ id, name }: { id: string; name: string }) {
  const [state, action, pending] = useActionState(renameProject, undefined);
  return (
    <form action={action} className="space-y-2">
      <input type="hidden" name="id" value={id} />
      <div className="flex flex-col gap-2 sm:flex-row">
        <input name="name" defaultValue={name} required minLength={2} maxLength={50} className="input" />
        <button className="btn-ghost shrink-0" disabled={pending}>
          {pending ? "Saving…" : "Rename"}
        </button>
      </div>
      {state?.error && <p className="text-sm text-red-600">{state.error}</p>}
    </form>
  );
}

export function DeleteForm({ id, name }: { id: string; name: string }) {
  const [state, action, pending] = useActionState(deleteProject, undefined);
  const [typed, setTyped] = useState("");
  return (
    <form action={action} className="space-y-2">
      <input type="hidden" name="id" value={id} />
      <p className="text-sm text-muted">
        This permanently deletes the project and all of its data. Type <b>{name}</b> to confirm.
      </p>
      <div className="flex flex-col gap-2 sm:flex-row">
        <input name="confirm" value={typed} onChange={(e) => setTyped(e.target.value)} className="input" autoComplete="off" />
        <button className="btn-danger shrink-0" disabled={pending || typed !== name}>
          {pending ? "Deleting…" : "Delete project"}
        </button>
      </div>
      {state?.error && <p className="text-sm text-red-600">{state.error}</p>}
    </form>
  );
}

export function BackupNowButton({ id, disabled }: { id: string; disabled?: boolean }) {
  const [pending, start] = useTransition();
  const [msg, setMsg] = useState<string | null>(null);
  return (
    <div className="flex items-center gap-3">
      {msg && <span className="text-sm text-red-600">{msg}</span>}
      <button
        className="btn"
        disabled={pending || disabled}
        onClick={() => start(async () => setMsg(await backupNowAction(id)))}
      >
        {pending ? "Starting…" : "Back up now"}
      </button>
    </div>
  );
}

export function RestoreForm({ id, name, backupId, label }: { id: string; name: string; backupId: string; label: string }) {
  const [state, action, pending] = useActionState(restoreAction, undefined);
  const [open, setOpen] = useState(false);
  const [typed, setTyped] = useState("");
  if (!open)
    return (
      <button className="text-sm text-accent-text hover:underline" onClick={() => setOpen(true)}>
        Restore
      </button>
    );
  return (
    <form action={action} className="mt-3 space-y-2 rounded-md border border-amber-500/40 bg-amber-500/5 p-3 text-left">
      <input type="hidden" name="id" value={id} />
      <input type="hidden" name="backupId" value={backupId} />
      <p className="text-sm">
        Replace <b>all</b> current data, users and files with the backup from <b>{label}</b>. A safety backup of the
        current state is taken first, so you can undo this. Type <b>{name}</b> to confirm.
      </p>
      <div className="flex flex-col gap-2 sm:flex-row">
        <input name="confirm" value={typed} onChange={(e) => setTyped(e.target.value)} className="input" autoComplete="off" />
        <button className="btn-danger shrink-0" disabled={pending || typed !== name}>
          {pending ? "Starting…" : "Restore"}
        </button>
        <button type="button" className="btn-ghost shrink-0" onClick={() => setOpen(false)}>
          Cancel
        </button>
      </div>
      {state?.error && <p className="text-sm text-red-600">{state.error}</p>}
    </form>
  );
}

export function CreateServerKeyForm({ id }: { id: string }) {
  const [state, action, pending] = useActionState(createServerKeyAction, undefined);
  return (
    <div className="space-y-3">
      {state?.token && (
        <div className="space-y-2 rounded-md border border-amber-500/50 bg-amber-500/10 p-3">
          <p className="text-sm">
            <b>Copy the key “{state.name}” now.</b> It won&apos;t be shown again. Store it as the environment
            variable <code>DATBASE_SERVER_KEY</code> on your server, never in browser or mobile code.
          </p>
          <div className="flex items-center gap-2">
            <code className="min-w-0 flex-1 truncate rounded bg-border/50 px-2 py-1 text-xs" data-testid="new-server-key">
              {state.token}
            </code>
            <CopyButton value={state.token} />
          </div>
        </div>
      )}
      <form action={action} className="flex flex-col gap-2 sm:flex-row">
        <input type="hidden" name="id" value={id} />
        <input name="name" placeholder="Key name, e.g. production server" required minLength={2} maxLength={50} className="input" />
        <select name="days" defaultValue="365" className="input sm:w-40">
          <option value="30">Valid 30 days</option>
          <option value="90">Valid 90 days</option>
          <option value="365">Valid 1 year</option>
        </select>
        <button className="btn shrink-0" disabled={pending}>
          {pending ? "Creating…" : "Create key"}
        </button>
      </form>
      {state?.error && <p className="text-sm text-red-600">{state.error}</p>}
    </div>
  );
}

export function RevokeKeyButton({ projectId, keyId, name }: { projectId: string; keyId: string; name: string }) {
  const [pending, start] = useTransition();
  return (
    <button
      className="text-sm text-red-600 hover:underline disabled:opacity-50"
      disabled={pending}
      onClick={() => {
        if (!confirm(`Revoke “${name}”? Servers using it stop working immediately.`)) return;
        start(() => revokeServerKeyAction(projectId, keyId));
      }}
    >
      {pending ? "Revoking…" : "Revoke"}
    </button>
  );
}
