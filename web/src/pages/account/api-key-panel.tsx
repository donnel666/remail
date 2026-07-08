import {
  IconDelete,
  IconEdit,
  IconKey,
  IconPlay,
  IconPlus,
  IconStop,
} from "@douyinfe/semi-icons";
import {
  Avatar,
  Button,
  Card,
  DatePicker,
  Input,
  InputNumber,
  Modal,
  Space,
  Tag,
  Tooltip,
  Toast,
  Typography,
} from "@douyinfe/semi-ui";
import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

import { createCopyableConfig } from "@/components/semi/copyable-config";
import { OverflowTooltip } from "@/components/semi/overflow-tooltip";
import { getIamErrorMessage } from "@/lib/iam-errors";
import {
  createAPIKey,
  deleteAPIKey,
  listAPIKeys,
  updateAPIKey,
  type APIKeyResponse,
} from "@/lib/openapi-credentials-api";

const { Text } = Typography;

interface ApiKeyRecord {
  createdAt: string;
  enabled: boolean;
  expiresAt: string | null;
  id: number;
  lastUsedAt: string;
  name: string;
  quota: number | null;
  quotaUsed: number;
  rpmLimit: number | null;
  token: string;
}

function maskApiKey(value: string) {
  if (value.length <= 18) return value;
  return `${value.slice(0, 7)}**********${value.slice(-4)}`;
}

function formatDateTime(value?: string | null) {
  if (!value) return "-";
  const date = new Date(value);
  if (!Number.isFinite(date.getTime())) return value;
  return date.toLocaleString();
}

function toDateInput(value?: string | null) {
  if (!value) return null;
  return value.slice(0, 10);
}

function toExpireAt(value: string | null) {
  if (!value) return null;
  return new Date(`${value}T23:59:59`).toISOString();
}

function toApiKeyRecord(item: APIKeyResponse): ApiKeyRecord {
  return {
    createdAt: formatDateTime(item.createdAt),
    enabled: item.enabled,
    expiresAt: toDateInput(item.expireAt),
    id: item.id,
    lastUsedAt: formatDateTime(item.lastUsedAt),
    name: item.name || item.keyPrefix,
    quota: item.quotaLimit ?? null,
    quotaUsed: item.quotaUsed,
    rpmLimit: item.rateLimitPerMinute ?? null,
    token: item.keyPlain || item.keyPrefix,
  };
}

function getRemainingDays(expiresAt: string | null) {
  if (!expiresAt) return null;
  const expiresAtTime = new Date(`${expiresAt}T23:59:59`).getTime();
  if (!Number.isFinite(expiresAtTime)) return null;
  return Math.max(0, Math.ceil((expiresAtTime - Date.now()) / 86_400_000));
}

function getRemainingQuota(record: ApiKeyRecord) {
  if (record.quota == null) return null;
  return Math.max(0, record.quota - record.quotaUsed);
}

function normalizeOptionalPositiveInteger(value: number | string | null | undefined) {
  if (value === "" || value == null) return null;
  const parsed = Number(value);
  if (!Number.isFinite(parsed) || parsed <= 0) return null;
  return Math.floor(parsed);
}

function normalizeDatePickerValue(value: unknown) {
  if (Array.isArray(value)) return normalizeDatePickerValue(value[0]);
  if (value instanceof Date) return value.toISOString().slice(0, 10);
  if (typeof value === "string" && value.trim()) {
    return value.trim().slice(0, 10);
  }
  return null;
}

export function ApiKeyPanel() {
  const { t } = useTranslation();
  const [apiKeys, setApiKeys] = useState<ApiKeyRecord[]>([]);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [apiKeyModalOpen, setApiKeyModalOpen] = useState(false);
  const [editingApiKey, setEditingApiKey] = useState<ApiKeyRecord | null>(null);
  const [apiKeyName, setApiKeyName] = useState("");
  const [apiKeyExpiresAt, setApiKeyExpiresAt] = useState<string | null>(null);
  const [apiKeyQuota, setApiKeyQuota] = useState<number | null>(null);
  const [apiKeyRpmLimit, setApiKeyRpmLimit] = useState<number | null>(null);

  const refreshAPIKeys = useCallback(async () => {
    setLoading(true);
    try {
      const response = await listAPIKeys({ limit: 100, offset: 0 });
      setApiKeys(response.items.map(toApiKeyRecord));
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Failed to load API keys."));
    } finally {
      setLoading(false);
    }
  }, [t]);

  useEffect(() => {
    void refreshAPIKeys();
  }, [refreshAPIKeys]);

  const openCreateApiKey = () => {
    setEditingApiKey(null);
    setApiKeyName("");
    setApiKeyExpiresAt(null);
    setApiKeyQuota(null);
    setApiKeyRpmLimit(null);
    setApiKeyModalOpen(true);
  };

  const openEditApiKey = (record: ApiKeyRecord) => {
    setEditingApiKey(record);
    setApiKeyName(record.name);
    setApiKeyExpiresAt(record.expiresAt);
    setApiKeyQuota(record.quota);
    setApiKeyRpmLimit(record.rpmLimit);
    setApiKeyModalOpen(true);
  };

  const closeApiKeyModal = () => {
    setApiKeyModalOpen(false);
    setEditingApiKey(null);
    setApiKeyName("");
    setApiKeyExpiresAt(null);
    setApiKeyQuota(null);
    setApiKeyRpmLimit(null);
  };

  const saveApiKey = async () => {
    const nextName = apiKeyName.trim();
    if (!nextName) {
      Toast.warning(t("Please enter API key name."));
      return;
    }

    setSaving(true);
    try {
      if (editingApiKey) {
        const updated = await updateAPIKey(editingApiKey.id, {
          expireAt: toExpireAt(apiKeyExpiresAt),
          name: nextName,
          quotaLimit: apiKeyQuota,
          rateLimitPerMinute: apiKeyRpmLimit,
        });
        setApiKeys((items) =>
          items.map((item) =>
            item.id === editingApiKey.id ? toApiKeyRecord(updated) : item
          )
        );
        Toast.success(t("API key updated."));
      } else {
        const created = await createAPIKey({
          expireAt: toExpireAt(apiKeyExpiresAt),
          name: nextName,
          quotaLimit: apiKeyQuota,
          rateLimitPerMinute: apiKeyRpmLimit,
        });
        setApiKeys((items) => [toApiKeyRecord(created), ...items]);
        Toast.success(t("API key created."));
      }
      closeApiKeyModal();
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "API key operation failed."));
    } finally {
      setSaving(false);
    }
  };

  const toggleApiKeyEnabled = async (record: ApiKeyRecord) => {
    const nextEnabled = !record.enabled;
    try {
      const updated = await updateAPIKey(record.id, { enabled: nextEnabled });
      setApiKeys((items) =>
        items.map((item) => (item.id === record.id ? toApiKeyRecord(updated) : item))
      );
      Toast.success(t(nextEnabled ? "API key enabled." : "API key disabled."));
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "API key operation failed."));
    }
  };

  const deleteApiKey = (record: ApiKeyRecord) => {
    Modal.confirm({
      cancelText: t("Cancel"),
      content: t("Confirm delete API key content", { name: record.name }),
      okText: t("Delete"),
      onOk: async () => {
        try {
          await deleteAPIKey(record.id);
          setApiKeys((items) => items.filter((item) => item.id !== record.id));
          Toast.success(t("API key deleted."));
        } catch (error) {
          Toast.error(getIamErrorMessage(t, error, "API key operation failed."));
        }
      },
      title: t("Confirm delete"),
    });
  };

  return (
    <>
      <Card className="account-api-card !rounded-2xl">
        <div className="account-card-header account-card-header-with-action">
          <div className="account-card-header-main">
            <Avatar className="mr-3 shadow-md" color="orange" size="small">
              <IconKey />
            </Avatar>
            <div>
              <Text className="text-lg font-medium">{t("API KEY")}</Text>
              <div className="text-xs text-[var(--semi-color-text-2)]">
                {t("Manage API keys for programmatic access.")}
              </div>
            </div>
          </div>
          <Button
            icon={<IconPlus />}
            onClick={openCreateApiKey}
            theme="solid"
            type="primary"
          >
            {t("Create API key")}
          </Button>
        </div>

        <div className="account-api-body">
          {loading ? (
            <div className="account-api-empty">
              <Text type="tertiary">{t("Loading")}</Text>
            </div>
          ) : apiKeys.length === 0 ? (
            <div className="account-api-empty">
              <div className="account-setting-icon is-orange">
                <IconKey />
              </div>
              <Text type="tertiary">{t("No API keys")}</Text>
            </div>
          ) : (
            apiKeys.map((record) => (
              <Card className="account-api-key-item !rounded-xl" key={record.id}>
                <div className="account-api-key-main">
                  <div className="account-api-key-heading">
                    <div className="account-api-key-title-block">
                      <div className="account-api-key-title">
                        <OverflowTooltip content={record.name}>
                          <Text strong>{record.name}</Text>
                        </OverflowTooltip>
                        <Tag
                          color={record.enabled ? "green" : "grey"}
                          shape="circle"
                          size="small"
                        >
                          {t(record.enabled ? "Enabled" : "Disabled")}
                        </Tag>
                      </div>
                      <div className="account-api-key-time-row">
                        <span className="account-api-key-time-item">
                          <Text size="small" type="tertiary">
                            {t("Created At")}
                          </Text>
                          <Text size="small" type="secondary">
                            {record.createdAt}
                          </Text>
                        </span>
                        <span className="account-api-key-time-item">
                          <Text size="small" type="tertiary">
                            {t("Last used")}
                          </Text>
                          <Text size="small" type="secondary">
                            {record.lastUsedAt}
                          </Text>
                        </span>
                      </div>
                    </div>
                    <Space className="account-api-key-actions" spacing={4}>
                      <Tooltip content={record.enabled ? t("Disable") : t("Enable")}>
                        <Button
                          aria-label={record.enabled ? t("Disable") : t("Enable")}
                          icon={record.enabled ? <IconStop /> : <IconPlay />}
                          onClick={() => toggleApiKeyEnabled(record)}
                          size="small"
                          theme="borderless"
                          type={record.enabled ? "tertiary" : "primary"}
                        />
                      </Tooltip>
                      <Tooltip content={t("Edit")}>
                        <Button
                          aria-label={t("Edit")}
                          icon={<IconEdit />}
                          onClick={() => openEditApiKey(record)}
                          size="small"
                          theme="borderless"
                          type="tertiary"
                        />
                      </Tooltip>
                      <Tooltip content={t("Delete")}>
                        <Button
                          aria-label={t("Delete")}
                          icon={<IconDelete />}
                          onClick={() => deleteApiKey(record)}
                          size="small"
                          theme="borderless"
                          type="danger"
                        />
                      </Tooltip>
                    </Space>
                  </div>
                  <div className="account-api-key-summary">
                    <Text
                      className="account-api-key-token font-mono-data"
                      copyable={createCopyableConfig(record.token, t("Copied"))}
                      type="tertiary"
                    >
                      {maskApiKey(record.token)}
                    </Text>
                  </div>
                  <div className="account-api-key-limits">
                    <div className="account-api-key-limit">
                      <Text size="small" type="tertiary">
                        {t("Days left")}
                      </Text>
                      <Text size="small" strong>
                        {record.expiresAt == null
                          ? t("No expiry")
                          : t("Days count", {
                              count: getRemainingDays(record.expiresAt) ?? 0,
                            })}
                      </Text>
                    </div>
                    <div className="account-api-key-limit">
                      <Text size="small" type="tertiary">
                        {t("Remaining quota")}
                      </Text>
                      <Text size="small" strong>
                        {record.quota == null
                          ? t("Unlimited")
                          : (getRemainingQuota(record) ?? 0).toLocaleString()}
                      </Text>
                    </div>
                    <div className="account-api-key-limit">
                      <Text size="small" type="tertiary">
                        {t("RPM limit")}
                      </Text>
                      <Text size="small" strong>
                        {record.rpmLimit == null
                          ? t("Unlimited")
                          : record.rpmLimit.toLocaleString()}
                      </Text>
                    </div>
                  </div>
                </div>
              </Card>
            ))
          )}
        </div>
      </Card>

      <Modal
        centered
        className="account-api-key-modal"
        onCancel={closeApiKeyModal}
        onOk={saveApiKey}
        confirmLoading={saving}
        size="small"
        title={editingApiKey ? t("Edit API key") : t("Create API key")}
        visible={apiKeyModalOpen}
      >
        <div className="account-api-key-modal-body">
          <Text strong>{t("Name")}</Text>
          <Input
            autoFocus
            className="!rounded-lg mt-2"
            onChange={(value) => setApiKeyName(String(value))}
            placeholder={t("API key name")}
            prefix={<IconKey />}
            size="large"
            value={apiKeyName}
          />
          <div className="account-api-key-form-grid">
            <div>
              <Text strong>{t("Expires at")}</Text>
              <DatePicker
                className="!rounded-lg mt-2"
                format="yyyy-MM-dd"
                inputReadOnly
                onChange={(value, valueText) =>
                  setApiKeyExpiresAt(normalizeDatePickerValue(valueText ?? value))
                }
                placeholder={t("No expiry")}
                showClear
                size="large"
                style={{ width: "100%" }}
                type="date"
                value={apiKeyExpiresAt ?? undefined}
              />
            </div>
            <div>
              <Text strong>{t("Quota limit")}</Text>
              <InputNumber
                className="!rounded-lg mt-2"
                min={1}
                onChange={(value) =>
                  setApiKeyQuota(normalizeOptionalPositiveInteger(value))
                }
                precision={0}
                placeholder={t("Unlimited")}
                showClear
                size="large"
                step={100}
                style={{ width: "100%" }}
                value={apiKeyQuota ?? ""}
              />
            </div>
            <div>
              <Text strong>{t("RPM limit")}</Text>
              <InputNumber
                className="!rounded-lg mt-2"
                min={1}
                onChange={(value) =>
                  setApiKeyRpmLimit(normalizeOptionalPositiveInteger(value))
                }
                precision={0}
                placeholder={t("Unlimited")}
                showClear
                size="large"
                step={10}
                style={{ width: "100%" }}
                value={apiKeyRpmLimit ?? ""}
              />
            </div>
          </div>
        </div>
      </Modal>
    </>
  );
}
