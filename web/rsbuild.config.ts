import { defineConfig } from "@rsbuild/core";
import { pluginReact } from "@rsbuild/plugin-react";

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
      "/v1/": {
        target: "http://localhost:8080",
      },
    },
  },
});
