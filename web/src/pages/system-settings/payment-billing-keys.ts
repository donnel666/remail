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
] as const;

export const RECHARGE_CHECK_KEYS = [
  "async_check_request_timeout_seconds",
] as const;

export const PAYMENT_BILLING_KEYS = [
  ...EPAY_GATEWAY_KEYS,
  ...TOPUP_KEYS,
  ...RECHARGE_CHECK_KEYS,
] as const;
