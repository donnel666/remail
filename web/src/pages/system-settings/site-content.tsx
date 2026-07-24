import { useState } from "react";
import {
  Button,
  DatePicker,
  Divider,
  Empty,
  Input,
  InputNumber,
  Modal,
  Select,
  Space,
  Switch,
  Table,
  Tag,
  TextArea,
  Toast,
  Tooltip,
  Typography,
} from "@douyinfe/semi-ui";
import {
  Bell,
  Edit,
  HelpCircle,
  Maximize2,
  Plus,
  Save,
  Trash2,
} from "lucide-react";
import {
  IllustrationNoResult,
  IllustrationNoResultDark,
} from "@douyinfe/semi-illustrations";
import { useTranslation } from "react-i18next";

import { parseOption, parseSettingsList } from "@/lib/system-settings-api";

import type { SectionProps } from "./index";
import {
  SettingsFormGrid,
  SettingsSection,
  SettingsSwitchField,
  SettingsTextareaField,
} from "./settings-layout";

type AnnouncementType = "default" | "ongoing" | "success" | "warning" | "error";

interface Announcement {
  id: number;
  title: string;
  content: string;
  type: AnnouncementType;
  startTime: string;
  endTime: string;
  enabled: boolean;
}

interface FAQItem {
  id: number;
  question: string;
  answer: string;
  weight: number;
}

const D = {
  announcements: "[]",
  global_notice: "",
  maintenance_notice: "",
  maintenance_mode: false,
  maintenance_allow_ips: "",
  announcement_enabled: true,
  faq_enabled: true,
  faq_list: "[]",
};

const EMPTY_ANNOUNCEMENT: Announcement = {
  id: 0,
  title: "",
  content: "",
  type: "default",
  startTime: "",
  endTime: "",
  enabled: true,
};

const EMPTY_FAQ: FAQItem = { id: 0, question: "", answer: "", weight: 0 };
const { Text } = Typography;

function nextId(items: { id: number }[]) {
  return Math.max(0, ...items.map((item) => item.id)) + 1;
}

function toDate(value: string) {
  if (!value) return undefined;
  const date = new Date(value);
  return Number.isFinite(date.getTime()) ? date : undefined;
}

function formatDateTime(value: string, fallback: string) {
  const date = toDate(value);
  return date ? date.toLocaleString("zh-CN", { hour12: false }) : fallback;
}

export default function SiteContentSection({ options, loading, onBulkSave }: SectionProps) {
  const { t } = useTranslation();
  const parsed = parseOption(options, D);
  const [form, setForm] = useState(parsed);
  const [announcements, setAnnouncements] = useState(() => parseSettingsList<Announcement>(parsed.announcements));
  const [faqList, setFaqList] = useState(() => parseSettingsList<FAQItem>(parsed.faq_list));
  const [announcementModalOpen, setAnnouncementModalOpen] = useState(false);
  const [contentModalOpen, setContentModalOpen] = useState(false);
  const [faqModalOpen, setFaqModalOpen] = useState(false);
  const [announcementDraft, setAnnouncementDraft] = useState<Announcement>(EMPTY_ANNOUNCEMENT);
  const [faqDraft, setFaqDraft] = useState<FAQItem>(EMPTY_FAQ);
  const [announcementPanelEnabled, setAnnouncementPanelEnabled] = useState(parsed.announcement_enabled);
  const [announcementDirty, setAnnouncementDirty] = useState(false);
  const [selectedAnnouncementIds, setSelectedAnnouncementIds] = useState<number[]>([]);
  const [faqDirty, setFaqDirty] = useState(false);
  const [selectedFaqIds, setSelectedFaqIds] = useState<number[]>([]);
  const update = <K extends keyof typeof form>(key: K, value: (typeof form)[K]) =>
    setForm((current) => ({ ...current, [key]: value }));

  const markAnnouncementChanged = () => setAnnouncementDirty(true);
  const markFaqChanged = () => setFaqDirty(true);

  const saveAll = async () => {
    const values = {
      global_notice: form.global_notice,
      maintenance_notice: form.maintenance_notice,
      maintenance_mode: form.maintenance_mode,
      maintenance_allow_ips: form.maintenance_allow_ips,
    };
    await onBulkSave(Object.entries(values).map(([key, value]) => ({ key, value: String(value) })));
  };

  const saveAnnouncement = () => {
    const draft = {
      ...announcementDraft,
      title: announcementDraft.title.trim(),
      content: announcementDraft.content.trim(),
    };
    if (!draft.title || !draft.content) {
      Toast.warning(t("请填写公告标题和内容"));
      return;
    }
    if (!draft.id && announcements.length >= 100) {
      Toast.warning(t("系统公告最多添加 100 条"));
      return;
    }
    setAnnouncements((current) => draft.id
      ? current.map((item) => item.id === draft.id ? draft : item)
      : [...current, { ...draft, id: nextId(current) }]);
    markAnnouncementChanged();
    setAnnouncementModalOpen(false);
  };

  const saveAnnouncements = async () => {
    await onBulkSave([
      { key: "announcements", value: JSON.stringify(announcements) },
    ]);
    setAnnouncementDirty(false);
    setSelectedAnnouncementIds([]);
  };

  const deleteAnnouncement = (id: number) => {
    setAnnouncements((current) => current.filter((item) => item.id !== id));
    setSelectedAnnouncementIds((current) => current.filter((item) => item !== id));
    markAnnouncementChanged();
  };

  const deleteSelectedAnnouncements = () => {
    if (selectedAnnouncementIds.length === 0) {
      Toast.warning(t("请先选择要删除的系统公告"));
      return;
    }
    setAnnouncements((current) => current.filter((item) => !selectedAnnouncementIds.includes(item.id)));
    setSelectedAnnouncementIds([]);
    markAnnouncementChanged();
  };

  const saveFaq = () => {
    const draft = {
      ...faqDraft,
      question: faqDraft.question.trim(),
      answer: faqDraft.answer.trim(),
    };
    if (!draft.question || !draft.answer) {
      Toast.warning(t("请填写问题和答案"));
      return;
    }
    if (!draft.id && faqList.length >= 50) {
      Toast.warning(t("常见问题最多添加 50 条"));
      return;
    }
    setFaqList((current) => draft.id
      ? current.map((item) => item.id === draft.id ? draft : item)
      : [...current, { ...draft, id: nextId(current) }]);
    markFaqChanged();
    setFaqModalOpen(false);
  };

  const saveFaqSettings = async () => {
    await onBulkSave([
      { key: "faq_list", value: JSON.stringify(faqList) },
      { key: "faq_enabled", value: String(form.faq_enabled) },
    ]);
    setFaqDirty(false);
    setSelectedFaqIds([]);
  };

  const typeOptions = [
    { value: "default", label: t("普通") },
    { value: "ongoing", label: t("进行中") },
    { value: "success", label: t("成功") },
    { value: "warning", label: t("警告") },
    { value: "error", label: t("错误") },
  ];
  const typeColors: Record<AnnouncementType, "grey" | "blue" | "green" | "orange" | "red"> = {
    default: "grey",
    ongoing: "blue",
    success: "green",
    warning: "orange",
    error: "red",
  };

  const announcementRowSelection = {
    selectedRowKeys: selectedAnnouncementIds,
    onChange: (keys: Array<string | number> | undefined) => setSelectedAnnouncementIds((keys ?? []).map(Number)),
  };

  const announcementColumns = [
    {
      title: t("标题"),
      dataIndex: "title",
      render: (value: string) => <Tooltip content={value}><div className="max-w-52 truncate font-medium">{value}</div></Tooltip>,
    },
    {
      title: t("内容"),
      dataIndex: "content",
      render: (value: string) => <Tooltip content={value}><div className="max-w-72 truncate">{value}</div></Tooltip>,
    },
    {
      title: t("类型"),
      dataIndex: "type",
      width: 90,
      render: (value: AnnouncementType) => <Tag color={typeColors[value] ?? "grey"} shape="circle">{typeOptions.find((item) => item.value === value)?.label ?? value}</Tag>,
    },
    {
      title: t("展示时间"),
      width: 260,
      render: (_: unknown, item: Announcement) => <span className="text-xs text-[var(--semi-color-text-2)]">{formatDateTime(item.startTime, t("立即"))} — {formatDateTime(item.endTime, t("长期"))}</span>,
    },
    {
      title: t("启用"),
      dataIndex: "enabled",
      width: 72,
      render: (checked: boolean, item: Announcement) => <Switch checked={checked} onChange={(value) => { setAnnouncements((current) => current.map((row) => row.id === item.id ? { ...row, enabled: value } : row)); markAnnouncementChanged(); }} />,
    },
    {
      title: t("操作"),
      width: 150,
      render: (_: unknown, item: Announcement) => <Space>
        <Button icon={<Edit size={14} />} size="small" theme="light" type="tertiary" onClick={() => { setAnnouncementDraft(item); setAnnouncementModalOpen(true); }}>{t("编辑")}</Button>
        <Button icon={<Trash2 size={14} />} size="small" theme="light" type="danger" onClick={() => Modal.confirm({ title: t("删除公告"), content: t("确认删除这条公告吗？"), onOk: () => deleteAnnouncement(item.id) })}>{t("删除")}</Button>
      </Space>,
    },
  ];

  const announcementHeader = <div className="flex w-full flex-col">
    <div className="mb-2">
      <div className="flex items-center">
        <Bell size={16} className="mr-2" />
        <Text>{t("系统公告管理，可以发布系统通知和重要消息（最多100个，前端显示最新20条）")}</Text>
      </div>
    </div>
    <Divider margin="12px" />
    <div className="flex w-full flex-col items-center justify-between gap-4 md:flex-row">
      <div className="order-2 flex w-full gap-2 md:order-1 md:w-auto">
        <Button icon={<Plus size={14} />} theme="light" type="primary" className="w-full md:w-auto" onClick={() => { setAnnouncementDraft({ ...EMPTY_ANNOUNCEMENT }); setAnnouncementModalOpen(true); }}>{t("添加公告")}</Button>
        <Button icon={<Trash2 size={14} />} theme="light" type="danger" className="w-full md:w-auto" disabled={selectedAnnouncementIds.length === 0} onClick={deleteSelectedAnnouncements}>{t("批量删除")} {selectedAnnouncementIds.length > 0 ? `(${selectedAnnouncementIds.length})` : ""}</Button>
        <Button icon={<Save size={14} />} type="secondary" className="w-full md:w-auto" loading={loading} disabled={!announcementDirty} onClick={() => void saveAnnouncements().catch(() => undefined)}>{t("保存设置")}</Button>
      </div>
      <div className="order-1 flex items-center gap-2 md:order-2">
        <Switch aria-label={t("系统公告开关")} checked={announcementPanelEnabled} onChange={(value) => {
          const previous = announcementPanelEnabled;
          setAnnouncementPanelEnabled(value);
          void onBulkSave([{ key: "announcement_enabled", value: String(value) }]).catch(() => setAnnouncementPanelEnabled(previous));
        }} />
        <Text>{announcementPanelEnabled ? t("已启用") : t("已禁用")}</Text>
      </div>
    </div>
  </div>;

  const faqRowSelection = {
    selectedRowKeys: selectedFaqIds,
    onChange: (keys: Array<string | number> | undefined) => setSelectedFaqIds((keys ?? []).map(Number)),
  };

  const deleteFaq = (id: number) => {
    setFaqList((current) => current.filter((item) => item.id !== id));
    setSelectedFaqIds((current) => current.filter((item) => item !== id));
    markFaqChanged();
  };

  const deleteSelectedFaqs = () => {
    if (selectedFaqIds.length === 0) {
      Toast.warning(t("请先选择要删除的常见问答"));
      return;
    }
    setFaqList((current) => current.filter((item) => !selectedFaqIds.includes(item.id)));
    setSelectedFaqIds([]);
    markFaqChanged();
  };

  const faqHeader = <div className="flex w-full flex-col">
    <div className="mb-2">
      <div className="flex items-center">
        <HelpCircle size={16} className="mr-2" />
        <Text>{t("常见问答管理，为用户提供常见问题的答案（最多50个，前端显示最新20条）")}</Text>
      </div>
    </div>
    <Divider margin="12px" />
    <div className="flex w-full flex-col items-center justify-between gap-4 md:flex-row">
      <div className="order-2 flex w-full gap-2 md:order-1 md:w-auto">
        <Button icon={<Plus size={14} />} theme="light" type="primary" className="w-full md:w-auto" onClick={() => { setFaqDraft({ ...EMPTY_FAQ }); setFaqModalOpen(true); }}>{t("添加问答")}</Button>
        <Button icon={<Trash2 size={14} />} theme="light" type="danger" className="w-full md:w-auto" disabled={selectedFaqIds.length === 0} onClick={deleteSelectedFaqs}>{t("批量删除")} {selectedFaqIds.length > 0 ? `(${selectedFaqIds.length})` : ""}</Button>
        <Button icon={<Save size={14} />} type="secondary" className="w-full md:w-auto" loading={loading} disabled={!faqDirty} onClick={() => void saveFaqSettings().catch(() => undefined)}>{t("保存设置")}</Button>
      </div>
      <div className="order-1 flex items-center gap-2 md:order-2">
        <Switch aria-label={t("常见问答开关")} checked={form.faq_enabled} onChange={(value) => { update("faq_enabled", value); markFaqChanged(); }} />
        <Text>{form.faq_enabled ? t("已启用") : t("已禁用")}</Text>
      </div>
    </div>
  </div>;

  const faqColumns = [
    { title: t("问题标题"), dataIndex: "question", render: (value: string) => <Tooltip content={value}><div className="max-w-[300px] truncate font-bold">{value}</div></Tooltip> },
    { title: t("回答内容"), dataIndex: "answer", render: (value: string) => <Tooltip content={value}><div className="max-w-[400px] truncate text-[var(--semi-color-text-1)]">{value}</div></Tooltip> },
    {
      title: t("操作"),
      width: 150,
      render: (_: unknown, item: FAQItem) => <Space>
        <Button icon={<Edit size={14} />} size="small" theme="light" type="tertiary" onClick={() => { setFaqDraft(item); setFaqModalOpen(true); }}>{t("编辑")}</Button>
        <Button icon={<Trash2 size={14} />} size="small" theme="light" type="danger" onClick={() => Modal.confirm({ title: t("删除常见问题"), content: t("确认删除这条常见问题吗？"), onOk: () => deleteFaq(item.id) })}>{t("删除")}</Button>
      </Space>,
    },
  ];

  return <div className="space-y-6">
    <SettingsSection title={announcementHeader}>
      <Table
        columns={announcementColumns as never}
        dataSource={announcements}
        empty={<Empty darkModeImage={<IllustrationNoResultDark style={{ height: 150, width: 150 }} />} description={t("暂无系统公告")} image={<IllustrationNoResult style={{ height: 150, width: 150 }} />} style={{ padding: "96px 30px" }} />}
        pagination={false}
        rowKey="id"
        rowSelection={announcementRowSelection}
        size="middle"
        style={{ width: "100%" }}
      />
    </SettingsSection>

    <SettingsSection title={t("系统通知") }>
      <SettingsFormGrid>
        <SettingsTextareaField label={t("全局通知")} value={form.global_notice} onChange={(value) => update("global_notice", value)} rows={9} placeholder={t("显示在页面顶部，支持 Markdown/HTML")} />
        <SettingsTextareaField label={t("维护模式通知")} value={form.maintenance_notice} onChange={(value) => update("maintenance_notice", value)} rows={9} />
        <SettingsSwitchField checked={form.maintenance_mode} onChange={(value) => update("maintenance_mode", value)} label={t("维护模式开关")} description={t("开启后所有非管理员用户看到维护页面")} />
        <SettingsTextareaField label={t("维护模式允许的 IP")} value={form.maintenance_allow_ips} onChange={(value) => update("maintenance_allow_ips", value)} rows={6} placeholder={t("每行一个 IP，维护期间仍可正常访问")} />
      </SettingsFormGrid>
      <Button icon={<Save size={14} />} loading={loading} onClick={() => void saveAll().catch(() => undefined)} theme="solid" type="primary" className="mt-4">{t("保存全部")}</Button>
    </SettingsSection>

    <SettingsSection title={faqHeader}>
      <Table
        columns={faqColumns as never}
        dataSource={faqList}
        empty={<Empty darkModeImage={<IllustrationNoResultDark style={{ height: 150, width: 150 }} />} description={t("暂无常见问答")} image={<IllustrationNoResult style={{ height: 150, width: 150 }} />} style={{ padding: "96px 30px" }} />}
        pagination={false}
        rowKey="id"
        rowSelection={faqRowSelection}
        size="middle"
        style={{ width: "100%" }}
      />
    </SettingsSection>

    <Modal title={announcementDraft.id ? t("编辑公告") : t("添加公告")} visible={announcementModalOpen} onCancel={() => setAnnouncementModalOpen(false)} onOk={saveAnnouncement} okText={t("保存")} cancelText={t("取消")} width={600}>
      <div className="space-y-4">
        <label className="block" htmlFor="announcement-title"><span className="mb-1.5 block text-sm font-medium">{t("公告标题")}</span><Input autoFocus id="announcement-title" name="announcement-title" value={announcementDraft.title} onChange={(value) => setAnnouncementDraft((current) => ({ ...current, title: value }))} /></label>
        <div>
          <label className="mb-1.5 block text-sm font-medium" htmlFor="announcement-content">{t("公告内容")}</label>
          <TextArea id="announcement-content" name="announcement-content" rows={4} maxCount={500} value={announcementDraft.content} placeholder={t("请输入公告内容（支持 Markdown/HTML）")} onChange={(value) => setAnnouncementDraft((current) => ({ ...current, content: value }))} />
          <Button icon={<Maximize2 size={14} />} size="small" theme="light" type="tertiary" className="mt-2" onClick={() => setContentModalOpen(true)}>{t("放大编辑")}</Button>
        </div>
        <div>
          <span className="mb-1.5 block text-sm font-medium">{t("开始时间")}</span>
          <DatePicker aria-label={t("开始时间")} type="dateTime" format="yyyy-MM-dd HH:mm:ss" showClear style={{ width: "100%" }} value={toDate(announcementDraft.startTime)} onChange={(value) => setAnnouncementDraft((current) => ({ ...current, startTime: value instanceof Date ? value.toISOString() : "" }))} />
        </div>
        <div>
          <span className="mb-1.5 block text-sm font-medium">{t("结束时间")}</span>
          <DatePicker aria-label={t("结束时间")} type="dateTime" format="yyyy-MM-dd HH:mm:ss" showClear style={{ width: "100%" }} value={toDate(announcementDraft.endTime)} onChange={(value) => setAnnouncementDraft((current) => ({ ...current, endTime: value instanceof Date ? value.toISOString() : "" }))} />
        </div>
        <div>
          <span className="mb-1.5 block text-sm font-medium">{t("公告类型")}</span>
          <Select aria-label={t("公告类型")} optionList={typeOptions} style={{ width: "100%" }} value={announcementDraft.type} onChange={(value) => setAnnouncementDraft((current) => ({ ...current, type: String(value) as AnnouncementType }))} />
        </div>
        <div className="flex items-center justify-between rounded-lg bg-[var(--semi-color-fill-0)] px-3 py-2">
          <span className="text-sm font-medium">{t("是否启用")}</span>
          <Switch aria-label={t("是否启用")} checked={announcementDraft.enabled} onChange={(value) => setAnnouncementDraft((current) => ({ ...current, enabled: value }))} />
        </div>
      </div>
    </Modal>

    <Modal title={t("编辑公告内容")} visible={contentModalOpen} onCancel={() => setContentModalOpen(false)} onOk={() => setContentModalOpen(false)} okText={t("确定")} cancelText={t("取消")} width={800}>
      <TextArea autoFocus rows={14} maxCount={500} value={announcementDraft.content} placeholder={t("请输入公告内容（支持 Markdown/HTML）")} onChange={(value) => setAnnouncementDraft((current) => ({ ...current, content: value }))} />
    </Modal>

    <Modal title={faqDraft.id ? t("编辑问答") : t("添加问答")} visible={faqModalOpen} onCancel={() => setFaqModalOpen(false)} onOk={saveFaq} width={800}>
      <div className="space-y-4">
        <label className="block" htmlFor="faq-question"><span className="mb-1.5 block text-sm font-medium">{t("问题标题")}</span><Input autoFocus id="faq-question" name="faq-question" maxLength={200} value={faqDraft.question} onChange={(value) => setFaqDraft((current) => ({ ...current, question: value }))} /></label>
        <label className="block" htmlFor="faq-answer"><span className="mb-1.5 block text-sm font-medium">{t("回答内容")}</span><TextArea id="faq-answer" name="faq-answer" rows={6} maxCount={1000} placeholder={t("请输入回答内容（支持 Markdown/HTML）")} value={faqDraft.answer} onChange={(value) => setFaqDraft((current) => ({ ...current, answer: value }))} /></label>
        <label className="block" htmlFor="faq-weight"><span className="mb-1.5 block text-sm font-medium">{t("排序权重")}</span><InputNumber id="faq-weight" value={faqDraft.weight} onNumberChange={(value) => setFaqDraft((current) => ({ ...current, weight: value }))} style={{ width: "100%" }} /></label>
      </div>
    </Modal>
  </div>;
}
