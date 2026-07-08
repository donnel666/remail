import { defineConfig } from "@rsbuild/core";
import { pluginReact } from "@rsbuild/plugin-react";
import { createRequire } from "module";
import { HttpsProxyAgent } from "https-proxy-agent";
import path from "path";

const require = createRequire(import.meta.url);
const semiUiDir = path.resolve(
  path.dirname(require.resolve("@douyinfe/semi-ui")),
  "../.."
);

const apiProxyTarget =
  process.env.REMAIL_API_PROXY_TARGET || "https://remail.aishop6.com";
const proxyUrl = process.env.HTTPS_PROXY || process.env.https_proxy;
const proxyAgent = proxyUrl ? new HttpsProxyAgent(proxyUrl) : undefined;
const rewriteProxyCookies =
  process.env.REMAIL_DEV_PROXY_REWRITE_COOKIES !== "false";
const apiProxyTimeoutMs = 600_000;

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
  resolve: {
    alias: {
      "@douyinfe/semi-ui/dist/css/semi.css": path.resolve(
        semiUiDir,
        "dist/css/semi.css"
      ),
    },
  },
  output: {
    distPath: { root: "dist" },
  },
  performance: {
    chunkSplit: {
      forceSplitting: {
        "lib-react": /node_modules[\\/](?:react|react-dom|scheduler)[\\/]/,
        "lib-semi": /node_modules[\\/]@douyinfe[\\/]/,
        "lib-icons": /node_modules[\\/](?:lucide-react|react-icons)[\\/]/,
      },
    },
  },
  server: {
    port: 3000,
    proxy: {
      "/v1": {
        target: apiProxyTarget,
        changeOrigin: true,
        secure: true,
        agent: proxyAgent,
        proxyTimeout: apiProxyTimeoutMs,
        timeout: apiProxyTimeoutMs,
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
