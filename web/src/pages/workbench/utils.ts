import type {
  InventoryScope,
  ProductType,
  ServiceMode,
  ServiceState,
  WorkbenchOrder,
} from "./types";

export function formatMoney(value: number) {
  if (!Number.isFinite(value)) return "￥0";
  return `￥${value.toFixed(2).replace(/\.00$/, "")}`;
}

export function formatDateTime(value?: string) {
  if (!value) return "-";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "-";
  return new Intl.DateTimeFormat("zh-CN", {
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    month: "2-digit",
  }).format(date);
}

export function remainingMinutes(value?: string) {
  if (!value) return 0;
  const time = new Date(value).getTime();
  if (!Number.isFinite(time)) return 0;
  return Math.max(0, Math.ceil((time - Date.now()) / 60000));
}

export function formatDurationFromMinutes(minutes: number) {
  if (!Number.isFinite(minutes) || minutes <= 0) return "0m";
  const days = Math.floor(minutes / 1440);
  const hours = Math.floor((minutes % 1440) / 60);
  const restMinutes = minutes % 60;
  if (days > 0) {
    return restMinutes > 0 ? `${days}d ${hours}h ${restMinutes}m` : `${days}d ${hours}h`;
  }
  if (hours > 0) {
    return restMinutes > 0 ? `${hours}h ${restMinutes}m` : `${hours}h`;
  }
  return `${restMinutes}m`;
}

export function formatRemainingDuration(value?: string) {
  return formatDurationFromMinutes(remainingMinutes(value));
}

export function maskMiddle(value: string, head = 6, tail = 4) {
  if (!value) return "-";
  if (value.length <= head + tail + 3) return value;
  return `${value.slice(0, head)}***${value.slice(-tail)}`;
}

export function maskSecret(value: string, head = 3, tail = 4) {
  if (!value) return "-";
  if (value.length <= head + tail) {
    const visibleHead = Math.min(head, Math.max(1, value.length - 1));
    return `${value.slice(0, visibleHead)}***`;
  }
  return `${value.slice(0, head)}***${value.slice(-tail)}`;
}

export function buildPickupUrl(email: string, token: string) {
  const origin =
    typeof window === "undefined" ? "" : window.location.origin;
  return `${origin}/pickup?email=${encodeURIComponent(email)}&token=${encodeURIComponent(token)}`;
}

export function serviceModeLabel(mode: ServiceMode, t: (key: string) => string) {
  return mode === "code" ? t("Code receiving") : t("Purchase");
}

export function inventoryScopeLabel(
  scope: InventoryScope,
  t: (key: string) => string
) {
  return scope === "private_first" ? t("Private first") : t("Public only");
}

export function productTypeLabel(type: ProductType, t: (key: string) => string) {
  return type === "microsoft" ? t("Microsoft email") : t("Domain email");
}

export function serviceStateMeta(
  state: ServiceState,
  t: (key: string) => string
) {
  if (state === "waiting_mail") {
    return { color: "amber" as const, label: t("Waiting mail") };
  }
  if (state === "code_received") {
    return { color: "green" as const, label: t("Code received") };
  }
  if (state === "pending_activation") {
    return { color: "amber" as const, label: t("Pending activation") };
  }
  if (state === "activated") {
    return { color: "green" as const, label: t("Activated") };
  }
  if (state === "in_warranty") {
    return { color: "blue" as const, label: t("In warranty") };
  }
  if (state === "activation_timeout") {
    return { color: "red" as const, label: t("Activation timed out") };
  }
  if (state === "order_failed") {
    return { color: "red" as const, label: t("Order failed") };
  }
  if (state === "refunded") {
    return { color: "grey" as const, label: t("Refunded") };
  }
  if (state === "warranty_ended") {
    return { color: "grey" as const, label: t("Warranty ended") };
  }
  return { color: "grey" as const, label: t("Read expired") };
}

export function orderStatusLabel(status: WorkbenchOrder["status"], t: (key: string) => string) {
  if (status === "pending_payment") return t("Pending payment");
  if (status === "paid") return t("Paid");
  if (status === "active") return t("Active");
  if (status === "completed") return t("Completed");
  if (status === "failed") return t("Failed");
  if (status === "closed") return t("Closed");
  return t("Refunded");
}

export function matchesProjectEmailSearch(
  order: WorkbenchOrder,
  search: string,
  projectName?: string
) {
  const q = search.trim().toLowerCase();
  if (!q) return true;
  return [projectName ?? "", order.deliveryEmail]
    .join(" ")
    .toLowerCase()
    .includes(q);
}
