import { useEffect, useState } from "react";
import {
  DatePicker,
  InputNumber,
  Modal,
  TextArea,
  Toast,
} from "@douyinfe/semi-ui";
import { useTranslation } from "react-i18next";

import { getIamErrorMessage } from "@/lib/iam-errors";
import {
  createMockFinanceCardKeys,
  updateMockFinanceCardKey,
  type FinanceCardKey,
} from "./admin-finance-mock";
import { formatMoney } from "./finance-meta";

interface CreateCardKeyModalProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onCreated: (count: number, keys: string[]) => void;
}

export function CreateCardKeyModal({
  open,
  onOpenChange,
  onCreated,
}: CreateCardKeyModalProps) {
  const { t } = useTranslation();
  const [amount, setAmount] = useState<number | string>(100);
  const [count, setCount] = useState<number | string>(1);
  const [maxRedemptions, setMaxRedemptions] = useState<number | string>(1);
  const [expireAt, setExpireAt] = useState<Date | null>(null);
  const [customKeys, setCustomKeys] = useState("");
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    if (!open) return;
    setAmount(100);
    setCount(1);
    setMaxRedemptions(1);
    setExpireAt(null);
    setCustomKeys("");
  }, [open]);

  const submit = async () => {
    const amountValue = Number(amount);
    const countValue = Number(count);
    const maxRedemptionsValue = Number(maxRedemptions);
    const cardKeys = customKeys
      .split(/[\n,;\s]+/)
      .map((item) => item.trim())
      .filter(Boolean);

    if (!Number.isFinite(amountValue) || amountValue <= 0) {
      Toast.warning(t("Amount must be positive."));
      return;
    }
    if (!cardKeys.length && (!Number.isFinite(countValue) || countValue < 1)) {
      Toast.warning(t("Count must be at least 1."));
      return;
    }
    if (!Number.isFinite(maxRedemptionsValue) || maxRedemptionsValue < 1) {
      Toast.warning(t("Max redemptions must be at least 1."));
      return;
    }

    setSaving(true);
    try {
      const result = await createMockFinanceCardKeys({
        amount: amountValue.toFixed(6),
        count: cardKeys.length ? undefined : countValue,
        maxRedemptions: maxRedemptionsValue,
        expireAt: expireAt ? expireAt.toISOString() : null,
        cardKeys: cardKeys.length ? cardKeys : undefined,
      });
      Toast.success(t("Card keys created.", { count: result.created }));
      onCreated(
        result.created,
        result.items.map((item) => item.key)
      );
      onOpenChange(false);
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Operation failed."));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal
      centered
      confirmLoading={saving}
      onCancel={() => onOpenChange(false)}
      onOk={() => void submit()}
      okText={t("Create")}
      cancelText={t("Cancel")}
      style={{ width: 620 }}
      title={t("Create card keys")}
      visible={open}
    >
      <div className="space-y-4 py-1">
        <div className="rounded-lg bg-[var(--semi-color-fill-0)] px-3 py-2 text-xs text-[var(--semi-color-text-2)]">
          {t("Create card keys hint")}
        </div>
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
          <label className="block">
            <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
              {t("Amount")} *
            </span>
            <InputNumber
              min={0.01}
              onChange={setAmount}
              precision={6}
              prefix="¥"
              style={{ width: "100%" }}
              value={amount}
            />
          </label>
          <label className="block">
            <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
              {t("Count")}
            </span>
            <InputNumber
              max={1000}
              min={1}
              onChange={setCount}
              style={{ width: "100%" }}
              value={count}
            />
          </label>
          <label className="block">
            <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
              {t("Max redemptions")} *
            </span>
            <InputNumber
              min={1}
              onChange={setMaxRedemptions}
              style={{ width: "100%" }}
              value={maxRedemptions}
            />
          </label>
        </div>
        <label className="block">
          <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
            {t("Expire at")}
          </span>
          <DatePicker
            onChange={(value) =>
              setExpireAt(value instanceof Date ? value : null)
            }
            showClear
            style={{ width: "100%" }}
            type="dateTime"
            value={expireAt ?? undefined}
          />
        </label>
        <label className="block">
          <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
            {t("Custom card keys")}
          </span>
          <TextArea
            onChange={(value) => setCustomKeys(String(value))}
            placeholder={t("Custom card keys placeholder")}
            rows={4}
            value={customKeys}
          />
          <div className="mt-1 text-xs text-[var(--semi-color-text-2)]">
            {t("Custom card keys override count and auto-generation.")}
          </div>
        </label>
      </div>
    </Modal>
  );
}

export function EditCardKeyModal({
  card,
  onClose,
  onSaved,
}: {
  card: FinanceCardKey | null;
  onClose: () => void;
  onSaved: (card: FinanceCardKey) => void;
}) {
  const { t } = useTranslation();
  const [maxRedemptions, setMaxRedemptions] = useState<number | string>(1);
  const [expireAt, setExpireAt] = useState<Date | null>(null);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    if (!card) return;
    setMaxRedemptions(card.maxRedemptions);
    setExpireAt(card.expireAt ? new Date(card.expireAt) : null);
  }, [card]);

  const submit = async () => {
    if (!card) return;
    const parsedMaxRedemptions = Number(maxRedemptions);
    if (!Number.isFinite(parsedMaxRedemptions) || parsedMaxRedemptions < 1) {
      Toast.warning(t("Max redemptions must be at least 1."));
      return;
    }
    setSaving(true);
    try {
      const updated = await updateMockFinanceCardKey(card.key, {
        maxRedemptions: parsedMaxRedemptions,
        expireAt: expireAt ? expireAt.toISOString() : null,
      });
      Toast.success(t("Card key updated."));
      onSaved(updated);
      onClose();
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Operation failed."));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal
      centered
      confirmLoading={saving}
      onCancel={onClose}
      onOk={() => void submit()}
      okText={t("Save")}
      cancelText={t("Cancel")}
      style={{ width: 520 }}
      title={t("Edit card key")}
      visible={Boolean(card)}
    >
      <div className="space-y-4 py-1">
        <div className="rounded-lg bg-[var(--semi-color-fill-0)] px-3 py-2 text-sm">
          <div>
            <span className="text-[var(--semi-color-text-2)]">{t("Card key")}</span>
            <span className="ml-2 font-mono-data font-semibold">{card?.key}</span>
          </div>
          <div className="mt-1">
            <span className="text-[var(--semi-color-text-2)]">{t("Amount")}</span>
            <span className="ml-2 font-mono-data">¥{formatMoney(card?.amount)}</span>
          </div>
        </div>
        <label className="block">
          <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
            {t("Max redemptions")} *
          </span>
          <InputNumber
            min={1}
            onChange={setMaxRedemptions}
            style={{ width: "100%" }}
            value={maxRedemptions}
          />
        </label>
        <label className="block">
          <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
            {t("Expire at")}
          </span>
          <DatePicker
            onChange={(value) =>
              setExpireAt(value instanceof Date ? value : null)
            }
            showClear
            style={{ width: "100%" }}
            type="dateTime"
            value={expireAt ?? undefined}
          />
        </label>
      </div>
    </Modal>
  );
}
