import type { TFunction } from "i18next";
import { IamApiError } from "./iam-api";

export function getIamErrorMessage(
  t: TFunction,
  error: unknown,
  fallbackKey = "Request failed."
) {
  if (error instanceof IamApiError && error.message) {
    return t(error.message);
  }
  if (error instanceof Error && error.message) {
    return error.message;
  }
  return t(fallbackKey);
}
