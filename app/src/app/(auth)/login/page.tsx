import { AuthForm } from "../auth-form";

export default async function LoginPage({ searchParams }: PageProps<"/login">) {
  const { error, reset, deleted } = await searchParams;
  const notice =
    reset === "1" ? "Password changed. Sign in with your new password." : deleted === "1" ? "Your account and all its projects were deleted." : undefined;
  const linkError = error ? "That link is invalid or has expired. Sign in to get a new one." : undefined;
  return <AuthForm mode="login" notice={notice} initialError={linkError} />;
}
