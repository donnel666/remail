import { useEffect, useState } from "react";
import { Popover, Toast } from "@douyinfe/semi-ui";
import { Copy, ExternalLink, Headphones } from "lucide-react";
import { useTranslation } from "react-i18next";
import { SiQq, SiTelegram } from "react-icons/si";

import { copyText } from "@/lib/clipboard";
import {
  CUSTOMER_SERVICE_UPDATED_EVENT,
  getCustomerService,
  type CustomerServiceConfig,
} from "@/lib/system-settings-api";

export function CustomerServiceFloat() {
  const { t } = useTranslation();
  const [config, setConfig] = useState<CustomerServiceConfig | null>(null);
  const [visible, setVisible] = useState(false);

  useEffect(() => {
    let controller: AbortController | undefined;
    const load = () => {
      controller?.abort();
      controller = new AbortController();
      setConfig(null);
      setVisible(false);
      void getCustomerService(controller.signal)
        .then(setConfig)
        .catch(() => undefined);
    };

    load();
    window.addEventListener(CUSTOMER_SERVICE_UPDATED_EVENT, load);
    return () => {
      controller?.abort();
      window.removeEventListener(CUSTOMER_SERVICE_UPDATED_EVENT, load);
    };
  }, []);

  const qqGroupNumber = config?.qqGroupNumber.trim() ?? "";
  const qqGroupUrl = config?.qqGroupUrl.trim() ?? "";
  const telegramGroupUrl = config?.telegramGroupUrl.trim() ?? "";
  if (!qqGroupNumber && !qqGroupUrl && !telegramGroupUrl) return null;

  const copyQQGroupNumber = async () => {
    try {
      await copyText(qqGroupNumber);
      Toast.success(t("Copied"));
    } catch {
      Toast.error(t("Copy failed."));
    }
  };

  const actionClassName = "flex h-11 w-11 shrink-0 cursor-pointer items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-surface-sunken hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand";
  const content = (
    <section id="customer-service-panel" role="dialog" aria-label={t("联系客服")} className="w-[min(288px,calc(100vw-32px))] p-1">
      <header className="border-b border-border px-3 py-2.5">
        <h2 className="text-sm font-semibold text-foreground">{t("联系客服")}</h2>
      </header>
      <div className="divide-y divide-border">
        {(qqGroupNumber || qqGroupUrl) ? (
          <div className="flex min-h-16 items-center gap-3 px-3 py-2">
            <SiQq aria-hidden className="size-5 shrink-0 text-[#12B7F5]" />
            <div className="min-w-0 flex-1">
              <div className="text-sm font-medium text-foreground">{t("QQ群")}</div>
              <div className="truncate text-xs text-muted-foreground">{qqGroupNumber || t("加入群聊")}</div>
            </div>
            {qqGroupNumber ? (
              <button type="button" className={actionClassName} aria-label={t("复制QQ群号")} title={t("复制QQ群号")} onClick={() => void copyQQGroupNumber()}>
                <Copy aria-hidden className="size-4" />
              </button>
            ) : null}
            {qqGroupUrl ? (
              <a className={actionClassName} aria-label={t("打开QQ群")} title={t("打开QQ群")} href={qqGroupUrl} target="_blank" rel="noreferrer" onClick={() => setVisible(false)}>
                <ExternalLink aria-hidden className="size-4" />
              </a>
            ) : null}
          </div>
        ) : null}
        {telegramGroupUrl ? (
          <div className="flex min-h-16 items-center gap-3 px-3 py-2">
            <SiTelegram aria-hidden className="size-5 shrink-0 text-[#229ED9]" />
            <div className="min-w-0 flex-1">
              <div className="text-sm font-medium text-foreground">{t("Telegram 群")}</div>
              <div className="text-xs text-muted-foreground">{t("加入群聊")}</div>
            </div>
            <a className={actionClassName} aria-label={t("打开 Telegram 群")} title={t("打开 Telegram 群")} href={telegramGroupUrl} target="_blank" rel="noreferrer" onClick={() => setVisible(false)}>
              <ExternalLink aria-hidden className="size-4" />
            </a>
          </div>
        ) : null}
      </div>
    </section>
  );

  return (
    <div className="fixed bottom-5 right-4 z-30 lg:bottom-auto lg:right-5 lg:top-1/2 lg:-translate-y-1/2">
      <Popover
        closeOnEsc
        content={content}
        guardFocus
        onClickOutSide={() => setVisible(false)}
        onEscKeyDown={() => setVisible(false)}
        onVisibleChange={setVisible}
        position="left"
        returnFocusOnClose
        showArrow
        trigger="click"
        visible={visible}
        zIndex={50}
      >
        <button
          type="button"
          aria-controls="customer-service-panel"
          aria-expanded={visible}
          aria-haspopup="dialog"
          aria-label={t("联系客服")}
          className="flex h-12 w-12 cursor-pointer items-center justify-center rounded-full border-0 bg-brand text-white shadow-lg transition-colors hover:bg-brand-hover active:bg-brand-active focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand focus-visible:ring-offset-2"
          onClick={() => setVisible((current) => !current)}
        >
          <Headphones aria-hidden className="size-5" />
        </button>
      </Popover>
    </div>
  );
}
