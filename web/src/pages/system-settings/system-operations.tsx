import type { SectionProps } from "./index";
import AdminMonitorSection from "./admin-monitor";
import BackgroundJobSection from "./background-jobs";
import BatchDataSection from "./batch-data";

export default function SystemOperationsSection(props: SectionProps) {
  return <div className="space-y-6">
    <BackgroundJobSection {...props} />
    <BatchDataSection {...props} />
    <AdminMonitorSection {...props} />
  </div>;
}
