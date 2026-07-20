import type { ReactNode } from "react";
import { Tag, Tooltip } from "@douyinfe/semi-ui";

import type { ResourceStatus } from "./model";

export function renderStatusTag(
  status: ResourceStatus,
  t: (key: string) => string,
  reason?: string
) {
  let tag: ReactNode;

  if (status === "normal") {
    tag = (
      <Tag color="green" shape="circle" size="small">
        {t("Normal")}
      </Tag>
    );
  } else if (status === "pending") {
    tag = (
      <Tag color="blue" shape="circle" size="small">
        {t("Pending")}
      </Tag>
    );
  } else if (status === "validating") {
    tag = (
      <Tag color="orange" shape="circle" size="small">
        {t("Validating")}
      </Tag>
    );
  } else if (status === "identifying") {
    tag = (
      <Tag color="blue" shape="circle" size="small">
        {t("Identifying")}
      </Tag>
    );
  } else if (status === "abnormal") {
    tag = (
      <Tag color="red" shape="circle" size="small">
        {t("Abnormal")}
      </Tag>
    );
  } else {
    tag = (
      <Tag color="grey" shape="circle" size="small">
        {t("Disabled")}
      </Tag>
    );
  }

  if (status === "abnormal" && reason) {
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
