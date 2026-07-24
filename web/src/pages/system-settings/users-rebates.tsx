import type { SectionProps } from "./index";
import InviteRebateSection from "./invite-rebate";
import UserGroupSection from "./user-groups";

export default function UsersRebatesSection(props: SectionProps) {
  return <div className="space-y-6">
    <InviteRebateSection {...props} />
    <UserGroupSection {...props} />
  </div>;
}
