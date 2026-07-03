import { Toast, Typography } from "@douyinfe/semi-ui";

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
        copyable={{
          content: copyContent ?? text,
          onCopy: () => Toast.success(copiedText),
        }}
      >
        <span className="remail-copyable-table-text-content">{text}</span>
      </Text>
    </span>
  );
}
