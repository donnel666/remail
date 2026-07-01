import type { ReactNode } from "react";
import { Tag, Tooltip } from "@douyinfe/semi-ui";

import { isAvailable, type ResourceStatus } from "./model";

export function renderStatusTag(
  status: ResourceStatus,
  t: (key: string) => string,
  reason?: string
) {
  let tag: ReactNode;

  if (isAvailable(status)) {
    tag = (
      <Tag color="green" shape="circle" size="small">
        {t("Available")}
      </Tag>
    );
  } else if (status === "pending_validation") {
    tag = (
      <Tag color="orange" shape="circle" size="small">
        {t("Pending validation")}
      </Tag>
    );
  } else {
    tag = (
      <Tag color="grey" shape="circle" size="small">
        {t("Disabled")}
      </Tag>
    );
  }

  if (!isAvailable(status) && status !== "pending_validation" && reason) {
    return (
      <Tooltip
        content={reason}
        mouseEnterDelay={0}
        mouseLeaveDelay={0.05}
        position="top"
      >
        <span className="inline-flex">{tag}</span>
      </Tooltip>
    );
  }

  return tag;
}
