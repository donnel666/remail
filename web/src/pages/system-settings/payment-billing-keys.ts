export const EPAY_GATEWAY_KEYS = [
  "epay_enabled",
  "epay_version",
  "epay_gateway_url",
  "epay_merchant_id",
  "epay_merchant_key",
  "epay_private_key",
  "epay_platform_public_key",
  "epay_notify_url",
  "epay_return_url",
] as const;

export const EPAY_WRITE_ONLY_KEYS = ["epay_merchant_key", "epay_private_key"] as const;

export const TOPUP_KEYS = [
  "min_topup_amount",
  "topup_fee_rate",
  "topup_fee_cap",
  "topup_amount_presets",
  "topup_amount_bonus",
  "max_pending_recharge_orders",
] as const;

export const RECHARGE_CHECK_KEYS = [
  "async_check_request_timeout_seconds",
] as const;

export const PROJECT_PRICE_KEYS = [
  "default_project_microsoft_code_price",
  "default_project_microsoft_code_supplier_price",
  "default_project_microsoft_purchase_price",
  "default_project_microsoft_purchase_supplier_price",
  "default_project_domain_code_price",
  "default_project_domain_code_supplier_price",
  "default_project_domain_purchase_price",
  "default_project_domain_purchase_supplier_price",
] as const;

export const PAYMENT_BILLING_KEYS = [
  ...EPAY_GATEWAY_KEYS,
  ...TOPUP_KEYS,
  ...RECHARGE_CHECK_KEYS,
  ...PROJECT_PRICE_KEYS,
] as const;

export function applyEPayURLDefaults(form: Record<string, unknown>, origin: string): Record<string, unknown> {
  const version = String(form.epay_version) === "v2" ? "v2" : "v1";
  return {
    ...form,
    epay_notify_url: String(form.epay_notify_url ?? "").trim() || `${origin}/v1/payments/webhooks/epay/${version}`,
    epay_return_url: String(form.epay_return_url ?? "").trim() || `${origin}/wallet`,
  };
}

export function changeEPayVersion(form: Record<string, unknown>, version: string, origin: string): Record<string, unknown> {
  const currentDefault = applyEPayURLDefaults({ epay_version: form.epay_version }, origin).epay_notify_url;
  return applyEPayURLDefaults({
    ...form,
    epay_version: version,
    epay_notify_url: form.epay_notify_url === currentDefault ? "" : form.epay_notify_url,
  }, origin);
}
