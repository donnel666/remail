import type { ComponentType, CSSProperties } from "react";
import { Button, Empty, Space } from "@douyinfe/semi-ui";
import {
  IllustrationFailure,
  IllustrationFailureDark,
  IllustrationNoAccess,
  IllustrationNoAccessDark,
  IllustrationNotFound,
  IllustrationNotFoundDark,
} from "@douyinfe/semi-illustrations";
import { ArrowLeft, Home, RotateCcw } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useTheme } from "@/context/theme-provider";

interface ErrorPageProps {
  titleKey: string;
  descriptionKey: string;
  image: ComponentType<{ style?: CSSProperties }>;
  darkImage: ComponentType<{ style?: CSSProperties }>;
  primaryTo: string;
  primaryLabelKey: string;
  onRetry?: () => void;
}

function goBack() {
  if (window.history.length > 1) {
    window.history.back();
    return;
  }
  window.location.assign("/");
}

function ErrorPage({
  titleKey,
  descriptionKey,
  image: Image,
  darkImage: DarkImage,
  primaryTo,
  primaryLabelKey,
  onRetry,
}: ErrorPageProps) {
  const { t } = useTranslation();
  const { resolvedTheme } = useTheme();
  const Illustration = resolvedTheme === "dark" ? DarkImage : Image;

  return (
    <div className="flex min-h-[calc(100vh-64px)] items-center justify-center bg-background px-4 py-12 sm:px-6">
      <section className="w-full max-w-[720px] text-center">
        <Empty
          className="mx-auto"
          image={<Illustration style={{ height: 240, width: 240 }} />}
          title={
            <span className="text-xl font-semibold tracking-tight text-[var(--ink-primary)] sm:text-2xl">
              {t(titleKey)}
            </span>
          }
          description={
            <span className="mx-auto block max-w-[430px] text-sm leading-6 text-[var(--ink-muted)]">
              {t(descriptionKey)}
            </span>
          }
          style={{ padding: 0 }}
        />

        <Space className="mt-8 justify-center" spacing={12} wrap>
          <Button
            type="primary"
            icon={<Home size={15} />}
            onClick={() => window.location.assign(primaryTo)}
          >
            {t(primaryLabelKey)}
          </Button>
          <Button icon={<ArrowLeft size={15} />} onClick={goBack}>
            {t("Go back")}
          </Button>
          {onRetry ? (
            <Button icon={<RotateCcw size={15} />} onClick={onRetry}>
              {t("Try again")}
            </Button>
          ) : null}
        </Space>
      </section>
    </div>
  );
}

export function ForbiddenPage() {
  return (
    <ErrorPage
      titleKey="Access denied"
      descriptionKey="You do not have permission to access this page."
      image={IllustrationNoAccess}
      darkImage={IllustrationNoAccessDark}
      primaryTo="/dashboard"
      primaryLabelKey="Back to console"
    />
  );
}

export function NotFoundPage() {
  return (
    <ErrorPage
      titleKey="Page not found"
      descriptionKey="The page you are looking for does not exist or has been moved."
      image={IllustrationNotFound}
      darkImage={IllustrationNotFoundDark}
      primaryTo="/"
      primaryLabelKey="Back home"
    />
  );
}

export function ServerErrorPage({ onRetry }: { onRetry?: () => void }) {
  return (
    <ErrorPage
      titleKey="Service unavailable"
      descriptionKey="The service is temporarily unavailable. Please retry or return later."
      image={IllustrationFailure}
      darkImage={IllustrationFailureDark}
      primaryTo="/dashboard"
      primaryLabelKey="Back to console"
      onRetry={onRetry}
    />
  );
}
