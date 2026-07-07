import { IconTick } from "@douyinfe/semi-icons";
import { Toast } from "@douyinfe/semi-ui";

export function createCopyableConfig(content: string, copiedText: string) {
  return {
    content,
    onCopy: () => Toast.success(copiedText),
    successTip: <IconTick className="remail-copy-success-icon" />,
  };
}
