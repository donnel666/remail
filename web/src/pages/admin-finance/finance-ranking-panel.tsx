import { Card } from "@douyinfe/semi-ui";
import { Package, ShoppingBag } from "lucide-react";
import { useTranslation } from "react-i18next";

import type { FinanceHotItem } from "./admin-finance-api";
import { formatMoney } from "./finance-meta";

const MEDALS = ["🥇", "🥈", "🥉"] as const;

function RankMark({ rank }: { rank: number }) {
  return (
    <div className="flex h-8 w-8 items-center justify-center text-2xl">
      {MEDALS[rank - 1] ?? (
        <span className="text-base font-semibold text-[var(--semi-color-text-2)]">#{rank}</span>
      )}
    </div>
  );
}

function RankingColumn({ items, offset }: { items: FinanceHotItem[]; offset: number }) {
  return items.map((item, index) => {
    const rank = offset + index + 1;

    return (
      <div
        className="grid grid-cols-4 items-center py-2 transition-colors hover:bg-[var(--semi-color-fill-0)]"
        key={`${rank}-${item.name}`}
      >
        <div className="flex items-center justify-center">
          <RankMark rank={rank} />
        </div>
        <div className="truncate font-medium text-[var(--semi-color-text-1)]">{item.name}</div>
        <div className="whitespace-nowrap text-sm text-[var(--semi-color-text-2)]">
          ¥{formatMoney(item.amount)}
        </div>
        <div />
      </div>
    );
  });
}

export function FinanceRankingPanel({
  items,
  kind,
  loading,
  title,
}: {
  items: FinanceHotItem[];
  kind: "product" | "project";
  loading: boolean;
  title: string;
}) {
  const { t } = useTranslation();
  const top = items.slice(0, 10);
  const columns = [top.slice(0, 5), top.slice(5, 10)];
  const TitleIcon = kind === "project" ? Package : ShoppingBag;

  return (
    <Card
      bodyStyle={{ padding: 10 }}
      bordered
      className="console-dashboard-ranking-card shadow-sm !rounded-2xl"
      headerLine
      title={
        <div className="flex items-center gap-2">
          <TitleIcon size={18} />
          {title}
        </div>
      }
    >
      {loading ? (
        <div className="flex h-60 items-center justify-center text-sm text-[var(--semi-color-text-2)]">
          {t("Loading...")}
        </div>
      ) : top.length === 0 ? (
        <div className="flex h-60 items-center justify-center text-sm text-[var(--semi-color-text-2)]">
          {t("No overview data")}
        </div>
      ) : (
        <div className="grid grid-cols-1 md:grid-cols-2">
          <div className="md:border-r">
            <RankingColumn items={columns[0]!} offset={0} />
          </div>
          <div>
            <RankingColumn items={columns[1]!} offset={5} />
          </div>
        </div>
      )}
    </Card>
  );
}
