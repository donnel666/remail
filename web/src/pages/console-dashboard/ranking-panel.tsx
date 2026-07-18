import { Card } from "@douyinfe/semi-ui";
import { History, Trophy } from "lucide-react";
import { useTranslation } from "react-i18next";

import type { DashboardRankItem } from "@/lib/dashboard-api";

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

function RankingColumn({
  items,
}: {
  items: DashboardRankItem[];
}) {
  const { t } = useTranslation();

  return items.map((item, index) => {
    const rank = item.rank || index + 1;

    return (
      <div
        className="grid grid-cols-4 items-center py-2 transition-colors hover:bg-[var(--semi-color-fill-0)]"
        key={item.name}
      >
        <div className="flex items-center justify-center">
          <RankMark rank={rank} />
        </div>
        <div className="truncate font-medium text-[var(--semi-color-text-1)]">
          {item.name}
          {item.isCurrentUser ? (
            <span className="ml-1 text-xs text-[var(--semi-color-primary)]">({t("Me")})</span>
          ) : null}
        </div>
        <div className="text-sm text-[var(--semi-color-text-2)]">
          {item.count.toLocaleString("zh-CN")}
        </div>
        <div />
      </div>
    );
  });
}

function CurrentUserRankRow({ item }: { item: DashboardRankItem }) {
  const { t } = useTranslation();

  return (
    <div className="grid grid-cols-1 border-t md:grid-cols-2">
      <div className="hidden md:block" />
      <div className="grid grid-cols-4 items-center py-2 transition-colors hover:bg-[var(--semi-color-fill-0)]">
        <div className="flex items-center justify-center">
          <RankMark rank={item.rank} />
        </div>
        <div className="min-w-0 truncate font-medium text-[var(--semi-color-text-1)]">
          {item.name}
          <span className="ml-1 text-xs text-[var(--semi-color-primary)]">({t("Me")})</span>
        </div>
        <div className="text-sm text-[var(--semi-color-text-2)]">
          {item.count.toLocaleString("zh-CN")}
        </div>
        <div />
      </div>
    </div>
  );
}

function topRankingItems(items: DashboardRankItem[], currentUserRank?: DashboardRankItem) {
  const top = items.slice(0, 10);
  if (
    !currentUserRank ||
    currentUserRank.rank < 1 ||
    currentUserRank.rank > 10 ||
    top.some((item) => item.isCurrentUser)
  ) {
    return top;
  }
  return [...top, currentUserRank].sort((left, right) => left.rank - right.rank).slice(0, 10);
}

export function RankingPanel({
  currentUserRank,
  items,
  kind,
  loading,
  title,
}: {
  currentUserRank?: DashboardRankItem;
  items: DashboardRankItem[];
  kind: "history" | "today";
  loading: boolean;
  title: string;
}) {
  const { t } = useTranslation();
  const top = topRankingItems(items, currentUserRank);
  const columns = [top.slice(0, 5), top.slice(5, 10)];
  const TitleIcon = kind === "today" ? Trophy : History;
  const currentUserInTop = top.some((item) => item.isCurrentUser);
  const showCurrentUserRank = currentUserRank && currentUserRank.rank > 10 && !currentUserInTop;

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
        <div>
          <div className="grid grid-cols-1 md:grid-cols-2">
            <div className="md:border-r" data-testid="ranking-left-column">
              <RankingColumn items={columns[0]!} />
            </div>
            <div data-testid="ranking-right-column">
              <RankingColumn items={columns[1]!} />
            </div>
          </div>
          {showCurrentUserRank ? (
            <div data-testid="ranking-current-user-row">
              <CurrentUserRankRow item={currentUserRank} />
            </div>
          ) : null}
        </div>
      )}
    </Card>
  );
}
