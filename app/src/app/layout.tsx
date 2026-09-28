import type { Metadata } from "next";
import { connection } from "next/server";
import "./globals.css";

export const metadata: Metadata = {
  title: "DATBase",
  description: "Backend for your app: database, auth, storage and API.",
};

export default async function RootLayout({ children }: LayoutProps<"/">) {
  await connection(); // render per request so every page gets the CSP nonce (see src/proxy.ts)
  return (
    <html lang="en" className="h-full antialiased">
      <body className="min-h-full flex flex-col">{children}</body>
    </html>
  );
}
