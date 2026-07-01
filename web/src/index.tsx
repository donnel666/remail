import "@douyinfe/semi-ui/react19-adapter";
import React from "react";
import { createRoot } from "react-dom/client";
import "@douyinfe/semi-ui/dist/css/semi.css";
import App from "./App";
import "./i18n/config";
import "./index.css";

const root = createRoot(document.getElementById("root")!);
root.render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);
