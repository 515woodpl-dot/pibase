import { betterAuth } from "better-auth";
import { APIError } from "better-auth/api";
import { eq } from "drizzle-orm";
import { drizzleAdapter } from "better-auth/adapters/drizzle";
import { nextCookies } from "better-auth/next-js";
import { headers } from "next/headers";
import { redirect } from "next/navigation";
import { db, schema } from "@/db";
import { mailConfigured, renderMail, sendMail } from "./mail";

// With SMTP configured, new accounts must verify their email before signing in. Sign-up then answers
// the same way whether or not the email is already registered (closes F-09); the owner of an existing
// account gets an email instead. Without SMTP, accounts work immediately (local testing).
const withEmail = mailConfigured();

export const auth = betterAuth({
  database: drizzleAdapter(db, { provider: "pg", schema }),
  emailAndPassword: {
    enabled: true,
    minPasswordLength: 8,
    requireEmailVerification: withEmail,
    revokeSessionsOnPasswordReset: true,
    resetPasswordTokenExpiresIn: 3600,
    sendResetPassword: async ({ user, url }) => {
      const m = renderMail({
        heading: "Reset your DATBase password",
        lines: ["Someone (hopefully you) asked to reset the password for this DATBase account.", "The link works for 1 hour."],
        button: { label: "Choose a new password", url },
        footer: "If you didn't ask for this, ignore this email. Your password stays the same.",
      });
      await sendMail({ to: user.email, subject: "Reset your DATBase password", ...m });
    },
    onExistingUserSignUp: async ({ user }) => {
      const base = process.env.BETTER_AUTH_URL ?? "";
      const m = renderMail({
        heading: "You already have a DATBase account",
        lines: ["Someone tried to create a new DATBase account with this email address, which already has one."],
        button: { label: "Sign in", url: `${base}/login` },
        footer: `Forgot your password? Reset it at ${base}/forgot-password. If this wasn't you, ignore this email.`,
      });
      await sendMail({ to: user.email, subject: "You already have a DATBase account", ...m });
    },
  },
  emailVerification: {
    sendOnSignUp: withEmail,
    sendOnSignIn: withEmail, // signing in unverified re-sends the link
    autoSignInAfterVerification: true,
    expiresIn: 24 * 3600,
    sendVerificationEmail: async ({ user, url }) => {
      const m = renderMail({
        heading: "Confirm your email for DATBase",
        lines: ["Welcome to DATBase! Confirm your email address to finish creating your account.", "The link works for 24 hours."],
        button: { label: "Confirm email", url },
        footer: "If you didn't sign up for DATBase, ignore this email.",
      });
      await sendMail({ to: user.email, subject: "Confirm your email for DATBase", ...m });
    },
  },
  // Built-in limits already allow 3 sign-in/sign-up attempts per 10s per IP. Also cap new accounts
  // and emails sent per IP.
  rateLimit: {
    customRules: {
      "/sign-up/email": { window: 3600, max: 5 },
      "/request-password-reset": { window: 3600, max: 5 },
      "/send-verification-email": { window: 3600, max: 5 },
    },
  },
  // Better Auth only trusts BETTER_AUTH_URL's origin by default, so reaching the dashboard by a
  // second hostname (e.g. a LAN sslip.io address and a Tailscale one at the same time) loads the
  // page fine but sign-in fails with an origin mismatch. EXTRA_TRUSTED_ORIGINS (comma-separated
  // full origins, e.g. "http://100-119-190-50.sslip.io:9081") adds any others that should work too.
  trustedOrigins: (process.env.EXTRA_TRUSTED_ORIGINS ?? "")
    .split(",")
    .map((s) => s.trim())
    .filter(Boolean),
  // Suspended (by the operator) or deleted accounts can't start a session.
  databaseHooks: {
    session: {
      create: {
        before: async (s) => {
          const u = await db.query.user.findFirst({ where: eq(schema.user.id, s.userId) });
          if (u?.deletedAt) throw new APIError("FORBIDDEN", { message: "This account was deleted." });
          if (u?.suspendedAt) throw new APIError("FORBIDDEN", { message: "This account is suspended. Contact support." });
        },
      },
    },
  },
  plugins: [nextCookies()],
});

/** Returns the signed-in user or redirects to /login. Use in every protected page and action. */
export async function requireUser() {
  const session = await auth.api.getSession({ headers: await headers() });
  if (!session) redirect("/login");
  return session.user;
}

/** The DATBase operator (ADMIN_EMAIL). Needs a verified email when email is on. */
export const isAdmin = (u: { email: string; emailVerified: boolean }) =>
  !!process.env.ADMIN_EMAIL &&
  u.email.toLowerCase() === process.env.ADMIN_EMAIL.toLowerCase() &&
  (u.emailVerified || !mailConfigured());
