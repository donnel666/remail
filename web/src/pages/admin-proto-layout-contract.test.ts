// @ts-expect-error -- This source-contract test runs in Node, not the browser.
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { PROTO_EMAIL_SUFFIXES } from "./resources/proto-model";

const protoSource = readFileSync(new URL("./AdminProtoEmails.tsx", import.meta.url), "utf8");
const icloudSource = readFileSync(new URL("./AdminICloudEmails.tsx", import.meta.url), "utf8");
const protoMetaSource = readFileSync(new URL("./admin-proto/proto-meta.tsx", import.meta.url), "utf8");

function toolbar(source: string) {
  return source.slice(source.indexOf("  const actionsArea = ("), source.indexOf("  const paginationArea ="));
}

describe("admin Proto mailbox toolbar", () => {
  it("matches the admin Apple toolbar controls while retaining owner filtering", () => {
    const protoToolbar = toolbar(protoSource);
    const icloudToolbar = toolbar(icloudSource);
    for (const fragment of [
      'className="flex w-full flex-col items-center justify-between gap-2 md:flex-row"',
      'className="order-2 flex w-full flex-wrap gap-2 md:order-1 md:w-auto"',
      'className="order-1 flex w-full flex-col items-center gap-2 md:order-2 md:w-auto md:flex-row"',
      'className="resources-search-input w-full md:w-56"',
      'className="remail-toolbar-fixed-button flex-1 md:flex-none"',
      'icon={<Upload size={14} />}',
      'style={{ width: isMobile ? "100%" : 224 }}',
      'style={{ width: isMobile ? "100%" : 380 }}',
      "<CompactModeToggle",
    ]) {
      expect(icloudToolbar).toContain(fragment);
      expect(protoToolbar).toContain(fragment);
    }
    for (const component of ["Input", "DatePicker"]) {
      for (const source of [protoToolbar, icloudToolbar]) {
        const control = source.match(new RegExp(`<${component}\\b[\\s\\S]*?\\n        />`))?.[0];
        expect(control).toContain('size="small"');
        expect(control).toContain("showClear");
        if (component === "Input") expect(control).toContain("onEnterPress=");
      }
    }
    const ownerSelect = protoToolbar.match(/<AdminUserSelect[\s\S]*?\/>/)?.[0];
    expect(ownerSelect).toBeDefined();
    expect(ownerSelect).toContain('size="small"');
    expect(ownerSelect).toContain('style={{ width: isMobile ? "100%" : 224 }}');
    expect(ownerSelect).toContain("value={ownerFilter}");
    expect(protoToolbar).toContain('placeholder={t("Search email, owner or ID")}');
    expect(protoSource).toContain("filter.ownerId = ownerFilter");
    expect(protoSource).toContain("owners={importOwners}");
    expect(protoSource).not.toContain('from "@/lib/admin-microsoft-api"');
    expect(protoSource).not.toContain('from "@/lib/admin-icloud-api"');
  });

  it("omits the redundant suffix column and grouping without changing other filters", () => {
    for (const fragment of [
      'dataIndex: "suffix"',
      'title: t("Suffix")',
      "activeSuffix",
      "suffixCounts",
      "tabsArea=",
      "<Tabs",
    ]) {
      expect(protoSource).not.toContain(fragment);
    }
    expect(protoSource).toContain('scroll={{ x: "max(100%, 1310px)"');
    for (const field of ["ownerId", "suffix", "search", "status", "forSale", "longLived", "createdFrom", "createdTo"]) {
      expect(protoSource).toContain(`filter.${field} =`);
    }
  });

  it("keeps all three suffix choices inside the existing filters menu", () => {
    expect(PROTO_EMAIL_SUFFIXES).toEqual(["protonmail.com", "proton.me"]);
    const protoToolbar = toolbar(protoSource);
    const filtersMenu = protoToolbar.match(/<Dropdown\b[\s\S]*?<\/Dropdown>/)?.[0];
    expect(filtersMenu).toBeDefined();
    expect(protoToolbar).not.toMatch(/<Select\b/);
    for (const fragment of [
      '{t("Suffix")}',
      '(["all", ...PROTO_EMAIL_SUFFIXES] as const).map',
      "<StatisticFilterOption",
      'active={(suffixFilter ?? "all") === value}',
      'label={value === "all" ? t("All") : `@${value}`}',
      "stats.suffixes.reduce((sum, item) => sum + item.count, 0)",
      "stats.suffixes.find((item) => item.key === value)?.count ?? 0",
      'setSuffixFilter(next === "all" ? undefined : next)',
      "resetPageAndSelection();",
    ]) {
      expect(filtersMenu).toContain(fragment);
    }
  });

  it("carries the suffix through the shared list and bulk filter and clears it on reset", () => {
    const listFilter = protoSource.slice(protoSource.indexOf("  const listFilter ="), protoSource.indexOf("  const listFilterKey ="));
    expect(listFilter).toContain("if (suffixFilter) filter.suffix = suffixFilter;");
    expect(listFilter).toMatch(/\}, \[[\s\S]*?suffixFilter,/);
    expect(protoSource).toContain("const listFilterKey = JSON.stringify(listFilter)");
    expect(protoSource).toContain("Number(Boolean(suffixFilter))");
    const reset = protoSource.slice(protoSource.indexOf("  const resetFilters ="), protoSource.indexOf("  const runRowOperation ="));
    expect(reset).toContain("setSuffixFilter(undefined)");
    expect(reset).toContain("setActivePage(1)");
    expect(reset).toContain("setSelectedKeys([])");
    expect(protoSource).toContain("listAdminProtoResources(\n        listFilter,");
    expect(protoSource).toContain("filter: { ...listFilter }");
    expect(protoSource).toContain("deleteAdminProtoResourcesByFilter(listFilter)");
    expect(protoSource).toContain("setAdminProtoResourcesForSaleByFilter(\n              listFilter,");
  });

  it("displays stopped validations as failures and exposes the same status filter", () => {
    expect(protoMetaSource).toContain('validation_failed: { color: "red", label: "Validation failed" }');
    expect(protoSource).toContain("validation_failed: 0");
    expect(toolbar(protoSource)).toContain('"validation_failed"');
  });
});
