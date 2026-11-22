import type { Metadata } from "next";
import type { ReactNode } from "react";
import "./globals.css";
import { themeBootScript } from "@/lib/themeBoot";
import { Providers } from "./providers";

export const metadata: Metadata = {
  // Geen vaste titel: elke pagina zet haar eigen <title> (zie (app)/layout.tsx),
  // en de eerste <title> in <head> zou die anders overschrijven.
  description: "Beheer, monitoring en deployment van je HA-clusters",
};

export default function RootLayout({ children }: { children: ReactNode }) {
  return (
    <html lang="nl" suppressHydrationWarning>
      <head>
        <script dangerouslySetInnerHTML={{ __html: themeBootScript }} />
      </head>
      <body className="min-h-screen bg-slate-50 text-slate-900 antialiased dark:bg-slate-950 dark:text-slate-100">
        <Providers>{children}</Providers>
      </body>
    </html>
  );
}
