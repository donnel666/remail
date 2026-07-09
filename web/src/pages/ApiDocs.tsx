import { useNavigate } from "@tanstack/react-router";
import { useEffect, useId } from "react";

import { listAPIKeys } from "@/lib/openapi-credentials-api";

import {
  loadRuntimeOpenApiSpec,
  loadSwaggerAssets,
} from "./api-docs/assets";
import { installSwaggerZhCn } from "./api-docs/swagger-zh-cn";

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
      const [spec] = await Promise.all([
        loadRuntimeOpenApiSpec(),
        loadSwaggerAssets(),
      ]);
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
        validatorUrl: null,
        spec,
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
