import { useEffect, useState } from "react";
import { Button, Table, Tag, Typography } from "@douyinfe/semi-ui";
import { useTranslation } from "react-i18next";

import { CopyableTableText } from "@/components/semi/copyable-table-text";
import { getAdminICloudFamily, refreshAdminICloudFamily, type AdminICloudFamily } from "@/lib/admin-icloud-api";
import { getIamErrorMessage } from "@/lib/iam-errors";

const stateLabels: Record<string, string> = {
  queued: "Family refresh queued",
  querying: "Loading family members",
  logging_in: "Signing in with device code",
  waiting: "Family refresh is waiting for an account task.",
};

export function ICloudFamilyPanel({ resourceId, canOperate }: { resourceId: number; canOperate: boolean }) {
  const { t, i18n } = useTranslation();
  const [view, setView] = useState<AdminICloudFamily | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [refreshKey, setRefreshKey] = useState(0);

  useEffect(() => {
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout> | undefined;
    let retryDelay = 1500;
    const load = async (refresh: boolean) => {
      let refreshOnRetry = refresh;
      try {
        let next = await getAdminICloudFamily(resourceId, controller.signal);
        if (controller.signal.aborted) return;
        setView(next);
        // A failed POST may already have queued the job. Retry only its GET.
        refreshOnRetry = false;
        if (refresh && canOperate && next.canRefresh) {
          next = await refreshAdminICloudFamily(resourceId, controller.signal);
          if (controller.signal.aborted) return;
          setView(next);
        }
        setError("");
        retryDelay = 1500;
        if (stateLabels[next.state]) timer = setTimeout(() => void load(false), 1500);
      } catch (error) {
        if (!controller.signal.aborted) {
          setError(getIamErrorMessage(t, error, "Family refresh failed. Please retry."));
          retryDelay = Math.min(retryDelay * 2, 30000);
          timer = setTimeout(() => void load(refreshOnRetry), retryDelay);
        }
      } finally {
        if (!controller.signal.aborted) setLoading(false);
      }
    };
    setLoading(true);
    void load(true);
    return () => { controller.abort(); if (timer) clearTimeout(timer); };
  }, [resourceId, canOperate, refreshKey, t]);

  const pending = Boolean(view && stateLabels[view.state]);
  const members = view?.members ?? [];
  return <div className="space-y-4">
    <div className="flex flex-wrap items-center justify-between gap-3">
      <div className="flex flex-wrap gap-4 text-sm text-[var(--semi-color-text-1)]">
        <span>{t("Family members: {{count}}", { count: members.length })}</span>
        <span>{t("Imported members: {{count}}", { count: view?.importedCount ?? 0 })}</span>
        <span>{t("Members at limit: {{count}}", { count: view?.fullCount ?? 0 })}</span>
      </div>
      <Button disabled={!canOperate || view?.canRefresh === false || (pending && !error)} loading={loading || (pending && !error)} onClick={() => setRefreshKey((key) => key + 1)}>{t("Refresh family")}</Button>
    </div>
    <div className="flex flex-wrap items-center gap-3 text-xs text-[var(--semi-color-text-2)]">
      <span>{t("Last updated")}: {view?.syncedAt ? new Date(view.syncedAt).toLocaleString(i18n.resolvedLanguage) : "—"}</span>
      {pending && view ? <Tag color="blue">{t(stateLabels[view.state])}</Tag> : null}
    </div>
    {error || view?.lastError ? <div role="alert" className="rounded-lg bg-[var(--semi-color-warning-light-default)] p-3 text-sm">{error || t(view!.lastError)}</div> : null}
    {view?.unavailableReason ? <Typography.Text type="tertiary">{t(view.unavailableReason)}</Typography.Text> : null}
    <Table
      dataSource={members.map((member, index) => ({ ...member, key: index }))}
      rowKey="key"
      pagination={false}
      loading={loading && !view}
      empty={t(view?.state === "ready" ? "This account is not in a family." : "No family information yet.")}
      columns={[
        { title: t("Member email"), dataIndex: "email", render: (_value: unknown, member: AdminICloudFamily["members"][number]) => <div className="flex flex-wrap items-center gap-2">
          {member.email ? <CopyableTableText copiedText={t("Copied")} text={member.email} /> : <Typography.Text type="tertiary">{t("Member email unavailable")}</Typography.Text>}
          {member.organizer ? <Tag size="small">{t("Organizer")}</Tag> : null}
          {member.current ? <Tag size="small" color="blue">{t("Current account")}</Tag> : null}
        </div> },
        { title: t("Member alias count"), dataIndex: "aliasCount", width: 180, render: (count: number | null) => count === null ? null : <Tag color={count >= (view?.aliasLimit ?? 750) ? "green" : "grey"}>{count} / {view?.aliasLimit ?? 750}</Tag> },
      ]}
    />
    <Typography.Text type="tertiary" size="small">{t("Members not imported have an empty alias count.")}</Typography.Text>
  </div>;
}
