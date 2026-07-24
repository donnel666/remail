import type { SectionProps } from "./index";
import InviteRebateSection from "./invite-rebate";
import UserGroupSection from "./user-groups";
import { SettingsAccessBoundary } from "./settings-layout";
import { useTranslation } from "react-i18next";

export default function UsersRebatesSection(props: SectionProps) {
  const { t } = useTranslation();
  return <div className="space-y-6">
    <SettingsAccessBoundary canWrite={props.canWrite}>
      <InviteRebateSection {...props} />
    </SettingsAccessBoundary>
    {props.canReadUserGroups ? (
      <SettingsAccessBoundary canWrite={props.canWriteUserGroups}>
        <UserGroupSection {...props} />
      </SettingsAccessBoundary>
    ) : (
      <p className="py-8 text-center text-sm text-[var(--semi-color-text-2)]">{t("需要用户分组读取权限。")}</p>
    )}
  </div>;
}
