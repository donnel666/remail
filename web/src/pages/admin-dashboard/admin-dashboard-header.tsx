import { useEffect, useState } from "react";

import { Button, DatePicker, Popover } from "@douyinfe/semi-ui";
import { RefreshCw, Search } from "lucide-react";

import { useIsMobile } from "@/hooks/use-is-mobile";
import {
  DATE_RANGE_DROPDOWN_CLASS,
  type DateRangeValue,
} from "@/pages/resources/date-range-filter";

export function AdminDashboardHeader({
  dateRangePresets,
  displayName,
  greeting,
  loading,
  onDateRangeChange,
  onRefresh,
  range,
  t,
}: {
  dateRangePresets: Array<{
    end: () => Date;
    start: () => Date;
    text: string;
  }>;
  displayName: string;
  greeting: string;
  loading: boolean;
  onDateRangeChange: (value?: Date | Date[] | string | string[]) => void;
  onRefresh: () => void;
  range: DateRangeValue;
  t: (key: string) => string;
}) {
  const isMobile = useIsMobile();
  const [visible, setVisible] = useState(false);

  useEffect(() => {
    const timer = window.setTimeout(() => setVisible(true), 100);
    return () => window.clearTimeout(timer);
  }, []);

  return (
    <div className="mb-4 flex items-center justify-between">
      <h2
        className="text-2xl font-semibold text-[var(--semi-color-text-0)] transition-opacity duration-1000 ease-in-out"
        style={{ opacity: visible ? 1 : 0 }}
      >
        👋{greeting}，{displayName}
      </h2>
      <div className="flex gap-3">
        <Popover
          content={
            <div className="p-2">
              <DatePicker
                dropdownClassName={DATE_RANGE_DROPDOWN_CLASS}
                format="yyyy-MM-dd HH:mm"
                onChange={onDateRangeChange}
                placeholder={[t("Start time"), t("End time")]}
                presetPosition="bottom"
                presets={dateRangePresets}
                showClear={false}
                size="small"
                style={{
                  maxWidth: 420,
                  width: isMobile ? "calc(100vw - 48px)" : 420,
                }}
                type="dateTimeRange"
                value={range}
              />
            </div>
          }
          position="bottomRight"
          trigger="click"
        >
          <Button
            aria-label={t("Change date")}
            className="!rounded-full"
            icon={<Search size={16} />}
            theme="borderless"
            type="tertiary"
          />
        </Popover>
        <Button
          aria-label={t("Refresh")}
          className="!rounded-full"
          icon={<RefreshCw size={16} />}
          loading={loading}
          onClick={onRefresh}
          theme="borderless"
          type="tertiary"
        />
      </div>
    </div>
  );
}
