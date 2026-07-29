import { useEffect, useRef, useState } from "react";
import { Modal } from "@douyinfe/semi-ui";
import { useLocation } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";

import { useAuth } from "@/context/auth-provider";
import { formatPoints } from "@/lib/points";
import { claimDailyCheckin, type DailyCheckinResponse } from "@/lib/wallet-api";

export default function DailyCheckin() {
  const { t } = useTranslation();
  const { currentUser } = useAuth();
  const userID = currentUser?.id ?? null;
  const pathname = useLocation().pathname;
  const checkedUsers = useRef(new Set<number>());
  const inFlightUsers = useRef(new Set<number>());
  const activeUser = useRef<number | null>(null);
  const previousPath = useRef(pathname);
  const [result, setResult] = useState<DailyCheckinResponse | null>(null);
  activeUser.current = userID;

  useEffect(() => {
    const enteredConsole = pathname === "/console" && previousPath.current !== "/console";
    previousPath.current = pathname;
    if (userID === null) {
      checkedUsers.current.clear();
      setResult(null);
      return;
    }
    const firstOpen = !checkedUsers.current.has(userID);
    if (!firstOpen && !enteredConsole) return;
    if (inFlightUsers.current.has(userID)) return;
    inFlightUsers.current.add(userID);
    void claimDailyCheckin().then((response) => {
      inFlightUsers.current.delete(userID);
      if (activeUser.current !== userID) return;
      checkedUsers.current.add(userID);
      if (response.enabled && response.firstClaim) setResult(response);
    }).catch(() => { inFlightUsers.current.delete(userID); });
  }, [userID, pathname]);

  const amount = Number(result?.rewardAmount ?? 0);
  return <Modal
    cancelButtonProps={{ style: { display: "none" } }}
    okText={t("知道了")}
    onCancel={() => setResult(null)}
    onOk={() => setResult(null)}
    title={t("每日签到")}
    visible={result !== null}
  >
    <p className="py-2 text-base text-[var(--semi-color-text-0)]">
      {amount > 0
        ? t("签到成功，获得奖励 {{amount}}", { amount: formatPoints(result?.rewardAmount) })
        : t("签到成功，本次未获得积分奖励")}
    </p>
  </Modal>;
}
