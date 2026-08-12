import type { SectionProps } from "./index";
import AdminMonitorSection from "./admin-monitor";
import BackgroundJobSection from "./background-jobs";
import BatchDataSection from "./batch-data";
import InventoryRefreshSection from "./inventory-refresh";

export default function SystemOperationsSection(props: SectionProps) {
  return <div className="space-y-6">
    <InventoryRefreshSection canWrite={props.canWrite} />
    <BackgroundJobSection {...props} />
    <BatchDataSection {...props} />
    <AdminMonitorSection {...props} />
  </div>;
}
