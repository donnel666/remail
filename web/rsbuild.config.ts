import { defineConfig } from "@rsbuild/core";
import { pluginReact } from "@rsbuild/plugin-react";
import { HttpsProxyAgent } from "https-proxy-agent";

const apiProxyTarget =
  process.env.REMAIL_API_PROXY_TARGET || "https://remail.aishop6.com";
const proxyUrl = process.env.HTTPS_PROXY || process.env.https_proxy;
const proxyAgent = proxyUrl ? new HttpsProxyAgent(proxyUrl) : undefined;
const rewriteProxyCookies =
  process.env.REMAIL_DEV_PROXY_REWRITE_COOKIES !== "false";

const PROXY_HEADERS_TO_STRIP = [
  "origin",
  "referer",
  "sec-fetch-dest",
  "sec-fetch-mode",
  "sec-fetch-site",
  "sec-fetch-user",
  "access-control-request-method",
  "access-control-request-headers",
];

function rewriteDevSetCookie(cookie: string) {
  const parts = cookie
    .split(";")
    .map((part) => part.trim())
    .filter(Boolean);

  if (parts.length === 0) return cookie;

  const [nameValue, ...attributes] = parts;
  const rewrittenAttributes = attributes.flatMap((attribute) => {
    const lower = attribute.toLowerCase();
    if (lower === "secure" || lower.startsWith("domain=")) {
      return [];
    }
    if (lower === "samesite=none") {
      return ["SameSite=Lax"];
    }
    return [attribute];
  });

  return [nameValue, ...rewrittenAttributes].join("; ");
}

function rewriteDevSetCookieHeaders(
  setCookie: string | string[] | undefined
) {
  if (!setCookie) return setCookie;
  if (Array.isArray(setCookie)) {
    return setCookie.map(rewriteDevSetCookie);
  }
  return rewriteDevSetCookie(setCookie);
}

export default defineConfig({
  plugins: [pluginReact()],
  html: {
    template: "./public/index.html",
  },
  source: {
    entry: { index: "./src/index.tsx" },
  },
  output: {
    distPath: { root: "dist" },
  },
  server: {
    port: 3000,
    proxy: {
      "/v1": {
        target: apiProxyTarget,
        changeOrigin: true,
        secure: true,
        agent: proxyAgent,
        proxyTimeout: 30_000,
        timeout: 30_000,
        bypass(req) {
          for (const header of PROXY_HEADERS_TO_STRIP) {
            delete req.headers[header];
          }
          return undefined;
        },
        onProxyRes(proxyRes) {
          if (!rewriteProxyCookies) return;
          proxyRes.headers["set-cookie"] = rewriteDevSetCookieHeaders(
            proxyRes.headers["set-cookie"]
          );
        },
        onError(error) {
          console.error("[dev proxy] /v1 proxy failed:", error);
        },
      },
    },
  },
});
