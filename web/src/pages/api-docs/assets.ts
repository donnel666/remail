declare global {
  interface Window {
    SwaggerUIBundle?: SwaggerUIBundleFactory;
    SwaggerUIStandalonePreset?: unknown;
  }
}

export interface SwaggerUIBundleFactory {
  (config: Record<string, unknown>): unknown;
  presets?: {
    apis?: unknown;
  };
}

export const swaggerStyleHref = "/vendor/swagger-ui/swagger-ui.css";
export const swaggerBundleSrc = "/vendor/swagger-ui/swagger-ui-bundle.js";
export const swaggerStandalonePresetSrc =
  "/vendor/swagger-ui/swagger-ui-standalone-preset.js";

type PublicOpenApiSpec = Record<string, unknown> & {
  servers?: Array<Record<string, unknown>>;
};

let bundleScriptPromise: Promise<void> | undefined;
let standaloneScriptPromise: Promise<void> | undefined;
let openApiSpecPromise: Promise<PublicOpenApiSpec> | undefined;

function ensureStylesheet(href: string) {
  if (document.querySelector(`link[href="${href}"]`)) return;
  const link = document.createElement("link");
  link.href = href;
  link.rel = "stylesheet";
  document.head.appendChild(link);
}

function loadScriptOnce(src: string) {
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

function loadSwaggerBundle() {
  bundleScriptPromise ??= loadScriptOnce(swaggerBundleSrc);
  return bundleScriptPromise;
}

function loadSwaggerStandalonePreset() {
  standaloneScriptPromise ??= loadScriptOnce(swaggerStandalonePresetSrc);
  return standaloneScriptPromise;
}

function getPublicOpenApiSpec() {
  openApiSpecPromise ??= fetch("/openapi.json", {
    credentials: "same-origin",
    headers: {
      Accept: "application/json",
    },
  }).then((response) => {
    if (!response.ok) {
      throw new Error(`Failed to load openapi.json: ${response.status}`);
    }
    return response.json() as Promise<PublicOpenApiSpec>;
  });
  return openApiSpecPromise;
}

function getCurrentServerUrl() {
  if (typeof window === "undefined") return "/";
  return window.location.origin;
}

export async function loadSwaggerAssets() {
  ensureStylesheet(swaggerStyleHref);
  await Promise.all([loadSwaggerBundle(), loadSwaggerStandalonePreset()]);
}

export async function loadRuntimeOpenApiSpec() {
  const spec = await getPublicOpenApiSpec();
  return {
    ...spec,
    servers: [
      {
        url: getCurrentServerUrl(),
        description: "当前访问地址",
      },
    ],
  } satisfies PublicOpenApiSpec;
}

export function preloadApiDocsAssets() {
  if (typeof window === "undefined") return;
  ensureStylesheet(swaggerStyleHref);
  void loadSwaggerBundle().catch((error: unknown) => {
    console.warn("Failed to preload Swagger UI bundle", error);
  });
  void loadSwaggerStandalonePreset().catch((error: unknown) => {
    console.warn("Failed to preload Swagger UI preset", error);
  });
  void getPublicOpenApiSpec().catch((error: unknown) => {
    console.warn("Failed to preload openapi.json", error);
  });
}
