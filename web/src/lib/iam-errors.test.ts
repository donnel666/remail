import type { TFunction } from "i18next";
import { describe, expect, it } from "vitest";

import { IamApiError } from "./iam-api";
import { getApiErrorBodyMessage, getIamErrorMessage } from "./iam-errors";

const translations: Record<string, string> = {
  "Authentication is required.": "请先登录。",
  "Insufficient inventory.": "当前库存不足，请减少数量或更换商品。",
  "Order creation failed.": "订单创建失败。",
  "Please retry in {{seconds}} seconds.": "请在 {{seconds}} 秒后重试。",
  "Request failed.": "请求失败。",
  "Service is temporarily unavailable.": "服务暂时不可用，请稍后重试。",
  "Minimum {{currency}} payment is {{amount}} {{currency}}.": "{{currency}} 单笔最低支付金额为 {{amount}} {{currency}}，请选择更高的充值档位。",
  "Lottery account must be at least {{days}} days old.": "账号需满 {{days}} 天。",
};

const t = ((key: string, options?: Record<string, unknown>) => {
  let value = translations[key] ?? String(options?.defaultValue ?? key);
  for (const [name, replacement] of Object.entries(options ?? {})) {
    value = value.split(`{{${name}}}`).join(String(replacement));
  }
  return value;
}) as TFunction;

describe("API error messages", () => {
  it("translates a known backend message", () => {
    const error = new IamApiError(422, {
      message: "Insufficient inventory.",
    });

    expect(getIamErrorMessage(t, error, "Order creation failed.")).toBe(
      "当前库存不足，请减少数量或更换商品。",
    );
  });

  it("translates a coded lottery eligibility error with its required age", () => {
    const error = new IamApiError(403, {
      code: "lottery_account_age",
      message: "Lottery account age requirement not met.",
      fields: { requiredDays: "30" },
    });

    expect(getIamErrorMessage(t, error)).toBe("账号需满 30 天。");
  });

  it("translates the USDT minimum payment error", () => {
    const error = new IamApiError(422, {
      code: "recharge_payment_below_minimum",
      message: "Recharge payment is below the configured minimum.",
      fields: { minimumPaymentAmount: "12.50", paymentCurrency: "USDT" },
    });

    expect(getIamErrorMessage(t, error)).toBe(
      "USDT 单笔最低支付金额为 12.50 USDT，请选择更高的充值档位。",
    );
  });

  it("does not expose an unknown backend message", () => {
    const error = new IamApiError(422, {
      message: "database connection refused",
    });

    expect(getIamErrorMessage(t, error, "Order creation failed.")).toBe(
      "订单创建失败。",
    );
  });

  it("uses a safe status fallback when a server error has no message", () => {
    const error = new IamApiError(503, {});

    expect(getIamErrorMessage(t, error)).toBe(
      "服务暂时不可用，请稍后重试。",
    );
  });

  it("uses Retry-After when a throttling response has no message", () => {
    const response = {
      headers: { get: () => "45" },
    } as unknown as Response;
    const error = new IamApiError(429, {}, response);

    expect(getIamErrorMessage(t, error)).toBe("请在 45 秒后重试。");
  });

  it("translates nested batch error bodies without exposing unknown text", () => {
    expect(
      getApiErrorBodyMessage(
        t,
        { message: "Insufficient inventory." },
        "Order creation failed.",
      ),
    ).toBe("当前库存不足，请减少数量或更换商品。");
    expect(
      getApiErrorBodyMessage(
        t,
        { message: "internal batch failure" },
        "Order creation failed.",
      ),
    ).toBe("订单创建失败。");
  });
});
