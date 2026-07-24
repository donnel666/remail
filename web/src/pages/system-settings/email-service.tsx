import type { SectionProps } from "./index";
import AllocationSection from "./allocation";
import EmailResourceSection from "./email-resources";
import MailDeliverySection from "./mail-delivery";
import MailmatchSection from "./mailmatch";
import MicrosoftOpsSection from "./microsoft-ops";
import ProxySection from "./proxy-network";

export default function EmailServiceSection(props: SectionProps) {
  return <div className="space-y-6">
    <EmailResourceSection {...props} />
    <AllocationSection {...props} />
    <MailmatchSection {...props} />
    <MicrosoftOpsSection {...props} />
    <ProxySection {...props} />
    <MailDeliverySection {...props} />
  </div>;
}
