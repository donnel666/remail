import { IconDelete, IconKey, IconPlus } from "@douyinfe/semi-icons";
import {
  Button,
  Card,
  Input,
  Modal,
  Radio,
  RadioGroup,
  Select,
  Space,
  Tag,
  TextArea,
  Toast,
  Tooltip,
  Typography,
} from "@douyinfe/semi-ui";
import { useCallback, useEffect, useId, useState } from "react";
import { useTranslation } from "react-i18next";

import { createCopyableConfig } from "@/components/semi/copyable-config";
import { OverflowTooltip } from "@/components/semi/overflow-tooltip";
import { getIamErrorMessage } from "@/lib/iam-errors";
import {
  createSystemKey,
  deleteSystemKey,
  listSystemKeys,
  type AdminSystemKey,
  type SystemKeyPurpose,
} from "@/lib/system-keys-api";

import type { SectionProps } from "./index";
import { SettingsCardHeader, SettingsSection } from "./settings-layout";

const { Text } = Typography;

type BotType = "qq" | "telegram";

const BOT_SCOPES = {
  qq: { platform: "qq", subjectNamespace: "qq:main" },
  telegram: { platform: "telegram", subjectNamespace: "telegram:main" },
} as const;

function formatDateTime(value?: string | null) {
  if (!value) return "-";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "-" : date.toLocaleString();
}

function parseAllowedGroupIds(value: string) {
  return [
    ...new Set(
      value
        .split(/[\s,，、]+/)
        .map((item) => item.trim())
        .filter(Boolean)
    ),
  ];
}

export default function SystemKeysSection({
  canSensitive,
  canWrite,
}: SectionProps) {
  const { t } = useTranslation();
  const nameInputId = useId();
  const botTypeInputId = useId();
  const allowedGroupsInputId = useId();
  const purposeLabelId = useId();
  const [keys, setKeys] = useState<AdminSystemKey[]>([]);
  const [loading, setLoading] = useState(false);
  const [creating, setCreating] = useState(false);
  const [deleting, setDeleting] = useState<number | null>(null);
  const [createOpen, setCreateOpen] = useState(false);
  const [name, setName] = useState("");
  const [purpose, setPurpose] =
    useState<SystemKeyPurpose>("smtp_submission");
  const [botType, setBotType] = useState<BotType>("qq");
  const [allowedGroups, setAllowedGroups] = useState("");
  const [createdKey, setCreatedKey] = useState<AdminSystemKey | null>(null);

  const purposeLabel = (value?: SystemKeyPurpose) =>
    t(
      value === "bot"
        ? "Bot integration"
        : value === "icloud_forwarding"
          ? "iCloud forwarding"
          : "SMTP submission"
    );

  const purposeColor = (value?: SystemKeyPurpose) =>
    value === "bot" ? "violet" : value === "icloud_forwarding" ? "green" : "blue";

  const resetCreateForm = () => {
    setName("");
    setPurpose("smtp_submission");
    setBotType("qq");
    setAllowedGroups("");
  };

  const openCreate = () => {
    resetCreateForm();
    setCreateOpen(true);
  };

  const closeCreate = () => {
    if (creating) return;
    setCreateOpen(false);
    resetCreateForm();
  };

  const load = useCallback(async () => {
    if (!canSensitive) return;
    setLoading(true);
    try {
      const response = await listSystemKeys();
      setKeys(response.items);
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "System keys load failed."));
    } finally {
      setLoading(false);
    }
  }, [canSensitive, t]);

  useEffect(() => {
    void load();
  }, [load]);

  const create = async () => {
    const nextName = name.trim();
    if (!nextName) {
      Toast.warning(t("Please enter system key name."));
      return;
    }
    const allowedGroupIds = parseAllowedGroupIds(allowedGroups);
    if (purpose === "bot" && allowedGroupIds.length === 0) {
      Toast.warning(t("Please enter at least one allowed group ID."));
      return;
    }
    setCreating(true);
    try {
      const result =
        purpose === "bot"
          ? await createSystemKey(nextName, purpose, {
              ...BOT_SCOPES[botType],
              allowedGroupIds,
            })
          : await createSystemKey(nextName, purpose);
      if (!result.keyPlain) {
        throw new Error("System key plaintext was not returned.");
      }
      setKeys((current) => [{ ...result, keyPlain: undefined }, ...current]);
      setCreateOpen(false);
      resetCreateForm();
      setCreatedKey(result);
      Toast.success(t("System key created."));
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "System key operation failed."));
    } finally {
      setCreating(false);
    }
  };

  const revoke = (item: AdminSystemKey) => {
    Modal.confirm({
      cancelText: t("Cancel"),
      content: t("Confirm revoke system key content", { name: item.name }),
      okButtonProps: { type: "danger" },
      okText: t("Revoke"),
      onOk: async () => {
        setDeleting(item.id);
        try {
          await deleteSystemKey(item.id);
          setKeys((current) => current.filter((key) => key.id !== item.id));
          Toast.success(t("System key revoked."));
        } catch (error) {
          Toast.error(getIamErrorMessage(t, error, "System key operation failed."));
          throw error;
        } finally {
          setDeleting(null);
        }
      },
      title: t("Revoke system key"),
    });
  };

  if (!canSensitive) {
    return (
      <div className="py-12 text-center text-sm text-[var(--semi-color-text-2)]">
        {t("Permission required: sensitive system settings")}
      </div>
    );
  }

  return (
    <>
      <SettingsSection
        title={
          <SettingsCardHeader
            action={
              <Button
                className="w-full sm:w-auto"
                disabled={!canWrite}
                icon={<IconPlus />}
                onClick={openCreate}
                theme="solid"
                type="primary"
              >
                {t("Create system key")}
              </Button>
            }
            description={t("iCloud forwarding service credentials")}
            icon={<IconKey />}
            title={t("System keys")}
          />
        }
      >
        <div className="account-api-body">
          {loading ? (
            <div className="account-api-empty" aria-live="polite">
              <Text type="tertiary">{t("Loading")}</Text>
            </div>
          ) : keys.length === 0 ? (
            <div className="account-api-empty">
              <div className="account-setting-icon is-orange">
                <IconKey />
              </div>
              <Text type="tertiary">{t("No system keys")}</Text>
            </div>
          ) : (
            keys.map((item) => {
              const isBot = item.purpose === "bot";
              const scope = isBot
                ? `${item.platform || "-"} / ${item.subjectNamespace || "-"}`
                : t("None");
              const groups =
                isBot && item.allowedGroupIds?.length
                  ? item.allowedGroupIds.join(", ")
                  : t("None");
              return (
                <Card
                  className="account-api-key-item !rounded-xl"
                  key={item.id}
                >
                  <div className="account-api-key-main">
                    <div className="account-api-key-heading">
                      <div className="account-api-key-title-block">
                        <div className="account-api-key-title">
                          <OverflowTooltip content={item.name}>
                            <Text strong>{item.name}</Text>
                          </OverflowTooltip>
                          <Tag
                            color={purposeColor(item.purpose)}
                            shape="circle"
                            size="small"
                          >
                            {purposeLabel(item.purpose)}
                          </Tag>
                        </div>
                        <div className="account-api-key-time-row">
                          <span className="account-api-key-time-item">
                            <Text size="small" type="tertiary">
                              {t("Created At")}
                            </Text>
                            <Text size="small" type="secondary">
                              {formatDateTime(item.createdAt)}
                            </Text>
                          </span>
                          <span className="account-api-key-time-item">
                            <Text size="small" type="tertiary">
                              {t("Last used")}
                            </Text>
                            <Text size="small" type="secondary">
                              {item.lastUsedAt
                                ? formatDateTime(item.lastUsedAt)
                                : t("Never used")}
                            </Text>
                          </span>
                        </div>
                      </div>
                      <Space className="account-api-key-actions" spacing={4}>
                        <Tooltip content={t("Revoke")}>
                          <Button
                            aria-label={t("Revoke system key")}
                            className="!h-11 !w-11 sm:!h-8 sm:!w-8"
                            disabled={!canWrite}
                            icon={<IconDelete />}
                            loading={deleting === item.id}
                            onClick={() => revoke(item)}
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
                        type="tertiary"
                      >
                        {item.keyPrefix}...
                      </Text>
                    </div>

                    {isBot ? (
                      <div className="account-api-key-limits !grid-cols-1 sm:!grid-cols-2">
                        <div className="account-api-key-limit">
                          <Text size="small" type="tertiary">
                            {t("Bot scope")}
                          </Text>
                          <Text
                            className="!whitespace-normal break-all font-mono-data"
                            size="small"
                            strong
                          >
                            {scope}
                          </Text>
                        </div>
                        <div className="account-api-key-limit">
                          <Text size="small" type="tertiary">
                            {t("Allowed groups")}
                          </Text>
                          <Text
                            className="!whitespace-normal break-all font-mono-data"
                            size="small"
                            strong
                          >
                            {groups}
                          </Text>
                        </div>
                      </div>
                    ) : null}
                  </div>
                </Card>
              );
            })
          )}
        </div>
      </SettingsSection>

      <Modal
        cancelText={t("Cancel")}
        centered
        className="account-api-key-modal"
        confirmLoading={creating}
        onCancel={closeCreate}
        onOk={() => void create()}
        okText={t("Create")}
        size="small"
        title={t("Create system key")}
        visible={createOpen}
        width="min(448px, calc(100vw - 32px))"
      >
        <div className="account-api-key-modal-body">
          <label className="block" htmlFor={nameInputId}>
            <Text strong>{t("System key name")}</Text>
            <Input
              autoFocus
              className="!rounded-lg mt-2"
              id={nameInputId}
              maxLength={120}
              onChange={setName}
              placeholder={t("System key name placeholder")}
              prefix={<IconKey />}
              showClear
              size="large"
              value={name}
            />
          </label>

          <div>
            <Text id={purposeLabelId} strong>
              {t("Purpose")}
            </Text>
            <RadioGroup
              aria-labelledby={purposeLabelId}
              buttonSize="middle"
              className="mt-2"
              onChange={(event) =>
                setPurpose(event.target.value as SystemKeyPurpose)
              }
              type="button"
              value={purpose}
            >
              <Radio value="smtp_submission">{t("SMTP submission")}</Radio>
              <Radio value="icloud_forwarding">{t("iCloud forwarding")}</Radio>
              <Radio value="bot">{t("Bot integration")}</Radio>
            </RadioGroup>
          </div>

          {purpose === "bot" ? (
            <div className="grid grid-cols-1 gap-3">
              <label className="block" htmlFor={botTypeInputId}>
                <Text strong>{t("Robot type")}</Text>
                <Select
                  aria-label={t("Robot type")}
                  className="mt-2"
                  id={botTypeInputId}
                  onChange={(value) => setBotType(String(value) as BotType)}
                  optionList={[
                    { label: t("QQ robot"), value: "qq" },
                    { label: t("Telegram robot"), value: "telegram" },
                  ]}
                  size="large"
                  style={{ width: "100%" }}
                  value={botType}
                />
              </label>
              <label className="block" htmlFor={allowedGroupsInputId}>
                <Text strong>{t("Allowed group IDs")}</Text>
                <TextArea
                  aria-label={t("Allowed group IDs")}
                  autosize={{ minRows: 3, maxRows: 5 }}
                  className="mt-2"
                  id={allowedGroupsInputId}
                  onChange={setAllowedGroups}
                  placeholder={t(
                    botType === "telegram"
                      ? "Telegram group IDs placeholder"
                      : "QQ group IDs placeholder"
                  )}
                  value={allowedGroups}
                />
                <Text className="mt-1.5 block" size="small" type="tertiary">
                  {t("Allowed group IDs hint")}
                </Text>
              </label>
            </div>
          ) : null}
        </div>
      </Modal>

      <Modal
        centered
        className="account-api-key-modal"
        closable={false}
        footer={
          <Button
            onClick={() => setCreatedKey(null)}
            theme="solid"
            type="primary"
          >
            {t("I copied the key")}
          </Button>
        }
        maskClosable={false}
        size="small"
        title={t("System key created")}
        visible={createdKey !== null}
        width="min(448px, calc(100vw - 32px))"
      >
        <div className="account-api-key-modal-body">
          <div
            className="rounded-lg border border-[var(--semi-color-warning-light-active)] bg-[var(--semi-color-warning-light-default)] p-3"
            role="alert"
          >
            <Text>{t("System key shown once warning")}</Text>
          </div>
          {createdKey ? (
            <div className="flex flex-wrap items-center gap-2">
              <Text className="min-w-0 break-all" strong>
                {createdKey.name}
              </Text>
              <Tag
                color={purposeColor(createdKey.purpose)}
                shape="circle"
                size="small"
              >
                {purposeLabel(createdKey.purpose)}
              </Tag>
            </div>
          ) : null}
          {createdKey?.keyPlain ? (
            <div className="rounded-lg border border-[var(--semi-color-border)] bg-[var(--semi-color-fill-0)] p-3">
              <Text
                className="break-all font-mono-data"
                copyable={createCopyableConfig(
                  createdKey.keyPlain,
                  t("Copied")
                )}
              >
                {createdKey.keyPlain}
              </Text>
            </div>
          ) : null}
        </div>
      </Modal>
    </>
  );
}
