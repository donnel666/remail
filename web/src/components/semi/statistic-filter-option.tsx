import { Tag } from "@douyinfe/semi-ui";

interface StatisticFilterOptionProps<T extends string> {
  active: boolean;
  count: number;
  label: string;
  onSelect: (value: T) => void;
  value: T;
}

export function StatisticFilterOption<T extends string>({
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
