import { Card, Typography } from "@douyinfe/semi-ui";
import type { ReactNode } from "react";

const { Text, Title } = Typography;

interface SettingItemProps {
  action?: ReactNode;
  description: ReactNode;
  icon: ReactNode;
  iconTone?: "slate" | "orange" | "violet" | "green";
  title: ReactNode;
}

export function SettingItem({
  action,
  description,
  icon,
  iconTone = "slate",
  title,
}: SettingItemProps) {
  return (
    <Card className="account-setting-item !rounded-xl">
      <div className="account-setting-item-inner">
        <div className="account-setting-item-main">
          <div className={`account-setting-icon is-${iconTone}`}>{icon}</div>
          <div className="account-setting-copy">
            <Title heading={6}>{title}</Title>
            <Text type="tertiary">{description}</Text>
          </div>
        </div>
        {action ? <div className="account-setting-action">{action}</div> : null}
      </div>
    </Card>
  );
}
