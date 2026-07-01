import { useCallback, useEffect, useMemo, useState } from "react";
import {
  Button,
  Dropdown,
  Empty,
  Input,
  Modal,
  Space,
  Tabs,
  Tag,
  Toast,
  Typography,
} from "@douyinfe/semi-ui";
import { IconSearch } from "@douyinfe/semi-icons";
import {
  IllustrationNoResult,
  IllustrationNoResultDark,
} from "@douyinfe/semi-illustrations";
import { Layers, SlidersHorizontal } from "lucide-react";
import { useTranslation } from "react-i18next";

import { CardPro } from "@/components/semi/card-pro";
import { createCardProPagination } from "@/components/semi/card-pro-pagination";
import { CardTable } from "@/components/semi/card-table";
import { CompactModeToggle } from "@/components/semi/compact-mode-toggle";
import { useIsMobile } from "@/hooks/use-is-mobile";

import { ImportMicrosoftEmailsModal } from "./resources/import-microsoft-emails-modal";
import {
  getSuffix,
  getSuffixCounts,
  isAvailable,
  MICROSOFT_EMAIL_RESOURCES_MOCK,
  type EmailResource,
  type LifetimeType,
  type ResourceStatus,
  type UsageScope,
} from "./resources/model";
import { renderStatusTag } from "./resources/resource-status-tag";
import { useSelectionNotification } from "./resources/use-selection-notification";

const { Text } = Typography;

type StatusFilter = "all" | "available" | "pending_validation" | "disabled";
type BooleanFilter = "all" | "yes" | "no";

function isDisabledStatus(status: ResourceStatus) {
  return !isAvailable(status) && status !== "pending_validation";
}

function matchesStatusFilter(status: ResourceStatus, filter: StatusFilter) {
  if (filter === "all") return true;
  if (filter === "available") return isAvailable(status);
  if (filter === "pending_validation") return status === "pending_validation";
  return isDisabledStatus(status);
}

function matchesBooleanFilter(value: boolean, filter: BooleanFilter) {
  if (filter === "all") return true;
  return filter === "yes" ? value : !value;
}

interface StatisticFilterOptionProps<T extends string> {
  active: boolean;
  count: number;
  label: string;
  onSelect: (value: T) => void;
  value: T;
}

function StatisticFilterOption<T extends string>({
  active,
  count,
  label,
  onSelect,
  value,
}: StatisticFilterOptionProps<T>) {
  return (
    <button
      className={`flex w-full items-center justify-between rounded-[10px] px-2 py-1.5 text-left text-sm transition-colors ${
        active
          ? "bg-[var(--semi-color-primary-light-default)] text-[var(--semi-color-primary)]"
          : "text-[var(--semi-color-text-1)] hover:bg-[var(--semi-color-fill-0)]"
      }`}
      onClick={() => onSelect(value)}
      type="button"
    >
      <span>{label}</span>
      <Tag color={active ? "orange" : "grey"} shape="circle" size="small">
        {count}
      </Tag>
    </button>
  );
}

function useResources() {
  const [items, setItems] = useState<EmailResource[]>([]);
  const [loading, setLoading] = useState(true);

  const refresh = useCallback(async () => {
    setLoading(true);
    await new Promise((resolve) => setTimeout(resolve, 250));
    setItems([...MICROSOFT_EMAIL_RESOURCES_MOCK]);
    setLoading(false);
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  return { items, loading, refresh };
}

export default function Resources() {
  const { t } = useTranslation();
  const isMobile = useIsMobile();
  const { items, loading, refresh } = useResources();
  const [activeSuffix, setActiveSuffix] = useState("all");
  const [searchKeyword, setSearchKeyword] = useState("");
  const [suffixKeyword, setSuffixKeyword] = useState("");
  const [statusFilter, setStatusFilter] = useState<StatusFilter>("all");
  const [privateFilter, setPrivateFilter] = useState<BooleanFilter>("all");
  const [longLivedFilter, setLongLivedFilter] =
    useState<BooleanFilter>("all");
  const [compactMode, setCompactMode] = useState(false);
  const [importOpen, setImportOpen] = useState(false);
  const [selectedKeys, setSelectedKeys] = useState<number[]>([]);
  const [activePage, setActivePage] = useState(1);
  const [pageSize, setPageSize] = useState(10);

  const suffixCounts = useMemo(() => getSuffixCounts(items), [items]);
  const suffixSet = useMemo(
    () => new Set(suffixCounts.map(([suffix]) => suffix)),
    [suffixCounts]
  );

  const resourceStats = useMemo(
    () => ({
      longLived: {
        all: items.length,
        no: items.filter((item) => item.lifetimeType !== "long_lived").length,
        yes: items.filter((item) => item.lifetimeType === "long_lived").length,
      },
      private: {
        all: items.length,
        no: items.filter((item) => item.usageScope !== "private").length,
        yes: items.filter((item) => item.usageScope === "private").length,
      },
      status: {
        all: items.length,
        available: items.filter((item) => isAvailable(item.status)).length,
        disabled: items.filter((item) => isDisabledStatus(item.status)).length,
        pending_validation: items.filter(
          (item) => item.status === "pending_validation"
        ).length,
      },
    }),
    [items]
  );

  const activeStatisticFilterCount =
    Number(statusFilter !== "all") +
    Number(privateFilter !== "all") +
    Number(longLivedFilter !== "all");

  useEffect(() => {
    if (activeSuffix !== "all" && !suffixSet.has(activeSuffix)) {
      setActiveSuffix("all");
    }
  }, [activeSuffix, suffixSet]);

  const filteredItems = useMemo(() => {
    const keyword = searchKeyword.trim().toLowerCase();
    const suffixKeywordValue = suffixKeyword.trim().toLowerCase();
    return items.filter((item) => {
      const suffix = getSuffix(item.emailAddress);
      const suffixMatched = activeSuffix === "all" || suffix === activeSuffix;
      const suffixKeywordMatched =
        suffixKeywordValue.length === 0 || suffix.includes(suffixKeywordValue);
      const statusMatched = matchesStatusFilter(item.status, statusFilter);
      const privateMatched = matchesBooleanFilter(
        item.usageScope === "private",
        privateFilter
      );
      const longLivedMatched = matchesBooleanFilter(
        item.lifetimeType === "long_lived",
        longLivedFilter
      );
      const keywordMatched =
        keyword.length === 0 ||
        item.emailAddress.toLowerCase().includes(keyword) ||
        item.emailType.toLowerCase().includes(keyword) ||
        suffix.includes(keyword);

      return (
        suffixMatched &&
        suffixKeywordMatched &&
        statusMatched &&
        privateMatched &&
        longLivedMatched &&
        keywordMatched
      );
    });
  }, [
    activeSuffix,
    items,
    longLivedFilter,
    privateFilter,
    searchKeyword,
    statusFilter,
    suffixKeyword,
  ]);

  const totalPages = Math.max(1, Math.ceil(filteredItems.length / pageSize));
  const safePage = Math.min(activePage, totalPages);
  const pagedItems = filteredItems.slice(
    (safePage - 1) * pageSize,
    safePage * pageSize
  );

  useEffect(() => {
    if (safePage !== activePage) setActivePage(safePage);
  }, [activePage, safePage]);

  useSelectionNotification({
    selectedCount: selectedKeys.length,
    onClear: () => setSelectedKeys([]),
    t,
  });

  const selectSuffix = (suffix: string) => {
    setActiveSuffix(suffix);
    setActivePage(1);
    setSelectedKeys([]);
  };

  const resetFilters = () => {
    setSearchKeyword("");
    setSuffixKeyword("");
    setStatusFilter("all");
    setPrivateFilter("all");
    setLongLivedFilter("all");
    setActiveSuffix("all");
    setActivePage(1);
    setSelectedKeys([]);
  };

  const applyStatusFilter = (value: StatusFilter) => {
    setStatusFilter(value);
    setActivePage(1);
    setSelectedKeys([]);
  };

  const applyPrivateFilter = (value: BooleanFilter) => {
    setPrivateFilter(value);
    setActivePage(1);
    setSelectedKeys([]);
  };

  const applyLongLivedFilter = (value: BooleanFilter) => {
    setLongLivedFilter(value);
    setActivePage(1);
    setSelectedKeys([]);
  };

  const confirmCheckAll = () => {
    Modal.confirm({
      title: t("Confirm check all"),
      content: t("Confirm check all content", { count: filteredItems.length }),
      okText: t("Check all"),
      cancelText: t("Cancel"),
      onOk: () => {
        Toast.success(t("Check all submitted"));
      },
    });
  };

  const confirmSellAll = () => {
    const sellableCount = filteredItems.filter(
      (item) => item.usageScope === "private"
    ).length;

    Modal.confirm({
      title: t("Confirm sell all"),
      content: t("Confirm sell all content", { count: sellableCount }),
      okText: t("Sell all"),
      cancelText: t("Cancel"),
      onOk: () => {
        Toast.success(t("Sell all submitted"));
      },
    });
  };

  const columns = useMemo(
    () =>
      [
        {
          title: t("Suffix"),
          dataIndex: "emailAddress",
          width: 120,
          render: (_: string, record: EmailResource) => (
            <Tag color="white" shape="circle">
              {getSuffix(record.emailAddress)}
            </Tag>
          ),
        },
        {
          title: t("Email"),
          dataIndex: "emailAddress",
          width: 260,
          render: (text: string) => (
            <Text
              copyable={{
                content: text,
                onCopy: () => Toast.success(t("Copied")),
              }}
            >
              {text}
            </Text>
          ),
        },
        {
          title: t("Status"),
          dataIndex: "status",
          width: 120,
          render: (status: ResourceStatus, record: EmailResource) =>
            renderStatusTag(status, t, record.validationFailureReason),
        },
        {
          title: t("Private"),
          dataIndex: "usageScope",
          width: 120,
          render: (scope: UsageScope) => (
            <Tag color={scope === "private" ? "green" : "grey"} shape="circle">
              {scope === "private" ? t("Yes") : t("No")}
            </Tag>
          ),
        },
        {
          title: t("Long-lived"),
          dataIndex: "lifetimeType",
          width: 110,
          render: (value: LifetimeType) => (
            <Tag
              color={value === "long_lived" ? "green" : "grey"}
              shape="circle"
            >
              {value === "long_lived" ? t("Yes") : t("No")}
            </Tag>
          ),
        },
        {
          title: t("Action"),
          dataIndex: "operate",
          width: 270,
          fixed: "right",
          render: (_: unknown, record: EmailResource) => (
            <Space wrap={false}>
              {isAvailable(record.status) || record.status === "pending_validation" ? (
                <Button type="danger" size="small">
                  {t("Disable")}
                </Button>
              ) : (
                <Button size="small">{t("Enable")}</Button>
              )}
              <Button type="tertiary" size="small">
                {t("Check")}
              </Button>
              <Button
                disabled={record.usageScope !== "private"}
                type="tertiary"
                size="small"
              >
                {t("Sell")}
              </Button>
              <Button
                type="danger"
                size="small"
                onClick={() => {
                  Modal.confirm({
                    title: t("Confirm remove"),
                    content: record.emailAddress,
                    okText: t("Remove"),
                    cancelText: t("Cancel"),
                  });
                }}
              >
                {t("Remove")}
              </Button>
            </Space>
          ),
        },
      ] as any[],
    [t]
  );

  const rowSelection = {
    selectedRowKeys: selectedKeys,
    onChange: (keys: Array<string | number>) => {
      setSelectedKeys(keys.map((key) => Number(key)));
    },
  };

  const tableColumns = useMemo(() => {
    if (!compactMode) return columns;
    return columns.map((column) => {
      if (column.dataIndex !== "operate") return column;
      const { fixed: _fixed, ...rest } = column;
      return rest;
    });
  }, [columns, compactMode]);

  const tabsArea = (
    <Tabs
      activeKey={activeSuffix}
      type="card"
      collapsible
      onChange={(key) => selectSuffix(String(key))}
      className="mb-2"
    >
      <Tabs.TabPane
        itemKey="all"
        tab={
          <span className="flex items-center gap-2">
            {t("All")}
            <Tag color={activeSuffix === "all" ? "red" : "grey"} shape="circle">
              {items.length}
            </Tag>
          </span>
        }
      />
      {suffixCounts.map(([suffix, count]) => (
        <Tabs.TabPane
          key={suffix}
          itemKey={suffix}
          tab={
            <span className="flex items-center gap-2">
              <Layers size={14} />
              {suffix}
              <Tag
                color={activeSuffix === suffix ? "red" : "grey"}
                shape="circle"
              >
                {count}
              </Tag>
            </span>
          }
        />
      ))}
    </Tabs>
  );

  const actionsArea = (
    <div className="flex w-full flex-col items-center justify-between gap-2 md:flex-row">
      <div className="order-2 flex w-full flex-wrap gap-2 md:order-1 md:w-auto">
        <Button
          type="primary"
          size="small"
          className="flex-1 md:flex-initial"
          onClick={() => setImportOpen(true)}
        >
          {t("Import")}
        </Button>
        <Button
          type="tertiary"
          size="small"
          className="flex-1 md:flex-initial"
          loading={loading}
          onClick={refresh}
        >
          {t("Refresh")}
        </Button>
        <Button
          type="tertiary"
          size="small"
          className="flex-1 md:flex-initial"
          onClick={confirmCheckAll}
        >
          {t("Check all")}
        </Button>
        <Button
          type="tertiary"
          size="small"
          className="flex-1 md:flex-initial"
          onClick={confirmSellAll}
        >
          {t("Sell all")}
        </Button>
        <CompactModeToggle
          compactMode={compactMode}
          setCompactMode={setCompactMode}
          t={t}
        />
      </div>

      <div className="order-1 flex w-full flex-col items-center gap-2 md:order-2 md:w-auto md:flex-row">
        <Dropdown
          position="bottomRight"
          trigger="click"
          render={
            <div className="w-[280px] p-2">
              <div className="px-2 pb-1 text-xs font-medium text-[var(--semi-color-text-2)]">
                {t("Status")}
              </div>
              <div className="mb-2 space-y-1">
                <StatisticFilterOption
                  active={statusFilter === "all"}
                  count={resourceStats.status.all}
                  label={t("All")}
                  onSelect={applyStatusFilter}
                  value="all"
                />
                <StatisticFilterOption
                  active={statusFilter === "available"}
                  count={resourceStats.status.available}
                  label={t("Available")}
                  onSelect={applyStatusFilter}
                  value="available"
                />
                <StatisticFilterOption
                  active={statusFilter === "pending_validation"}
                  count={resourceStats.status.pending_validation}
                  label={t("Pending validation")}
                  onSelect={applyStatusFilter}
                  value="pending_validation"
                />
                <StatisticFilterOption
                  active={statusFilter === "disabled"}
                  count={resourceStats.status.disabled}
                  label={t("Disabled")}
                  onSelect={applyStatusFilter}
                  value="disabled"
                />
              </div>

              <div className="px-2 pb-1 text-xs font-medium text-[var(--semi-color-text-2)]">
                {t("Private")}
              </div>
              <div className="mb-2 space-y-1">
                <StatisticFilterOption
                  active={privateFilter === "all"}
                  count={resourceStats.private.all}
                  label={t("All")}
                  onSelect={applyPrivateFilter}
                  value="all"
                />
                <StatisticFilterOption
                  active={privateFilter === "yes"}
                  count={resourceStats.private.yes}
                  label={t("Yes")}
                  onSelect={applyPrivateFilter}
                  value="yes"
                />
                <StatisticFilterOption
                  active={privateFilter === "no"}
                  count={resourceStats.private.no}
                  label={t("No")}
                  onSelect={applyPrivateFilter}
                  value="no"
                />
              </div>

              <div className="px-2 pb-1 text-xs font-medium text-[var(--semi-color-text-2)]">
                {t("Long-lived")}
              </div>
              <div className="space-y-1">
                <StatisticFilterOption
                  active={longLivedFilter === "all"}
                  count={resourceStats.longLived.all}
                  label={t("All")}
                  onSelect={applyLongLivedFilter}
                  value="all"
                />
                <StatisticFilterOption
                  active={longLivedFilter === "yes"}
                  count={resourceStats.longLived.yes}
                  label={t("Yes")}
                  onSelect={applyLongLivedFilter}
                  value="yes"
                />
                <StatisticFilterOption
                  active={longLivedFilter === "no"}
                  count={resourceStats.longLived.no}
                  label={t("No")}
                  onSelect={applyLongLivedFilter}
                  value="no"
                />
              </div>
            </div>
          }
        >
          <Button
            className="flex-1 md:flex-initial"
            icon={<SlidersHorizontal size={14} />}
            size="small"
            type="tertiary"
          >
            {activeStatisticFilterCount > 0
              ? `${t("Filters")} (${activeStatisticFilterCount})`
              : t("Filters")}
          </Button>
        </Dropdown>
        <Input
          prefix={<IconSearch />}
          placeholder={t("Search email")}
          showClear
          size="small"
          value={searchKeyword}
          onChange={(value) => setSearchKeyword(String(value))}
          className="resources-search-input w-full md:w-56"
        />
        <Input
          prefix={<IconSearch />}
          placeholder={t("Search suffix")}
          showClear
          size="small"
          value={suffixKeyword}
          onChange={(value) => setSuffixKeyword(String(value))}
          className="resources-search-input w-full md:w-56"
        />
        <div className="flex w-full gap-2 md:w-auto">
          <Button
            type="tertiary"
            size="small"
            loading={loading}
            className="flex-1 md:flex-initial"
            onClick={() => setActivePage(1)}
          >
            {t("Query")}
          </Button>
          <Button
            type="tertiary"
            size="small"
            className="flex-1 md:flex-initial"
            onClick={resetFilters}
          >
            {t("Reset")}
          </Button>
        </div>
      </div>
    </div>
  );

  const paginationArea = createCardProPagination({
    currentPage: safePage,
    isMobile,
    onPageChange: (page) => setActivePage(page),
    onPageSizeChange: (size) => {
      setPageSize(size);
      setActivePage(1);
    },
    pageSize,
    total: filteredItems.length,
    t,
  });

  return (
    <div className="px-2 pt-5">
      <CardPro
        type="type3"
        tabsArea={tabsArea}
        actionsArea={actionsArea}
        paginationArea={paginationArea}
        t={t}
      >
        <CardTable
          columns={tableColumns}
          dataSource={pagedItems}
          empty={
            <Empty
              darkModeImage={
                <IllustrationNoResultDark style={{ height: 150, width: 150 }} />
              }
              description={t("No email resources yet")}
              image={<IllustrationNoResult style={{ height: 150, width: 150 }} />}
              style={{ padding: 30 }}
            />
          }
          hidePagination
          loading={loading}
          pagination={false}
          className="overflow-hidden rounded-xl"
          rowKey="id"
          rowSelection={rowSelection}
          scroll={compactMode ? undefined : { x: "max-content" }}
          size="middle"
        />
      </CardPro>

      <ImportMicrosoftEmailsModal
        open={importOpen}
        onOpenChange={setImportOpen}
        onSuccess={refresh}
      />
    </div>
  );
}
