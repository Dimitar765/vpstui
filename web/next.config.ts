import type { NextConfig } from "next";

// Static export: served by the Go daemon (internal/server/webout).
// Dev-only API rewrites are gated behind VPSTUI_DEV because output:'export'
// rejects rewrites at build time.
const nextConfig: NextConfig = {
  output: "export",
  // Keep /api fetches unredirected in dev (308 on POST breaks the daemon);
  // page routes still export as directory/index.html for the embed.
  skipTrailingSlashRedirect: true,
  // Static export + the daemon's SPA fallback: directory-style routes so a
  // direct hit on /setup/ resolves to setup/index.html in the embed.
  trailingSlash: true,
  images: { unoptimized: true },
  turbopack: {
    rules: {
      "*.css": {
        loaders: ["@tailwindcss/turbopack"],
        as: "*.css",
      },
    },
  },
  ...(process.env.VPSTUI_DEV
    ? {
        async rewrites() {
          return [
            {
              source: "/api/:path*",
              destination: `${process.env.VPSTUI_API ?? "http://127.0.0.1:8713"}/api/:path*`,
            },
          ];
        },
      }
    : {}),
};

export default nextConfig;
