import type { NextConfig } from "next";

// In productie wordt de app een static export die de Go-server inbedt. Tijdens
// `next dev` stuurt Next.js /api door naar een lokaal draaiende server.
const isDev = process.env.NODE_ENV !== "production";
const apiTarget = process.env.CF_API_URL ?? "http://localhost:8080";

const config: NextConfig = isDev
  ? {
      async rewrites() {
        return [{ source: "/api/:path*", destination: `${apiTarget}/api/:path*` }];
      },
    }
  : {
      output: "export",
      images: { unoptimized: true },
    };

export default config;
