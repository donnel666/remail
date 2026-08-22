import type { TFunction } from "i18next";
import { IamApiError } from "./iam-api";

interface ApiErrorMessageBody {
  message?: string | null;
}

function translateErrorMessage(
  t: TFunction,
  message: string | null | undefined,
  fallback: string,
) {
  return message ? t(message, { defaultValue: fallback }) : fallback;
}

function fallbackMessage(
  t: TFunction,
  error: unknown,
  fallbackKey: string,
) {
  if (error instanceof IamApiError) {
    if (error.status === 401) return t("Authentication is required.");
    if (error.status === 403) return t("Permission denied.");
    if (error.status === 429) {
      return error.retryAfterSeconds
        ? t("Please retry in {{seconds}} seconds.", {
            seconds: error.retryAfterSeconds,
          })
        : t("Too many requests.");
    }
    if (error.status >= 500) return t("Service is temporarily unavailable.");
  }
  return t(fallbackKey);
}

function lotteryCodeMessage(t: TFunction, error: IamApiError) {
  switch (error.code) {
    case "lottery_account_age": {
      const days = error.fields?.requiredDays;
      return days
        ? t("Lottery account must be at least {{days}} days old.", { days })
        : t("Lottery account age requirement not met.");
    }
    case "lottery_account_inactive":
      return t("Lottery account is not active.");
    case "lottery_creator":
      return t("The lottery creator cannot enter this activity.");
    case "lottery_full":
      return t("Lottery entry limit has been reached.");
    case "lottery_closed":
      return t("Lottery entry is closed.");
    case "lottery_already_entered":
      return t("You have already entered this lottery.");
    default:
      return undefined;
  }
}

export function getApiErrorBodyMessage(
  t: TFunction,
  error: ApiErrorMessageBody | null | undefined,
  fallbackKey = "Request failed.",
) {
  return translateErrorMessage(t, error?.message, t(fallbackKey));
}

export function getIamErrorMessage(
  t: TFunction,
  error: unknown,
  fallbackKey = "Request failed.",
) {
  const fallback = fallbackMessage(t, error, fallbackKey);
  if (error instanceof IamApiError) {
    const coded = lotteryCodeMessage(t, error);
    if (coded) return coded;
  }
  let message = error instanceof Error ? error.message : undefined;
  if (error instanceof IamApiError && message === "Request failed.") {
    message = undefined;
  }
  return translateErrorMessage(t, message, fallback);
}
