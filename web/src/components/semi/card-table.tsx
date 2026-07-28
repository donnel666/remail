import type { ComponentProps, Key, ReactNode } from "react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  Button,
  Card,
  Checkbox,
  Collapsible,
  Empty,
  Pagination,
  Skeleton,
  Table,
} from "@douyinfe/semi-ui";
import { IconChevronDown, IconChevronUp } from "@douyinfe/semi-icons";

import { useIsMobile } from "@/hooks/use-is-mobile";

type RowData = object;
type TableColumn<T extends RowData> = {
  title?: ReactNode;
  key?: string;
  dataIndex?: string;
  render?: (value: unknown, record: T, index: number) => ReactNode;
  [key: string]: unknown;
};

type CardTableRowSelection<T extends RowData> = {
  disabled?: boolean;
  getCheckboxProps?: (
    record: T
  ) => Omit<
    ComponentProps<typeof Checkbox>,
    "checked" | "defaultChecked" | "indeterminate" | "onChange"
  >;
  hidden?: boolean;
  onChange?: (
    selectedRowKeys: Array<string | number>,
    selectedRows?: T[]
  ) => void;
  selectedRowKeys?: Array<string | number>;
};

export const DESKTOP_TABLE_SCROLL_Y = "100%";

interface CardTableProps<T extends RowData> {
  columns: TableColumn<T>[];
  dataSource?: T[];
  loading?: boolean;
  rowKey?: keyof T | string | ((record: T, index?: number) => Key);
  hidePagination?: boolean;
  empty?: ReactNode;
  pagination?: Record<string, unknown> | false;
  rowSelection?: CardTableRowSelection<T> | boolean;
  expandedRowRender?: (record: T, index: number) => ReactNode;
  rowExpandable?: (record: T) => boolean;
  visibleColumns?: Record<string, boolean>;
  [key: string]: unknown;
}

function useMinimumLoadingTime(loading: boolean, delay = 300) {
  const [showSkeleton, setShowSkeleton] = useState(loading);

  useEffect(() => {
    if (loading) {
      setShowSkeleton(true);
      return undefined;
    }

    const timer = window.setTimeout(() => setShowSkeleton(false), delay);
    return () => window.clearTimeout(timer);
  }, [delay, loading]);

  return showSkeleton;
}

export function CardTable<T extends RowData>({
  columns,
  dataSource = [],
  loading = false,
  rowKey = "key",
  hidePagination = false,
  ...tableProps
}: CardTableProps<T>) {
  const isMobile = useIsMobile();
  const { t } = useTranslation();
  const showSkeleton = useMinimumLoadingTime(loading);
  const mobileRowSelection =
    typeof tableProps.rowSelection === "object"
      ? tableProps.rowSelection
      : undefined;

  const getRowKey = (record: T, index: number): Key => {
    if (typeof rowKey === "function") return rowKey(record, index);
    const value = (record as Record<string, unknown>)[String(rowKey)];
    return typeof value === "string" || typeof value === "number"
      ? value
      : index;
  };

  const getSelectionKey = (record: T, index: number): string | number => {
    const key = getRowKey(record, index);
    return typeof key === "string" || typeof key === "number"
      ? key
      : String(key);
  };

  if (!isMobile) {
    const finalTableProps: Record<string, unknown> = hidePagination
      ? { ...tableProps, pagination: false }
      : tableProps;
    const incomingClassName =
      typeof finalTableProps.className === "string"
        ? finalTableProps.className
        : "";
    const tableClassName = ["remail-stable-table", incomingClassName]
      .filter(Boolean)
      .join(" ");
    const desktopTableProps = {
      ...finalTableProps,
      className: tableClassName,
      tableLayout: finalTableProps.tableLayout ?? "fixed",
    };

    return (
      <Table
        columns={columns as never}
        dataSource={dataSource}
        loading={loading}
        rowKey={rowKey as never}
        {...desktopTableProps}
      />
    );
  }

  const visibleColumns = columns.filter((column) => {
    if (tableProps.visibleColumns && column.key) {
      return tableProps.visibleColumns[column.key];
    }
    return true;
  });

  if (showSkeleton) {
    return (
      <div className="flex flex-col gap-2">
        {[1, 2, 3].map((item) => (
          <Card className="!rounded-2xl shadow-sm" key={item}>
            <Skeleton
              active
              loading
              placeholder={
                <div className="p-2">
                  {visibleColumns.map((column, index) => {
                    if (!column.title) {
                      return (
                        <div className="mt-2 flex justify-end" key={index}>
                          <Skeleton.Title style={{ height: 24, width: 100 }} />
                        </div>
                      );
                    }

                    return (
                      <div
                        className="flex items-center justify-between border-b border-dashed py-1 last:border-b-0"
                        key={index}
                        style={{ borderColor: "var(--semi-color-border)" }}
                      >
                        <Skeleton.Title style={{ height: 14, width: 80 }} />
                        <Skeleton.Title
                          style={{
                            height: 14,
                            maxWidth: 180,
                            width: `${50 + (index % 3) * 10}%`,
                          }}
                        />
                      </div>
                    );
                  })}
                </div>
              }
            />
          </Card>
        ))}
      </div>
    );
  }

  if (!dataSource.length) {
    if (tableProps.empty) return tableProps.empty;
    return (
      <div className="flex justify-center p-4">
        <Empty description="No Data" />
      </div>
    );
  }

  const MobileRowCard = ({
    index,
    record,
  }: {
    index: number;
    record: T;
  }) => {
    const [showDetails, setShowDetails] = useState(false);
    const hasDetails =
      tableProps.expandedRowRender &&
      (!tableProps.rowExpandable || tableProps.rowExpandable(record));
    const selectionKey = getSelectionKey(record, index);
    const selectedRowKeys = mobileRowSelection?.selectedRowKeys ?? [];
    const checkboxProps =
      mobileRowSelection?.getCheckboxProps?.(record) ?? {};
    const selected = selectedRowKeys.includes(selectionKey);

    const changeSelection = (checked: boolean) => {
      if (!mobileRowSelection?.onChange) return;
      const nextKeys = checked
        ? selected
          ? selectedRowKeys
          : [...selectedRowKeys, selectionKey]
        : selectedRowKeys.filter((key) => key !== selectionKey);
      const nextKeySet = new Set(nextKeys);
      mobileRowSelection.onChange(
        nextKeys,
        dataSource.filter((item, itemIndex) =>
          nextKeySet.has(getSelectionKey(item, itemIndex))
        )
      );
    };

    return (
      <Card className="!rounded-2xl shadow-sm">
        {mobileRowSelection && !mobileRowSelection.hidden ? (
          <div className="flex justify-end">
            <Checkbox
              {...checkboxProps}
              aria-label={
                checkboxProps["aria-label"] ??
                `${t("Select row")}: ${String(selectionKey)}`
              }
              checked={selected}
              className={[
                "inline-flex min-h-11 min-w-11 items-center justify-center",
                checkboxProps.className,
              ]
                .filter(Boolean)
                .join(" ")}
              disabled={mobileRowSelection.disabled || checkboxProps.disabled}
              onChange={(event) => changeSelection(Boolean(event.target.checked))}
            />
          </div>
        ) : null}
        {visibleColumns.map((column, columnIndex) => {
          const value =
            typeof column.dataIndex === "string"
              ? (record as Record<string, unknown>)[column.dataIndex]
              : undefined;
          const cellContent = column.render
            ? column.render(value, record, index)
            : value;

          if (!column.title) {
            return (
              <div
                className="mt-2 flex justify-end"
                key={column.key || columnIndex}
              >
                {cellContent as ReactNode}
              </div>
            );
          }

          return (
            <div
              className="flex items-start justify-between border-b border-dashed py-1 last:border-b-0"
              key={column.key || columnIndex}
              style={{ borderColor: "var(--semi-color-border)" }}
            >
              <span className="mr-2 whitespace-nowrap font-medium text-gray-600 select-none">
                {column.title}
              </span>
              <div className="flex flex-1 items-center justify-end gap-1 break-all">
                {cellContent !== undefined && cellContent !== null
                  ? (cellContent as ReactNode)
                  : "-"}
              </div>
            </div>
          );
        })}

        {hasDetails ? (
          <>
            <Button
              className="mt-2 flex w-full justify-center"
              icon={showDetails ? <IconChevronUp /> : <IconChevronDown />}
              onClick={(event) => {
                event.stopPropagation();
                setShowDetails((value) => !value);
              }}
              size="small"
              theme="borderless"
            >
              {showDetails ? t("Collapse") : t("Details")}
            </Button>
            <Collapsible isOpen={showDetails} keepDOM>
              <div className="pt-2">
                {tableProps.expandedRowRender?.(record, index)}
              </div>
            </Collapsible>
          </>
        ) : null}
      </Card>
    );
  };

  return (
    <div className="flex flex-col gap-2">
      {dataSource.map((record, index) => (
        <MobileRowCard
          index={index}
          key={getRowKey(record, index)}
          record={record}
        />
      ))}
      {!hidePagination && tableProps.pagination && dataSource.length > 0 ? (
        <div className="mt-2 flex justify-center">
          <Pagination {...(tableProps.pagination as Record<string, unknown>)} />
        </div>
      ) : null}
    </div>
  );
}
