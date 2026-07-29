import { useMemo, useState } from "react";
import { Button, InputNumber, Toast } from "@douyinfe/semi-ui";
import { CalendarCheck, Plus, Save, Trophy, Trash2 } from "lucide-react";
import { useTranslation } from "react-i18next";

import { parseOption, parseSettingsList } from "@/lib/system-settings-api";

import type { SectionProps } from "./index";
import { SettingsCardHeader, SettingsFormGrid, SettingsSection, SettingsTextField } from "./settings-layout";
import { DAILY_CHECKIN_REWARD_KEYS, LEADERBOARD_REWARD_KEYS } from "./users-rebates-keys";

type CheckinRule = { amount: number; probability: number };
type LeaderboardRule = { rankFrom: number; rankTo: number; amount: number };

const D: Record<string, unknown> = {
  daily_checkin_enabled: false,
  daily_checkin_reward_rules: "[]",
  leaderboard_reward_enabled: false,
  leaderboard_reward_rules: "[]",
  leaderboard_settlement_time: "00:00",
};

export default function RewardSettings({ options, onBulkSave }: SectionProps) {
  const { t } = useTranslation();
  const [form, setForm] = useState(() => parseOption(options, D as any) as Record<string, unknown>);
  const [checkinRules, setCheckinRules] = useState<CheckinRule[]>(() => parseSettingsList<CheckinRule>(form.daily_checkin_reward_rules));
  const [leaderboardRules, setLeaderboardRules] = useState<LeaderboardRule[]>(() => parseSettingsList<LeaderboardRule>(form.leaderboard_reward_rules));
  const [saving, setSaving] = useState<"checkin" | "leaderboard" | null>(null);
  const probabilityTotal = useMemo(() => checkinRules.reduce((sum, rule) => sum + Number(rule.probability || 0), 0), [checkinRules]);
  const update = (key: string, value: unknown) => setForm((current) => ({ ...current, [key]: value }));

  const saveCheckin = async () => {
    if ((form.daily_checkin_enabled && checkinRules.length === 0) || checkinRules.some((rule) => rule.amount <= 0 || rule.probability <= 0) || probabilityTotal > 1.0000001) {
      Toast.warning(t("签到奖励积分和概率必须大于 0，累计概率不能超过 1"));
      return;
    }
    setSaving("checkin");
    try {
      await onBulkSave(DAILY_CHECKIN_REWARD_KEYS.map((key) => ({
        key,
        value: key === "daily_checkin_reward_rules" ? JSON.stringify(checkinRules) : String(form[key]),
      })));
    } finally { setSaving(null); }
  };

  const saveLeaderboard = async () => {
    const invalid = leaderboardRules.some((rule, index) => rule.rankFrom < 1 || rule.rankTo < rule.rankFrom || rule.rankTo > 100 || rule.amount <= 0 || leaderboardRules.slice(0, index).some((other) => rule.rankFrom <= other.rankTo && other.rankFrom <= rule.rankTo));
    if ((form.leaderboard_reward_enabled && leaderboardRules.length === 0) || invalid) {
      Toast.warning(t("排行榜名次范围不能重叠，且奖励积分必须大于 0"));
      return;
    }
    setSaving("leaderboard");
    try {
      await onBulkSave(LEADERBOARD_REWARD_KEYS.map((key) => ({
        key,
        value: key === "leaderboard_reward_rules" ? JSON.stringify(leaderboardRules) : String(form[key]),
      })));
    } finally { setSaving(null); }
  };

  return <>
    <SettingsSection title={<SettingsCardHeader icon={<CalendarCheck size={16} />} title={t("每日签到奖励")} description={t("用户每天首次打开页面或进入数据看板时，按上海日期自动签到并抽取一次奖励")} enabled={!!form.daily_checkin_enabled} onToggle={(value) => update("daily_checkin_enabled", value)} statusText={form.daily_checkin_enabled ? t("已启用") : t("已禁用")} />}>
      <RuleHeader title={t("奖励档位")} description={t("例如奖励 100 积分、概率 0.005，表示以 0.5% 概率获得 100 积分；剩余概率不中奖")} onAdd={() => setCheckinRules((current) => [...current, { amount: 1, probability: 0.01 }])} addText={t("添加")} />
      <div className="hidden grid-cols-[1fr_1fr_32px] gap-4 border-x border-t border-[var(--semi-color-border)] px-4 py-2 text-xs text-[var(--semi-color-text-2)] sm:grid"><span>{t("奖励积分")}</span><span>{t("中奖概率（0-1）")}</span><span /></div>
      {checkinRules.map((rule, index) => <div key={index} className="grid gap-3 border-x border-b border-[var(--semi-color-border)] px-4 py-3 sm:grid-cols-[1fr_1fr_32px] sm:items-center sm:gap-4">
        <InputNumber aria-label={t("奖励积分")} min={0.000001} onNumberChange={(value) => setCheckinRules((current) => current.map((item, i) => i === index ? { ...item, amount: Number(value) || 0 } : item))} precision={6} style={{ width: "100%" }} value={rule.amount} />
        <InputNumber aria-label={t("中奖概率（0-1）")} min={0.000001} max={1} onNumberChange={(value) => setCheckinRules((current) => current.map((item, i) => i === index ? { ...item, probability: Number(value) || 0 } : item))} precision={6} step={0.001} style={{ width: "100%" }} value={rule.probability} />
        <Button aria-label={t("删除奖励档位")} icon={<Trash2 size={14} />} onClick={() => setCheckinRules((current) => current.filter((_, i) => i !== index))} size="small" theme="borderless" type="danger" />
      </div>)}
      {!checkinRules.length ? <EmptyRows text={t("暂无奖励档位")} /> : null}
      <div className="mt-2 text-xs text-[var(--semi-color-text-2)]">{t("累计中奖概率")}：{probabilityTotal.toFixed(6)}；{t("不中奖概率")}：{Math.max(0, 1 - probabilityTotal).toFixed(6)}</div>
      <Button className="mt-5" icon={<Save size={14} />} loading={saving === "checkin"} onClick={() => void saveCheckin().catch(() => undefined)} theme="solid" type="primary">{t("保存设置")}</Button>
    </SettingsSection>

    <SettingsSection title={<SettingsCardHeader icon={<Trophy size={16} />} title={t("每日排行榜奖励")} description={t("按上海自然日统计成功订单，结算后自动入账并向获奖用户发送系统通知邮件")} enabled={!!form.leaderboard_reward_enabled} onToggle={(value) => update("leaderboard_reward_enabled", value)} statusText={form.leaderboard_reward_enabled ? t("已启用") : t("已禁用")} />}>
      <SettingsFormGrid className="mt-4">
        <SettingsTextField label={t("每日结算时间（上海时区）")} type="time" value={String(form.leaderboard_settlement_time)} onChange={(value) => update("leaderboard_settlement_time", value)} />
      </SettingsFormGrid>
      <RuleHeader title={t("名次奖励")} description={t("可配置单名次或连续名次范围，例如第 3 至第 6 名使用同一奖励")} onAdd={() => setLeaderboardRules((current) => [...current, { rankFrom: current.length ? Math.max(...current.map((rule) => rule.rankTo)) + 1 : 1, rankTo: current.length ? Math.max(...current.map((rule) => rule.rankTo)) + 1 : 1, amount: 1 }])} addText={t("添加")} />
      <div className="hidden grid-cols-[1fr_1fr_1fr_32px] gap-4 border-x border-t border-[var(--semi-color-border)] px-4 py-2 text-xs text-[var(--semi-color-text-2)] sm:grid"><span>{t("起始名次")}</span><span>{t("结束名次")}</span><span>{t("奖励积分")}</span><span /></div>
      {leaderboardRules.map((rule, index) => <div key={index} className="grid gap-3 border-x border-b border-[var(--semi-color-border)] px-4 py-3 sm:grid-cols-[1fr_1fr_1fr_32px] sm:items-center sm:gap-4">
        <InputNumber aria-label={t("起始名次")} min={1} max={100} onNumberChange={(value) => setLeaderboardRules((current) => current.map((item, i) => i === index ? { ...item, rankFrom: Number(value) || 0 } : item))} precision={0} style={{ width: "100%" }} value={rule.rankFrom} />
        <InputNumber aria-label={t("结束名次")} min={1} max={100} onNumberChange={(value) => setLeaderboardRules((current) => current.map((item, i) => i === index ? { ...item, rankTo: Number(value) || 0 } : item))} precision={0} style={{ width: "100%" }} value={rule.rankTo} />
        <InputNumber aria-label={t("奖励积分")} min={0.000001} onNumberChange={(value) => setLeaderboardRules((current) => current.map((item, i) => i === index ? { ...item, amount: Number(value) || 0 } : item))} precision={6} style={{ width: "100%" }} value={rule.amount} />
        <Button aria-label={t("删除名次奖励")} icon={<Trash2 size={14} />} onClick={() => setLeaderboardRules((current) => current.filter((_, i) => i !== index))} size="small" theme="borderless" type="danger" />
      </div>)}
      {!leaderboardRules.length ? <EmptyRows text={t("暂无名次奖励")} /> : null}
      <Button className="mt-5" icon={<Save size={14} />} loading={saving === "leaderboard"} onClick={() => void saveLeaderboard().catch(() => undefined)} theme="solid" type="primary">{t("保存设置")}</Button>
    </SettingsSection>
  </>;
}

function RuleHeader({ title, description, onAdd, addText }: { title: string; description: string; onAdd: () => void; addText: string }) {
  return <div className="mt-5 flex items-center justify-between gap-4 rounded-t-lg border border-[var(--semi-color-border)] bg-[var(--semi-color-fill-0)] px-4 py-3"><div><div className="text-sm font-medium">{title}</div><div className="mt-0.5 text-xs text-[var(--semi-color-text-2)]">{description}</div></div><Button icon={<Plus size={14} />} onClick={onAdd} size="small">{addText}</Button></div>;
}

function EmptyRows({ text }: { text: string }) {
  return <div className="border border-t-0 border-[var(--semi-color-border)] px-4 py-8 text-center text-sm text-[var(--semi-color-text-2)]">{text}</div>;
}
