import { useEffect, useState } from "react";
import { Input, Modal, Select, TextArea, Toast } from "@douyinfe/semi-ui";
import { useTranslation } from "react-i18next";

import { requireTurnstile } from "@/components/auth/TurnstileGate";
import { getIamErrorMessage } from "@/lib/iam-errors";
import {
  createProjectApplication,
  resubmitProjectApplication,
  type CreateProjectApplicationRequest,
  type ProjectDetailResponse,
} from "@/lib/projects-api";

export type ApplyModalMode = "create" | "resubmit";

const mailBodyMaxLength = 10000;
const projectDescriptionMaxLength = 1000;

export interface ApplyFormState {
  codeMaxPrice: string;
  codeMinPrice: string;
  emailTypes: string[];
  mailBody: string;
  mailSubject: string;
  projectName: string;
  projectURL: string;
  purchaseMaxPrice: string;
  purchaseMinPrice: string;
  remarks: string;
  senderPattern: string;
}

export const initialApplyForm: ApplyFormState = {
  codeMaxPrice: "",
  codeMinPrice: "",
  emailTypes: ["microsoft"],
  mailBody: "",
  mailSubject: "",
  projectName: "",
  projectURL: "",
  purchaseMaxPrice: "",
  purchaseMinPrice: "",
  remarks: "",
  senderPattern: "",
};

export function productTypeLabel(type: string, t: (key: string) => string) {
  if (type === "microsoft") return t("Microsoft email");
  if (type === "domain") return t("Domain email");
  if (type === "random") return t("Random email");
  if (type === "gmail") return t("Gmail email");
  if (type === "icloud") return t("iCloud email");
  return type;
}

function extractTargetPlatform(projectURL: string, projectName: string) {
  const value = projectURL.trim();
  if (!value) return projectName.trim();
  try {
    const parsed = new URL(value.startsWith("http") ? value : `https://${value}`);
    return parsed.hostname.replace(/^www\./, "");
  } catch {
    return value.replace(/^https?:\/\//, "").replace(/^www\./, "").split("/")[0];
  }
}

function buildProjectApplicationPayload(
  form: ApplyFormState,
  t: (key: string) => string
): CreateProjectApplicationRequest {
  const targetPlatform = extractTargetPlatform(form.projectURL, form.projectName);
  const description = [
    `${t("Project URL")}: ${form.projectURL.trim()}`,
    `${t("Expected email types")}: ${form.emailTypes.map((type) => productTypeLabel(type, t)).join(", ")}`,
    form.codeMinPrice || form.codeMaxPrice
      ? `${t("Code price expectation")}: ${form.codeMinPrice || "-"} - ${form.codeMaxPrice || "-"}`
      : "",
    form.purchaseMinPrice || form.purchaseMaxPrice
      ? `${t("Purchase price expectation")}: ${form.purchaseMinPrice || "-"} - ${form.purchaseMaxPrice || "-"}`
      : "",
    form.remarks.trim() ? `${t("Remarks")}: ${form.remarks.trim()}` : "",
  ]
    .filter(Boolean)
    .join("\n");

  return {
    accessType: "public",
    description,
    looseMatch: true,
    mailRules: [
      { enabled: true, pattern: form.senderPattern.trim(), ruleType: "sender" },
      { enabled: true, pattern: "exact", ruleType: "recipient" },
      { enabled: true, pattern: form.mailSubject.trim(), ruleType: "subject" },
      { enabled: true, pattern: form.mailBody.trim(), ruleType: "body" },
    ],
    name: form.projectName.trim(),
    targetPlatform,
  };
}

export function ApplyProjectModal({
  initialValue,
  mode = "create",
  onCancel,
  onSuccess,
  projectId,
  visible,
}: {
  initialValue?: ApplyFormState;
  mode?: ApplyModalMode;
  onCancel: () => void;
  onSuccess: (detail: ProjectDetailResponse) => void;
  projectId?: number;
  visible: boolean;
}) {
  const { t } = useTranslation();
  const [form, setForm] = useState<ApplyFormState>(initialApplyForm);
  const [submitting, setSubmitting] = useState(false);

  useEffect(() => {
    if (visible) setForm(initialValue ?? initialApplyForm);
  }, [initialValue, visible]);

  const setField = <K extends keyof ApplyFormState>(key: K, value: ApplyFormState[K]) => {
    setForm((previous) => ({ ...previous, [key]: value }));
  };

  const submit = async () => {
    if (
      !form.projectName.trim() ||
      !form.projectURL.trim() ||
      !form.senderPattern.trim() ||
      !form.mailSubject.trim() ||
      !form.mailBody.trim() ||
      form.emailTypes.length === 0
    ) {
      Toast.error(t("Please complete required project application fields."));
      return;
    }
    if (Array.from(form.mailBody.trim()).length > mailBodyMaxLength) {
      Toast.error(t("Mail body is too long."));
      return;
    }
    if (mode === "resubmit" && !projectId) {
      Toast.error(t("Project application resubmit failed."));
      return;
    }

    const payload = buildProjectApplicationPayload(form, t);
    if (Array.from(payload.description ?? "").length > projectDescriptionMaxLength) {
      Toast.error(t("Project application details are too long."));
      return;
    }

    setSubmitting(true);
    try {
      const isResubmit = mode === "resubmit" && projectId;
      const turnstileToken = await requireTurnstile(
        isResubmit ? "project_resubmit" : "project_submit"
      );
      if (!turnstileToken) return;
      const response = isResubmit
        ? await resubmitProjectApplication(projectId, payload, turnstileToken)
        : await createProjectApplication(payload, turnstileToken);
      Toast.success(
        mode === "resubmit"
          ? t("Project application resubmitted.")
          : t("Project application submitted.")
      );
      onSuccess(response);
      onCancel();
    } catch (error) {
      Toast.error(
        getIamErrorMessage(
          t,
          error,
          mode === "resubmit"
            ? "Project application resubmit failed."
            : "Project application failed."
        )
      );
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Modal
      cancelText={t("Cancel")}
      confirmLoading={submitting}
      okText={mode === "resubmit" ? t("Resubmit application") : t("Submit application")}
      onCancel={onCancel}
      onOk={() => void submit()}
      title={mode === "resubmit" ? t("Resubmit project application") : t("Apply project")}
      visible={visible}
      width="min(666px, calc(100vw - 32px))"
    >
      <div className="grid gap-4">
        <label className="block">
          <span className="mb-1 block text-xs font-medium text-[var(--semi-color-text-1)]">
            {t("Project name")} *
          </span>
          <Input
            maxLength={120}
            onChange={(value) => setField("projectName", String(value))}
            placeholder={t("Project name placeholder")}
            value={form.projectName}
          />
        </label>

        <label className="block">
          <span className="mb-1 block text-xs font-medium text-[var(--semi-color-text-1)]">
            {t("Project URL")} *
          </span>
          <Input
            maxLength={300}
            onChange={(value) => setField("projectURL", String(value))}
            placeholder="https://example.com"
            value={form.projectURL}
          />
        </label>

        <label className="block">
          <span className="mb-1 block text-xs font-medium text-[var(--semi-color-text-1)]">
            {t("Sender email")} *
          </span>
          <Input
            maxLength={500}
            onChange={(value) => setField("senderPattern", String(value))}
            placeholder="noreply@example.com"
            value={form.senderPattern}
          />
        </label>

        <label className="block">
          <span className="mb-1 block text-xs font-medium text-[var(--semi-color-text-1)]">
            {t("Mail subject")} *
          </span>
          <Input
            maxLength={500}
            onChange={(value) => setField("mailSubject", String(value))}
            placeholder={t("Mail subject placeholder")}
            value={form.mailSubject}
          />
        </label>

        <label className="block">
          <span className="mb-1 block text-xs font-medium text-[var(--semi-color-text-1)]">
            {t("Mail body")} *
          </span>
          <TextArea
            maxCount={mailBodyMaxLength}
            maxLength={mailBodyMaxLength}
            onChange={(value) => setField("mailBody", String(value))}
            placeholder={t("Mail body placeholder")}
            rows={3}
            value={form.mailBody}
          />
        </label>

        <label className="block">
          <span className="mb-1 block text-xs font-medium text-[var(--semi-color-text-1)]">
            {t("Expected email types")} *
          </span>
          <Select
            multiple
            onChange={(value) =>
              setField(
                "emailTypes",
                Array.isArray(value) ? value.map(String) : ["microsoft"]
              )
            }
            style={{ width: "100%" }}
            value={form.emailTypes}
          >
            <Select.Option value="microsoft">{t("Microsoft email")}</Select.Option>
            <Select.Option value="domain">{t("Domain email")}</Select.Option>
            <Select.Option value="gmail">{t("Gmail email")}</Select.Option>
            <Select.Option value="icloud">{t("iCloud email")}</Select.Option>
          </Select>
        </label>

        <div className="grid gap-3 sm:grid-cols-2">
          {[
            ["codeMinPrice", t("Code min price")],
            ["codeMaxPrice", t("Code max price")],
            ["purchaseMinPrice", t("Purchase min price")],
            ["purchaseMaxPrice", t("Purchase max price")],
          ].map(([key, label]) => (
            <label className="block" key={key}>
              <span className="mb-1 block text-xs font-medium text-[var(--semi-color-text-1)]">
                {label}
              </span>
              <Input
                onChange={(value) =>
                  setField(key as keyof ApplyFormState, String(value) as never)
                }
                placeholder="0.00"
                value={form[key as keyof ApplyFormState] as string}
              />
            </label>
          ))}
        </div>

        <label className="block">
          <span className="mb-1 block text-xs font-medium text-[var(--semi-color-text-1)]">
            {t("Remarks")}
          </span>
          <TextArea
            maxCount={500}
            onChange={(value) => setField("remarks", String(value))}
            rows={3}
            value={form.remarks}
          />
        </label>
      </div>
    </Modal>
  );
}
