import "server-only";
import nodemailer, { type Transporter } from "nodemailer";

/**
 * Outgoing email over any SMTP provider (Resend, Brevo, Postmark, SES, Gmail, Mailpit for tests).
 *   SMTP_HOST, SMTP_PORT (587), SMTP_USER, SMTP_PASS, SMTP_FROM ("DATBase <no-reply@example.com>")
 *   SMTP_SECURE=true for port 465 (implicit TLS); otherwise STARTTLS is used when offered.
 * Without SMTP_HOST, emails are only logged and email features stay off.
 */
export const mailConfigured = () => !!process.env.SMTP_HOST;

export const smtpSettings = () => {
  const port = Number(process.env.SMTP_PORT ?? 587);
  return {
    host: process.env.SMTP_HOST ?? "",
    port,
    secure: process.env.SMTP_SECURE ? process.env.SMTP_SECURE === "true" : port === 465,
    user: process.env.SMTP_USER ?? "",
    pass: process.env.SMTP_PASS ?? "",
  };
};

/** The sender: { name, address } parsed from SMTP_FROM. */
export function mailFrom() {
  const raw = process.env.SMTP_FROM || `DATBase <no-reply@${process.env.DOMAIN ?? "localhost"}>`;
  const m = raw.match(/^\s*"?([^"<]*?)"?\s*<([^>]+)>\s*$/);
  return m ? { name: m[1] || "DATBase", address: m[2] } : { name: "DATBase", address: raw.trim() };
}

let transport: Transporter | null = null;
function smtp() {
  if (!transport) {
    const s = smtpSettings();
    transport = nodemailer.createTransport({
      host: s.host,
      port: s.port,
      secure: s.secure,
      auth: s.user ? { user: s.user, pass: s.pass } : undefined,
      connectionTimeout: 10_000,
      greetingTimeout: 10_000,
      socketTimeout: 20_000,
    });
  }
  return transport;
}

const esc = (s: string) => s.replace(/[&<>"']/g, (c) => `&#${c.charCodeAt(0)};`);

/** A plain, readable email with one optional button. Text and HTML versions. */
export function renderMail(o: { heading: string; lines: string[]; button?: { label: string; url: string }; footer?: string }) {
  const text = [o.heading, "", ...o.lines, ...(o.button ? ["", `${o.button.label}: ${o.button.url}`] : []), ...(o.footer ? ["", o.footer] : [])].join("\n");
  const html = `<div style="font-family:system-ui,sans-serif;max-width:520px;margin:auto;color:#111">
<p style="font-size:20px;font-weight:700;margin:0 0 16px"><span style="color:#2d7daf">DAT</span><span style="color:#61b895">Base</span></p>
<h2 style="font-size:18px">${esc(o.heading)}</h2>
${o.lines.map((l) => `<p>${esc(l)}</p>`).join("\n")}
${o.button ? `<p><a href="${esc(o.button.url)}" style="display:inline-block;background:#2d7daf;color:#fff;padding:10px 16px;border-radius:6px;text-decoration:none">${esc(o.button.label)}</a></p><p style="font-size:12px;color:#666">Or open: ${esc(o.button.url)}</p>` : ""}
${o.footer ? `<p style="font-size:12px;color:#666">${esc(o.footer)}</p>` : ""}
</div>`;
  return { text, html };
}

/** Send an email. Returns false (and logs) when SMTP isn't configured or sending fails. */
export async function sendMail(m: { to: string; subject: string; text: string; html?: string }) {
  if (!mailConfigured()) {
    console.log(`[mail] SMTP not configured; not sent: "${m.subject}" → ${m.to}`);
    return false;
  }
  try {
    await smtp().sendMail({ from: mailFrom(), ...m });
    return true;
  } catch (e) {
    console.error(`[mail] sending "${m.subject}" to ${m.to} failed:`, e instanceof Error ? e.message : e);
    return false;
  }
}
