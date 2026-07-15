import { Empty } from "@douyinfe/semi-ui";
import {
  IllustrationNoResult,
  IllustrationNoResultDark,
} from "@douyinfe/semi-illustrations";

// Small shared UI helpers only. Feature panels keep their own copies of any
// larger component so invites and card keys can diverge independently later.

export async function copyText(value: string) {
  if (typeof navigator !== "undefined" && navigator.clipboard?.writeText) {
    await navigator.clipboard.writeText(value);
    return;
  }
  const textarea = document.createElement("textarea");
  textarea.value = value;
  textarea.setAttribute("readonly", "true");
  textarea.style.position = "fixed";
  textarea.style.left = "-9999px";
  document.body.appendChild(textarea);
  textarea.select();
  document.execCommand("copy");
  document.body.removeChild(textarea);
}

export function emptyNode(description: string) {
  return (
    <Empty
      darkModeImage={
        <IllustrationNoResultDark style={{ height: 150, width: 150 }} />
      }
      description={description}
      image={<IllustrationNoResult style={{ height: 150, width: 150 }} />}
      style={{ padding: 30 }}
    />
  );
}
