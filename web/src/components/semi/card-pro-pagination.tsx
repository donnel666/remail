import { Pagination } from "@douyinfe/semi-ui";

interface CardProPaginationOptions {
  currentPage: number;
  pageSize: number;
  total: number;
  onPageChange: (page: number) => void;
  onPageSizeChange: (pageSize: number) => void;
  isMobile?: boolean;
  pageSizeOpts?: number[];
  showSizeChanger?: boolean;
  t?: (key: string, options?: Record<string, unknown>) => string;
}

export function createCardProPagination({
  currentPage,
  pageSize,
  total,
  onPageChange,
  onPageSizeChange,
  isMobile = false,
  pageSizeOpts = [10, 20, 50, 100],
  showSizeChanger = true,
  t = (key) => key,
}: CardProPaginationOptions) {
  if (!total || total <= 0) return null;

  const start = (currentPage - 1) * pageSize + 1;
  const end = Math.min(currentPage * pageSize, total);
  const totalText = t("Showing range", { end, start, total });

  return (
    <>
      {!isMobile ? (
        <span
          className="text-sm select-none"
          style={{ color: "var(--semi-color-text-2)" }}
        >
          {totalText}
        </span>
      ) : null}

      <Pagination
        currentPage={currentPage}
        onPageChange={onPageChange}
        onPageSizeChange={onPageSizeChange}
        pageSize={pageSize}
        pageSizeOpts={pageSizeOpts}
        showQuickJumper={isMobile}
        showSizeChanger={showSizeChanger}
        showTotal
        size={isMobile ? "small" : "default"}
        total={total}
      />
    </>
  );
}
