import type { CSSProperties, ReactNode } from "react";
import { useState } from "react";
import { Button, Card, Divider } from "@douyinfe/semi-ui";
import { IconEyeClosed, IconEyeOpened } from "@douyinfe/semi-icons";

import { useIsMobile } from "@/hooks/use-is-mobile";

type CardProType = "type1" | "type2" | "type3";

interface CardProProps {
  type?: CardProType;
  className?: string;
  children: ReactNode;
  statsArea?: ReactNode;
  descriptionArea?: ReactNode;
  tabsArea?: ReactNode;
  actionsArea?: ReactNode | ReactNode[];
  searchArea?: ReactNode;
  paginationArea?: ReactNode;
  shadows?: string | boolean;
  bordered?: boolean;
  style?: CSSProperties;
  t?: (key: string) => string;
}

export function CardPro({
  type = "type1",
  className = "",
  children,
  statsArea,
  descriptionArea,
  tabsArea,
  actionsArea,
  searchArea,
  paginationArea,
  shadows = "",
  bordered = true,
  style,
  t = (key) => key,
}: CardProProps) {
  const isMobile = useIsMobile();
  const [showMobileActions, setShowMobileActions] = useState(false);
  const hasMobileHideableContent = Boolean(actionsArea || searchArea);

  const headerContent = (() => {
    const hasContent =
      statsArea || descriptionArea || tabsArea || actionsArea || searchArea;
    if (!hasContent) return null;

    return (
      <div className="flex w-full flex-col">
        {type === "type2" && statsArea ? <>{statsArea}</> : null}
        {(type === "type1" || type === "type3") && descriptionArea ? (
          <>{descriptionArea}</>
        ) : null}

        {((type === "type1" || type === "type3") && descriptionArea) ||
        (type === "type2" && statsArea) ? (
          <Divider margin="12px" />
        ) : null}

        {type === "type3" && tabsArea ? <>{tabsArea}</> : null}

        {isMobile && hasMobileHideableContent ? (
          <div className="mb-2 w-full">
            <Button
              block
              icon={showMobileActions ? <IconEyeClosed /> : <IconEyeOpened />}
              onClick={() => setShowMobileActions((value) => !value)}
              size="small"
              theme="outline"
              type="tertiary"
            >
              {showMobileActions ? t("Hide actions") : t("Show actions")}
            </Button>
          </div>
        ) : null}

        <div
          className={`flex flex-col gap-2 ${
            isMobile && !showMobileActions ? "hidden" : ""
          }`}
        >
          {(type === "type1" || type === "type3") && actionsArea ? (
            Array.isArray(actionsArea) ? (
              actionsArea.map((area, index) => (
                <div className="w-full" key={index}>
                  {index !== 0 ? <Divider /> : null}
                  {area}
                </div>
              ))
            ) : (
              <div className="w-full">{actionsArea}</div>
            )
          ) : null}

          {actionsArea && searchArea ? <Divider /> : null}
          {searchArea ? <div className="w-full">{searchArea}</div> : null}
        </div>
      </div>
    );
  })();

  const footerContent = paginationArea ? (
    <div
      className={`flex w-full border-t pt-4 ${
        isMobile ? "justify-center" : "items-center justify-between"
      }`}
      style={{ borderColor: "var(--semi-color-border)" }}
    >
      {paginationArea}
    </div>
  ) : null;

  return (
    <Card
      bordered={bordered}
      className={`table-scroll-card !rounded-2xl ${className}`}
      footer={footerContent}
      shadows={shadows as never}
      style={style}
      title={headerContent}
    >
      {children}
    </Card>
  );
}
