import { useCallback, useEffect, useId, useState } from "react";
import { Button, Empty, Input, Modal, Table, Toast, Tooltip, Typography } from "@douyinfe/semi-ui";
import { KeyRound, Plus, Trash2 } from "lucide-react";
import { useTranslation } from "react-i18next";

import { createCopyableConfig } from "@/components/semi/copyable-config";
import { getIamErrorMessage } from "@/lib/iam-errors";
import {
  createSystemKey,
  deleteSystemKey,
  listSystemKeys,
  type AdminSystemKey,
} from "@/lib/system-keys-api";

import type { SectionProps } from "./index";
import { SettingsCardHeader, SettingsSection } from "./settings-layout";

const { Text } = Typography;

function formatDateTime(value?: string | null) {
  if (!value) return "-";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "-" : date.toLocaleString();
}

export default function SystemKeysSection({ canSensitive, canWrite }: SectionProps) {
  const { t } = useTranslation();
  const nameInputId = useId();
  const [keys, setKeys] = useState<AdminSystemKey[]>([]);
  const [loading, setLoading] = useState(false);
  const [creating, setCreating] = useState(false);
  const [deleting, setDeleting] = useState<number | null>(null);
  const [createOpen, setCreateOpen] = useState(false);
  const [name, setName] = useState("");
  const [createdKey, setCreatedKey] = useState<AdminSystemKey | null>(null);

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
    setCreating(true);
    try {
      const result = await createSystemKey(nextName);
      if (!result.keyPlain) throw new Error("System key plaintext was not returned.");
      setKeys((current) => [{ ...result, keyPlain: undefined }, ...current]);
      setCreateOpen(false);
      setName("");
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
      title: t("Revoke system key"),
      content: t("Confirm revoke system key content", { name: item.name }),
      okButtonProps: { type: "danger" },
      okText: t("Revoke"),
      cancelText: t("Cancel"),
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
    });
  };

  if (!canSensitive) {
    return <div className="py-12 text-center text-sm text-[var(--semi-color-text-2)]">{t("Permission required: sensitive system settings")}</div>;
  }

  const columns = [
    {
      title: t("Name"),
      dataIndex: "name",
      width: 240,
      render: (value: string) => <Text strong>{value}</Text>,
    },
    {
      title: t("Key prefix"),
      dataIndex: "keyPrefix",
      width: 210,
      render: (value: string) => <Text className="font-mono-data" type="secondary">{value}...</Text>,
    },
    {
      title: t("Created At"),
      dataIndex: "createdAt",
      width: 190,
      render: (value: string) => formatDateTime(value),
    },
    {
      title: t("Last used"),
      dataIndex: "lastUsedAt",
      width: 190,
      render: (value: string | null) => value ? formatDateTime(value) : t("Never used"),
    },
    {
      title: t("Actions"),
      fixed: "right" as const,
      width: 80,
      render: (_value: unknown, item: AdminSystemKey) => (
        <Tooltip content={t("Revoke")}>
          <Button
            aria-label={t("Revoke system key")}
            disabled={!canWrite}
            icon={<Trash2 size={14} />}
            loading={deleting === item.id}
            onClick={() => revoke(item)}
            size="small"
            theme="borderless"
            type="danger"
          />
        </Tooltip>
      ),
    },
  ];

  return <>
    <SettingsSection title={<SettingsCardHeader
      icon={<KeyRound size={16} />}
      title={t("System keys")}
      description={t("iCloud forwarding service credentials")}
    />}>
      <div className="mb-4 flex justify-end">
        <Button
          disabled={!canWrite}
          icon={<Plus size={14} />}
          onClick={() => setCreateOpen(true)}
          theme="solid"
          type="primary"
        >{t("Create system key")}</Button>
      </div>
      <Table
        columns={columns as never}
        dataSource={keys}
        empty={<Empty description={t("No system keys")} style={{ padding: 48 }} />}
        loading={loading}
        pagination={false}
        rowKey="id"
        scroll={{ x: 910 }}
        size="middle"
      />
    </SettingsSection>

    <Modal
      cancelText={t("Cancel")}
      confirmLoading={creating}
      onCancel={() => setCreateOpen(false)}
      onOk={() => void create()}
      okText={t("Create")}
      size="small"
      title={t("Create system key")}
      visible={createOpen}
    >
      <label className="block" htmlFor={nameInputId}>
        <span className="mb-1.5 block text-sm font-medium">{t("System key name")}</span>
        <Input
          autoFocus
          id={nameInputId}
          maxLength={120}
          onChange={setName}
          placeholder={t("System key name placeholder")}
          prefix={<KeyRound size={14} />}
          showClear
          value={name}
        />
      </label>
    </Modal>

    <Modal
      closable={false}
      footer={<Button onClick={() => setCreatedKey(null)} theme="solid" type="primary">{t("I copied the key")}</Button>}
      maskClosable={false}
      title={t("System key created")}
      visible={createdKey !== null}
    >
      <p role="alert" className="mb-3 text-sm text-[var(--semi-color-warning)]">
        {t("System key shown once warning")}
      </p>
      {createdKey?.keyPlain ? <div className="rounded-md border border-[var(--semi-color-border)] bg-[var(--semi-color-fill-0)] p-3">
        <Text
          className="break-all font-mono-data"
          copyable={createCopyableConfig(createdKey.keyPlain, t("Copied"))}
        >{createdKey.keyPlain}</Text>
      </div> : null}
    </Modal>
  </>;
}
