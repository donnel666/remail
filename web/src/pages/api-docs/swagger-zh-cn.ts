const textTranslations: Record<string, string> = {
  "Accept": "接收",
  "Additional properties": "附加属性",
  "All of": "全部满足",
  "Any of": "任一满足",
  "Apply credentials": "应用认证",
  "Apply given OAuth2 credentials": "应用 OAuth2 认证",
  "Authorization URL:": "授权 URL：",
  "Authorization header": "认证请求头",
  "Authorize": "认证",
  "Authorized": "已认证",
  "Available authorizations": "可用认证",
  "Basic authorization": "Basic 认证",
  "Wallet": "财务管理",
  "Callbacks": "回调",
  "Cancel": "取消",
  "Clear": "清空",
  "Client credentials location:": "客户端凭证位置：",
  "Close": "关闭",
  "Code": "状态码",
  "Collapse": "收起",
  "Collapse all": "全部收起",
  "Collapse operation": "收起接口",
  "Const": "常量",
  "Contact": "联系信息",
  "Contains": "包含",
  "Content schema": "内容结构",
  "Core": "核心 API",
  "Controls Accept header.": "控制 Accept 请求头。",
  "Copy to clipboard": "复制",
  "Curl": "cURL",
  "Default": "默认值",
  "Dependent schemas": "依赖结构",
  "Deprecated": "已废弃",
  "Deprecated:": "已废弃：",
  "Description": "说明",
  "Details": "详情",
  "Discriminator": "区分字段",
  "Download": "下载",
  "Download file": "下载文件",
  "Edit Value": "编辑值",
  "Else": "否则",
  "Encoding": "编码",
  "Enum": "枚举",
  "Enum:": "枚举：",
  "Example": "示例",
  "Example Description": "示例说明",
  "Example Value": "示例值",
  "Examples": "示例",
  "Examples:": "示例：",
  "Execute": "发送请求",
  "Expand all": "全部展开",
  "Expand operation": "展开接口",
  "Filter by tag": "按标签筛选",
  "If": "如果",
  "In:": "位置：",
  "JSON Schema dialect:": "JSON Schema 方言：",
  "Links": "链接",
  "Logout": "退出认证",
  "Media Type": "媒体类型",
  "Media type": "媒体类型",
  "Model": "模型",
  "Models": "模型",
  "Name": "名称",
  "Name:": "名称：",
  "No API definition provided.": "未提供 API 定义。",
  "No callbacks": "无回调",
  "No links": "无链接",
  "No parameters": "无参数",
  "No operations defined in spec!": "接口文档中未定义接口。",
  "Not": "非",
  "Object": "对象",
  "One of": "其中之一",
  "Parameter": "参数",
  "Parameter content type": "参数内容类型",
  "Parameters": "参数",
  "Pattern properties": "模式属性",
  "Prefix items": "前缀项",
  "Properties": "属性",
  "Property names": "属性名称",
  "Remove authorization": "移除认证",
  "Resources": "资源管理",
  "Request URL": "请求 URL",
  "Request body": "请求体",
  "Request content type": "请求内容类型",
  "Request duration": "请求耗时",
  "Request snippets": "请求片段",
  "Required": "必填",
  "Required field is not provided": "必填字段未填写",
  "Required property not found": "未找到必填属性",
  "Response": "响应",
  "Response body": "响应体",
  "Response content type": "响应内容类型",
  "Response headers": "响应头",
  "Responses": "响应",
  "Schema": "结构",
  "Schemas": "数据结构",
  "Select": "选择",
  "Select a definition": "选择定义",
  "Select a server": "选择服务器",
  "Server": "服务器",
  "Server response": "服务器响应",
  "Server variables": "服务器变量",
  "Servers": "服务器",
  "Show/Hide": "显示/隐藏",
  "String": "字符串",
  "Then": "则",
  "Tickets": "工单管理",
  "Try it out": "调试",
  "Type": "类型",
  "Unevaluated items": "未求值项",
  "Unevaluated properties": "未求值属性",
  "Value": "值",
  "Value:": "值：",
  "XML": "XML",
  "authorization button locked": "认证按钮已锁定",
  "authorization button unlocked": "认证按钮已解锁",
  "curl": "cURL",
};

const attributeTranslations: Record<string, string> = {
  ...textTranslations,
  "Copy": "复制",
  "Download contents": "下载内容",
};

const translatableAttributes = [
  "aria-label",
  "alt",
  "placeholder",
  "title",
] as const;

const ignoredTextSelector = [
  "code",
  "pre",
  "script",
  "style",
  "textarea",
  ".highlight-code",
  ".language-bash",
  ".language-json",
  ".microlight",
].join(",");

export interface SwaggerApiKeyOption {
  enabled?: boolean;
  name: string;
  token: string;
}

export interface SwaggerZhCnController {
  dispose: () => void;
  setApiKeys: (apiKeys: SwaggerApiKeyOption[]) => void;
}

export interface SwaggerZhCnOptions {
  onManageApiKey?: () => void;
}

const apiKeyMenuSelector = "[data-remail-api-key-menu='true']";
const apiKeyManageLinkSelector = "[data-remail-api-key-manage='true']";
let apiKeyPickerSeed = 0;

function normalizeText(value: string) {
  return value.replace(/\s+/g, " ").trim();
}

function translateValue(
  value: string | null,
  translations: Record<string, string>
) {
  if (!value) return null;
  const normalized = normalizeText(value);
  return translations[normalized] ?? null;
}

function translateTextNode(node: Text) {
  const currentValue = node.nodeValue;
  if (!currentValue || !node.parentElement) return;
  if (node.parentElement.closest(ignoredTextSelector)) return;

  const translated = translateValue(currentValue, textTranslations);
  if (!translated) return;

  const leading = currentValue.match(/^\s*/)?.[0] ?? "";
  const trailing = currentValue.match(/\s*$/)?.[0] ?? "";
  node.nodeValue = `${leading}${translated}${trailing}`;
}

function translateElementAttributes(element: Element) {
  for (const attr of translatableAttributes) {
    const translated = translateValue(
      element.getAttribute(attr),
      attributeTranslations
    );
    if (translated) {
      element.setAttribute(attr, translated);
    }
  }
}

function maskApiKey(value: string) {
  if (value.length <= 18) return value;
  return `${value.slice(0, 7)}**********${value.slice(-4)}`;
}

function toApiKeyCredential(value: string) {
  const token = value.trim();
  return token.toLowerCase().startsWith("bearer ")
    ? token.slice("bearer ".length).trim()
    : token;
}

function syncApiKeyPicker(
  root: HTMLElement,
  apiKeys: SwaggerApiKeyOption[],
  config: SwaggerZhCnOptions
) {
  const apiKeyOptions = apiKeys
    .filter((item) => item.enabled !== false && item.token.trim())
    .map((item) => ({
      label: `${item.name || "API Key"} · ${maskApiKey(item.token.trim())}`,
      name: item.name || "API Key",
      token: maskApiKey(item.token.trim()),
      value: toApiKeyCredential(item.token),
    }));

  root
    .querySelectorAll<HTMLElement>(".auth-container")
    .forEach((container) => {
      const content = normalizeText(container.textContent ?? "");
      if (!content.includes("remailApiKey")) {
        return;
      }

      container
        .querySelectorAll<HTMLInputElement>('input[type="text"], input:not([type])')
        .forEach((input) => {
          ensureManageApiKeyLink(container, input, config);
          input.removeAttribute("list");
          input.setAttribute(
            "placeholder",
            apiKeyOptions.length > 0 ? "选择或输入 API Key" : "输入 rk-..."
          );
          input.autocomplete = "off";
          input.setAttribute("aria-autocomplete", "list");
          input.setAttribute("aria-expanded", "false");
          input.setAttribute("aria-haspopup", "listbox");
          input.setAttribute("role", "combobox");

          const wrapper = input.parentElement;
          if (!wrapper) return;
          wrapper.classList.add("swagger-api-key-picker");
          if (!input.dataset.remailApiKeyPickerId) {
            apiKeyPickerSeed += 1;
            input.dataset.remailApiKeyPickerId = String(apiKeyPickerSeed);
          }

          let menu = wrapper.querySelector<HTMLDivElement>(apiKeyMenuSelector);
          if (apiKeyOptions.length === 0) {
            menu?.remove();
            return;
          }

          if (!menu) {
            menu = document.createElement("div");
            menu.className = "swagger-api-key-menu";
            menu.dataset.remailApiKeyMenu = "true";
            menu.hidden = true;
            menu.setAttribute("role", "listbox");
            wrapper.appendChild(menu);
          }

          if (input.dataset.remailApiKeyPickerBound !== "true") {
            input.dataset.remailApiKeyPickerBound = "true";
            input.addEventListener("focus", () => {
              showApiKeyMenu(input, wrapper);
            });
            input.addEventListener("click", () => {
              showApiKeyMenu(input, wrapper);
            });
            input.addEventListener("keydown", (event) => {
              handleApiKeyPickerKeydown(event, input, wrapper);
            });
            input.addEventListener("blur", () => {
              window.setTimeout(() => {
                const nextMenu =
                  wrapper.querySelector<HTMLDivElement>(apiKeyMenuSelector);
                if (nextMenu) hideApiKeyMenu(input, nextMenu);
              }, 120);
            });
          }

          const signature = apiKeyOptions
            .map((item) => `${item.label}\u001f${item.value}`)
            .join("\u001e");
          if (menu.dataset.remailApiKeySignature === signature) return;

          menu.replaceChildren(
            ...apiKeyOptions.map((item, index) => {
              const button = document.createElement("button");
              button.className = "swagger-api-key-option";
              button.id = `swagger-api-key-option-${input.dataset.remailApiKeyPickerId}-${index}`;
              button.type = "button";
              button.setAttribute("role", "option");
              button.addEventListener("mousedown", (event) => {
                event.preventDefault();
              });
              button.addEventListener("mouseenter", () => {
                setActiveApiKeyOption(input, menu, index);
              });
              button.addEventListener("click", () => {
                setInputValue(input, item.value);
                hideApiKeyMenu(input, menu);
              });

              const name = document.createElement("span");
              name.className = "swagger-api-key-option-name";
              name.textContent = item.name;

              const token = document.createElement("span");
              token.className = "swagger-api-key-option-token";
              token.textContent = item.token;

              button.append(name, token);
              return button;
            })
          );
          delete menu.dataset.activeIndex;
          input.removeAttribute("aria-activedescendant");
          menu.dataset.remailApiKeySignature = signature;
        });
    });
}

function getApiKeyOptionButtons(menu: HTMLElement) {
  return Array.from(
    menu.querySelectorAll<HTMLButtonElement>(".swagger-api-key-option")
  );
}

function setActiveApiKeyOption(
  input: HTMLInputElement,
  menu: HTMLElement,
  index: number
) {
  const buttons = getApiKeyOptionButtons(menu);
  if (buttons.length === 0) return;

  const nextIndex = Math.max(0, Math.min(index, buttons.length - 1));
  buttons.forEach((button, buttonIndex) => {
    const active = buttonIndex === nextIndex;
    button.classList.toggle("is-active", active);
    button.setAttribute("aria-selected", active ? "true" : "false");
  });
  menu.dataset.activeIndex = String(nextIndex);
  input.setAttribute("aria-activedescendant", buttons[nextIndex].id);
  buttons[nextIndex].scrollIntoView({ block: "nearest" });
}

function showApiKeyMenu(input: HTMLInputElement, wrapper: HTMLElement) {
  const menu = wrapper.querySelector<HTMLDivElement>(apiKeyMenuSelector);
  if (!menu?.children.length) return;
  menu.hidden = false;
  input.setAttribute("aria-expanded", "true");
  if (!menu.dataset.activeIndex) {
    setActiveApiKeyOption(input, menu, 0);
  }
}

function hideApiKeyMenu(input: HTMLInputElement, menu: HTMLElement) {
  menu.hidden = true;
  input.setAttribute("aria-expanded", "false");
  input.removeAttribute("aria-activedescendant");
}

function handleApiKeyPickerKeydown(
  event: KeyboardEvent,
  input: HTMLInputElement,
  wrapper: HTMLElement
) {
  const menu = wrapper.querySelector<HTMLDivElement>(apiKeyMenuSelector);
  if (!menu?.children.length) return;

  if (event.key === "Escape") {
    hideApiKeyMenu(input, menu);
    return;
  }

  if (event.key !== "ArrowDown" && event.key !== "ArrowUp" && event.key !== "Enter") {
    return;
  }

  event.preventDefault();

  if (menu.hidden) {
    showApiKeyMenu(input, wrapper);
  }

  const buttons = getApiKeyOptionButtons(menu);
  if (buttons.length === 0) return;

  const currentIndex = Number.parseInt(menu.dataset.activeIndex ?? "0", 10);
  const safeCurrentIndex = Number.isFinite(currentIndex) ? currentIndex : 0;

  if (event.key === "ArrowDown") {
    setActiveApiKeyOption(
      input,
      menu,
      Math.min(safeCurrentIndex + 1, buttons.length - 1)
    );
    return;
  }

  if (event.key === "ArrowUp") {
    setActiveApiKeyOption(input, menu, Math.max(safeCurrentIndex - 1, 0));
    return;
  }

  buttons[safeCurrentIndex]?.click();
}

function setInputValue(input: HTMLInputElement, value: string) {
  const valueSetter = Object.getOwnPropertyDescriptor(
    HTMLInputElement.prototype,
    "value"
  )?.set;
  if (valueSetter) {
    valueSetter.call(input, value);
  } else {
    input.value = value;
  }
  input.dispatchEvent(new Event("input", { bubbles: true }));
  input.dispatchEvent(new Event("change", { bubbles: true }));
}

function ensureManageApiKeyLink(
  container: HTMLElement,
  input: HTMLInputElement,
  options: SwaggerZhCnOptions
) {
  if (container.querySelector(apiKeyManageLinkSelector)) return;

  const actionRow = document.createElement("div");
  actionRow.className = "swagger-api-key-action-row";

  const link = document.createElement("a");
  link.className = "swagger-api-key-manage-link";
  link.dataset.remailApiKeyManage = "true";
  link.href = "/account";
  link.textContent = "管理 API Key";
  if (options.onManageApiKey) {
    link.addEventListener("click", (event) => {
      event.preventDefault();
      options.onManageApiKey?.();
    });
  }

  actionRow.appendChild(link);

  const description = Array.from(
    container.querySelectorAll<HTMLElement>("p, .markdown, .renderedMarkdown")
  ).find((element) => {
    const content = normalizeText(element.textContent ?? "");
    return !element.contains(input) && content.includes("API Key");
  });

  if (description) {
    description.insertAdjacentElement("afterend", actionRow);
    return;
  }

  const inputWrapper = input.parentElement;
  const beforeValueLabel = inputWrapper?.previousElementSibling ?? inputWrapper;
  if (beforeValueLabel) {
    beforeValueLabel.insertAdjacentElement("beforebegin", actionRow);
  }
}

function translateTree(
  root: HTMLElement,
  apiKeys: SwaggerApiKeyOption[],
  options: SwaggerZhCnOptions
) {
  translateElementAttributes(root);
  syncApiKeyPicker(root, apiKeys, options);

  root.querySelectorAll("*").forEach((element) => {
    translateElementAttributes(element);
  });

  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
  let current = walker.nextNode();
  while (current) {
    translateTextNode(current as Text);
    current = walker.nextNode();
  }
}

export function installSwaggerZhCn(
  root: HTMLElement,
  initialApiKeys: SwaggerApiKeyOption[] = [],
  options: SwaggerZhCnOptions = {}
): SwaggerZhCnController {
  let frameId = 0;
  let apiKeys = initialApiKeys;

  const scheduleTranslate = () => {
    if (frameId) return;
    frameId = window.requestAnimationFrame(() => {
      frameId = 0;
      if (root.isConnected) {
        translateTree(root, apiKeys, options);
      }
    });
  };

  translateTree(root, apiKeys, options);

  const observer = new MutationObserver(scheduleTranslate);
  observer.observe(root, {
    attributeFilter: [...translatableAttributes],
    attributes: true,
    characterData: true,
    childList: true,
    subtree: true,
  });

  return {
    dispose: () => {
      observer.disconnect();
      if (frameId) {
        window.cancelAnimationFrame(frameId);
      }
    },
    setApiKeys: (nextApiKeys) => {
      apiKeys = nextApiKeys;
      scheduleTranslate();
    },
  };
}
