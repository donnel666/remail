import { useNavigate } from "@tanstack/react-router";
import { useEffect, useId } from "react";

import { listAPIKeys } from "@/lib/openapi-credentials-api";

import { installSwaggerZhCn } from "./api-docs/swagger-zh-cn";

declare global {
  interface Window {
    SwaggerUIBundle?: SwaggerUIBundleFactory;
    SwaggerUIStandalonePreset?: unknown;
  }
}

interface SwaggerUIBundleFactory {
  (config: Record<string, unknown>): unknown;
  presets?: {
    apis?: unknown;
  };
}

const swaggerStyleHref = "/vendor/swagger-ui/swagger-ui.css";
const swaggerBundleSrc = "/vendor/swagger-ui/swagger-ui-bundle.js";
const swaggerStandalonePresetSrc =
  "/vendor/swagger-ui/swagger-ui-standalone-preset.js";
const swaggerTagOrder = [
  "Core",
  "Resources",
  "Wallet",
  "Tickets",
];

const developmentFallbackApiKeys = [
  {
    enabled: true,
    name: "主账户开放接口密钥",
    token: "rk-7q9m2x4n8p6v1c3b5d0a",
  },
  {
    enabled: true,
    name: "订单服务专用密钥",
    token: "rk-4h8s1k6p9t2w5y7m3c0e",
  },
];

function ensureStylesheet(href: string) {
  if (document.querySelector(`link[href="${href}"]`)) return;
  const link = document.createElement("link");
  link.href = href;
  link.rel = "stylesheet";
  document.head.appendChild(link);
}

function loadScript(src: string) {
  return new Promise<void>((resolve, reject) => {
    const existing = document.querySelector<HTMLScriptElement>(
      `script[src="${src}"]`
    );
    if (existing?.dataset.loaded === "true") {
      resolve();
      return;
    }
    if (existing) {
      existing.addEventListener("load", () => resolve(), { once: true });
      existing.addEventListener("error", reject, { once: true });
      return;
    }

    const script = document.createElement("script");
    script.src = src;
    script.async = true;
    script.addEventListener(
      "load",
      () => {
        script.dataset.loaded = "true";
        resolve();
      },
      { once: true }
    );
    script.addEventListener("error", reject, { once: true });
    document.body.appendChild(script);
  });
}

export default function ApiDocs() {
  const containerId = useId().replace(/:/g, "");
  const navigate = useNavigate();

  useEffect(() => {
    let disposed = false;
    let zhCnController: ReturnType<typeof installSwaggerZhCn> | undefined;

    const apiKeysPromise = listAPIKeys({ limit: 100, offset: 0 })
      .then((response) =>
        response.items.map((item) => ({
          enabled: item.enabled,
          name: item.name || item.keyPrefix || "API Key",
          token: item.keyPlain || item.keyPrefix || "",
        }))
      )
      .catch((error: unknown) => {
        console.warn("Failed to load API keys for Swagger UI", error);
        return [];
      })
      .then((apiKeys) =>
        apiKeys.length === 0 && import.meta.env.DEV
          ? developmentFallbackApiKeys
          : apiKeys
      );

    async function bootstrapSwagger() {
      ensureStylesheet(swaggerStyleHref);
      await loadScript(swaggerBundleSrc);
      await loadScript(swaggerStandalonePresetSrc);
      if (disposed || !window.SwaggerUIBundle) return;

      const presets = [
        window.SwaggerUIBundle.presets?.apis,
        window.SwaggerUIStandalonePreset,
      ].filter(Boolean);

      window.SwaggerUIBundle({
        deepLinking: true,
        docExpansion: "list",
        dom_id: `#${containerId}`,
        layout: "StandaloneLayout",
        presets,
        requestSnippetsEnabled: true,
        showExtensions: true,
        showMutatedRequest: true,
        tagsSorter: (left: string, right: string) => {
          const leftIndex = swaggerTagOrder.indexOf(left);
          const rightIndex = swaggerTagOrder.indexOf(right);
          if (leftIndex === -1 && rightIndex === -1) {
            return left.localeCompare(right);
          }
          if (leftIndex === -1) return 1;
          if (rightIndex === -1) return -1;
          return leftIndex - rightIndex;
        },
        tryItOutEnabled: true,
        url: "/openapi.json",
        validatorUrl: null,
      });

      const container = document.getElementById(containerId);
      if (container && !disposed) {
        zhCnController = installSwaggerZhCn(container, [], {
          onManageApiKey: () => {
            void navigate({ to: "/account" });
          },
        });
        const apiKeys = await apiKeysPromise;
        if (!disposed) {
          zhCnController.setApiKeys(apiKeys);
        }
      }
    }

    void bootstrapSwagger().catch((error: unknown) => {
      console.error("Failed to load Swagger UI", error);
    });

    return () => {
      disposed = true;
      zhCnController?.dispose();
      const container = document.getElementById(containerId);
      if (container) container.innerHTML = "";
    };
  }, [containerId, navigate]);

  return (
    <div className="api-docs-page">
      <div className="api-docs-swagger" id={containerId} />
    </div>
  );
}
