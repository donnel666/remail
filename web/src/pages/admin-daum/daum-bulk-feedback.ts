import { Toast } from "@douyinfe/semi-ui";
import type { TFunction } from "i18next";
import { isDaumBulkOutcomeUnconfirmed } from "@/lib/daum-bulk-session";
import { getIamErrorMessage } from "@/lib/iam-errors";
import type { AdminDaumBulkResponse } from "./admin-daum-types";

export function showAdminDaumBulkError(t: TFunction, error: unknown) {
  if (isDaumBulkOutcomeUnconfirmed(error)) {
    Toast.warning(t("Daum bulk operation outcome is not confirmed. Retry to confirm."));
  } else {
    Toast.error(getIamErrorMessage(t, error, "Daum resource operation failed."));
  }
}

export function showAdminDaumBulkOutcome(
  t: TFunction,
  response: AdminDaumBulkResponse,
  successKey: string,
  submittedKey = "Daum resources bulk operation submitted."
) {
  const reasons = (response.reasonCounts ?? []).map((item) => `${item.reason}: ${item.count}`).join(", ");
  const summary = `${t("Processed")}: ${response.processed ?? response.affected + response.skipped}/${response.requested}` +
    ` · ${t("Succeeded")}: ${response.affected} · ${t("Skipped")}: ${response.skipped}` +
    (reasons ? ` · ${t("Reason")}: ${reasons}` : "");
  if (response.status === "failed") {
    Toast.error(`${t("Daum resource operation failed.")} ${summary}`);
    return;
  }
  if (response.taskId && (response.status === "queued" || response.status === "running")) {
    Toast.success(t(submittedKey, { count: response.requested }));
    return;
  }
  if (response.taskId && response.status !== "succeeded") {
    Toast.warning(t("Daum bulk operation outcome is not confirmed. Retry to confirm."));
    return;
  }
  Toast.success(t(successKey, { count: response.affected }));
  if (response.skipped > 0) Toast.warning(summary);
}
