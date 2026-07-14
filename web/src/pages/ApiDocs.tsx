import { useNavigate } from "@tanstack/react-router";
import { useEffect, useId } from "react";

import { useAuth } from "@/context/auth-provider";
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
    token: "rk-550e8400-e29b-41d4-a716-446655440000",
  },
  {
    enabled: true,
    name: "订单服务专用密钥",
    token: "rk-3d813cbb-47fb-42ba-91df-831e1593ac29",
  },
];

export default function ApiDocs() {
  const containerId = useId().replace(/:/g, "");
  const navigate = useNavigate();
  const { currentUser } = useAuth();

  useEffect(() => {
    let disposed = false;
    let zhCnController: ReturnType<typeof installSwaggerZhCn> | undefined;

    const apiKeysPromise = currentUser
      ? listAPIKeys({ limit: 100, offset: 0 })
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
          )
      : Promise.resolve([]);

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
  }, [containerId, currentUser, navigate]);

  return (
    <div className="api-docs-page">
      <div className="api-docs-swagger" id={containerId} />
    </div>
  );
}
