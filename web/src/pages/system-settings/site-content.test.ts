import { describe, expect, it } from "vitest";

import { invalidNumericKeys, parseSettingsList, selectOptions, serializeOptions } from "@/lib/system-settings-api";

describe("parseSettingsList", () => {
  it("accepts arrays and safely ignores invalid setting values", () => {
    expect(parseSettingsList<{ id: number }>('[{"id":1}]')).toEqual([{ id: 1 }]);
    expect(parseSettingsList("{}")).toEqual([]);
    expect(parseSettingsList("broken")).toEqual([]);
  });
});

describe("selectOptions", () => {
  it("keeps only requested server values without creating defaults", () => {
    expect(selectOptions([
      { key: "smtp_task_retry_count", value: "0" },
      { key: "other", value: "9" },
    ], ["smtp_task_retry_count", "smtp_outbound_payload_ttl_minutes"])).toEqual({
      smtp_task_retry_count: "0",
    });

    expect(selectOptions([{ key: "SMTP_TASK_RETRY_COUNT", value: "4" }], ["smtp_task_retry_count"])).toEqual({
      smtp_task_retry_count: "4",
    });
  });
});

describe("serializeOptions", () => {
  it("skips untouched invalid numeric values and includes them after correction", () => {
    const form: Record<string, unknown> = {
      smtp_task_retry_count: "bad",
      smtp_outbound_payload_ttl_minutes: "5",
      inbound_mail_timeout_minutes: "1.5",
    };
    const keys = ["smtp_task_retry_count", "smtp_outbound_payload_ttl_minutes", "inbound_mail_timeout_minutes"];

    expect(invalidNumericKeys(form, keys)).toEqual(["smtp_task_retry_count", "inbound_mail_timeout_minutes"]);
    expect(serializeOptions(keys, form, keys)).toEqual([
      { key: "smtp_outbound_payload_ttl_minutes", value: "5" },
    ]);

    form.smtp_task_retry_count = 3;
    form.inbound_mail_timeout_minutes = 2;
    expect(invalidNumericKeys(form, keys)).toEqual([]);
    expect(serializeOptions(keys, form, keys)).toEqual([
      { key: "smtp_task_retry_count", value: "3" },
      { key: "smtp_outbound_payload_ttl_minutes", value: "5" },
      { key: "inbound_mail_timeout_minutes", value: "2" },
    ]);
  });
});
