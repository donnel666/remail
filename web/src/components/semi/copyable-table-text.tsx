import { Typography } from "@douyinfe/semi-ui";

import { createCopyableConfig } from "./copyable-config";
import { OverflowTooltip } from "./overflow-tooltip";

const { Text } = Typography;

interface CopyableTableTextProps {
  copiedText: string;
  copyContent?: string;
  text: string;
}

export function CopyableTableText({
  copiedText,
  copyContent,
  text,
}: CopyableTableTextProps) {
  return (
    <span className="remail-copyable-table-text">
      <Text
        copyable={createCopyableConfig(copyContent ?? text, copiedText)}
      >
        <OverflowTooltip
          className="remail-copyable-table-text-content"
          content={text}
        >
          {text}
        </OverflowTooltip>
      </Text>
    </span>
  );
}
