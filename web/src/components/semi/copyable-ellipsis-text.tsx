import { Toast, Typography } from "@douyinfe/semi-ui";
import { type KeyboardEvent, type MouseEvent } from "react";
import { useTranslation } from "react-i18next";

import { copyText } from "@/lib/clipboard";
import { cn } from "@/lib/utils";

const { Text } = Typography;

export function mailExtractionLabelKey(
  value: string,
  fallback = "Verification code"
) {
  try {
    const protocol = new URL(value.trim()).protocol;
    if (protocol === "http:" || protocol === "https:") return "Email URL";
  } catch {
    // Not a URL; keep the verification-code label.
  }
  return fallback;
}

export function CopyableEllipsisText({
  className,
  text,
}: {
  className?: string;
  text: string;
}) {
  const { t } = useTranslation();

  const copy = async () => {
    try {
      await copyText(text);
      Toast.success(t("Copied"));
    } catch {
      Toast.error(t("Copy failed."));
    }
  };

  const handleClick = (event: MouseEvent<HTMLSpanElement>) => {
    event.stopPropagation();
    void copy();
  };

  const handleKeyDown = (event: KeyboardEvent<HTMLSpanElement>) => {
    if (event.key !== "Enter" && event.key !== " ") return;
    event.preventDefault();
    event.stopPropagation();
    void copy();
  };

  return (
    <Text
      aria-label={`${t("Copy")}: ${text}`}
      className={cn("remail-copyable-ellipsis-text", className)}
      ellipsis={{ rows: 1, showTooltip: true }}
      onClick={handleClick}
      onKeyDown={handleKeyDown}
      role="button"
      tabIndex={0}
    >
      {text}
    </Text>
  );
}
