import { Typography, Toast } from "@douyinfe/semi-ui";
import { Globe, Lock, Shield, Zap } from "lucide-react";
import { useTranslation } from "react-i18next";

const FEATURES = [
  {
    icon: Zap,
    titleKey: "Real-time code receiving",
    descKey: "Receive verification codes within 30 seconds on average",
  },
  {
    icon: Shield,
    titleKey: "Platform escrow",
    descKey: "Full automatic refund on timeout",
  },
  {
    icon: Globe,
    titleKey: "Multi-platform support",
    descKey: "Receive verification codes from 200+ platforms",
  },
  {
    icon: Lock,
    titleKey: "Secure transactions",
    descKey: "Email resources are verified by the system",
  },
];

const endpoint = "/v1/orders";
const examplePayload = '{"orderKind":"code","projectId":1,"selectedEmailType":"outlook"}';
const curlRaw = `curl -X POST ${endpoint} -H "Authorization: Bearer <YOUR_KEY>" -d '${examplePayload}'`;
const titleBrandGradient =
  "bg-gradient-to-r from-[#8a4a34] via-[#c6533c] to-[#f4513b] bg-clip-text text-transparent dark:from-[#ffd0a3] dark:via-[#ff8a5c] dark:to-[#ff5a82]";
const titleHotGradient =
  "bg-gradient-to-r from-[#ff7a1a] via-[#ff5a3d] to-[#ff3d73] bg-clip-text text-transparent";
const { Text } = Typography;

function TerminalCode({ responseLabel }: { responseLabel: string }) {
  return (
    <pre className="overflow-x-auto px-4 py-3.5 text-[13px] leading-relaxed font-mono-data">
      <code>
        <span className="text-[var(--terminal-prompt)]">$</span>{" "}
        <span className="text-[var(--terminal-command)]">curl -X POST</span>{" "}
        <span className="text-[var(--terminal-accent)]">{endpoint}</span>
        {"\n  "}
        <span className="text-[var(--terminal-flag)]">-H</span>{" "}
        <span className="text-[var(--terminal-string)]">
          "Authorization: Bearer &lt;YOUR_KEY&gt;"
        </span>
        {"\n  "}
        <span className="text-[var(--terminal-flag)]">-d</span>{" "}
        <span className="text-[var(--terminal-string)]">'{examplePayload}'</span>
        {"\n\n"}
        <span className="text-[var(--terminal-muted)]"># {responseLabel}</span>
        {"\n"}
        <span className="text-[var(--terminal-accent)]">{"{"}</span>
        {"\n  "}
        <span className="text-[var(--terminal-key)]">"orderNo"</span>
        <span className="text-[var(--terminal-muted)]">: </span>
        <span className="text-[var(--terminal-string)]">"MT20260509A1B2"</span>
        <span className="text-[var(--terminal-muted)]">,</span>
        {"\n  "}
        <span className="text-[var(--terminal-key)]">"status"</span>
        <span className="text-[var(--terminal-muted)]">: </span>
        <span className="text-[var(--terminal-string)]">"fulfilling"</span>
        <span className="text-[var(--terminal-muted)]">,</span>
        {"\n  "}
        <span className="text-[var(--terminal-key)]">"email"</span>
        <span className="text-[var(--terminal-muted)]">: </span>
        <span className="text-[var(--terminal-string)]">"user_abc@outlook.com"</span>
        {"\n"}
        <span className="text-[var(--terminal-accent)]">{"}"}</span>
      </code>
    </pre>
  );
}

export default function Home() {
  const { t } = useTranslation();

  return (
    <section className="relative flex min-h-[calc(100svh-64px)] items-center overflow-hidden bg-background px-6 lg:px-12">
      <div className="pointer-events-none absolute inset-0 bg-[radial-gradient(circle_at_30%_20%,rgba(0,0,0,0.02)_0%,transparent_50%),radial-gradient(circle_at_70%_80%,rgba(0,0,0,0.02)_0%,transparent_50%)]" />
      <div
        className="pointer-events-none absolute inset-0 opacity-[0.03]"
        style={{
          backgroundImage:
            "url(\"data:image/svg+xml,%3Csvg width='60' height='60' viewBox='0 0 60 60' xmlns='http://www.w3.org/2000/svg'%3E%3Cg fill='none' fill-rule='evenodd'%3E%3Cg fill='%23000000' fill-opacity='1'%3E%3Cpath d='M36 34v-4h-2v4h-4v2h4v4h2v-4h4v-2h-4zm0-30V0h-2v4h-4v2h4v4h2V6h4V4h-4zM6 34v-4H4v4H0v2h4v4h2v-4h4v-2H6zM6 4V0H4v4H0v2h4v4h2V6h4V4H6z'/%3E%3C/g%3E%3C/g%3E%3C/svg%3E\")",
        }}
      />
      <div className="relative mx-auto flex w-full max-w-7xl flex-col items-center gap-10 lg:flex-row lg:gap-14">
        <div className="flex-1 text-center lg:flex-[1.18] lg:text-left">
          <div className="mb-5 inline-flex items-center gap-2 rounded-full border border-border bg-background/80 px-3 py-1 text-[13px] font-medium text-muted-foreground shadow-sm backdrop-blur">
            <span className="relative flex size-1.5">
              <span className="absolute size-full animate-ping rounded-full bg-emerald-400 opacity-60" />
              <span className="relative size-1.5 rounded-full bg-emerald-500" />
            </span>
            {t("Millions of email resources online, ready to use!")}
          </div>

          <h1 className="text-[2.25rem] font-bold leading-[1.16] tracking-tight text-foreground sm:text-5xl sm:leading-[1.1] lg:text-[3.25rem]">
            <span className="relative inline-block">
              <span className="relative z-[1]">
                <span className={titleBrandGradient}>Remail</span>
                <span>{t("Remail title suffix")}</span>
              </span>
              <span
                className="shine-text absolute inset-0 z-[2]"
                aria-hidden="true"
              >
                Remail{t("Remail title suffix")}
              </span>
            </span>
            <br />
            <span>{t("Remail title second prefix")}</span>
            <span className={titleHotGradient}>
              {t("Remail title hot word")}
            </span>
            <span>{t("Remail title second suffix")}</span>
          </h1>

          <p className="mx-auto mt-4 max-w-md text-[16px] leading-relaxed text-muted-foreground lg:mx-0">
            {t("Monetize idle emails, sync verification codes in real time, and stay protected end to end")}
          </p>

          <div className="mx-auto mt-8 grid max-w-[620px] grid-cols-1 gap-x-6 gap-y-2 sm:grid-cols-2 lg:mx-0">
            {FEATURES.map((feature) => {
              const Icon = feature.icon;
              const title = t(feature.titleKey);
              const description = t(feature.descKey);

              return (
                <div
                  key={feature.titleKey}
                  className="flex items-center gap-1.5 text-[13px] text-muted-foreground"
                >
                  <Icon className="size-3 text-brand" />
                  <span className="font-medium">{title}</span>
                  <span className="text-[var(--ink-faint)]">· {description}</span>
                </div>
              );
            })}
          </div>
        </div>

        <div className="w-full max-w-md flex-shrink-0 lg:max-w-[544px]">
          <div className="overflow-hidden rounded-xl border border-[var(--terminal-border)] bg-[var(--terminal-background)] text-left shadow-xl">
            <div className="flex items-center gap-1.5 border-b border-white/10 px-4 py-2">
              <div className="size-3 rounded-full bg-red-400/70" />
              <div className="size-3 rounded-full bg-amber-400/70" />
              <div className="size-3 rounded-full bg-emerald-400/70" />
              <span className="ml-2 text-[12px] text-white/30 font-mono-data">
                bash — remail-api
              </span>
              <Text
                className="relative ml-auto inline-flex items-center text-white/30 transition-colors hover:text-[var(--brand-light)]"
                aria-label={t("Copy API example")}
                copyable={{
                  content: curlRaw,
                  onCopy: () => Toast.success(t("Copied")),
                }}
              >
                <span className="sr-only">{t("Copy API example")}</span>
              </Text>
            </div>
            <TerminalCode responseLabel={t("Response")} />
          </div>
          <p className="mt-2 text-center text-[12px] text-[var(--ink-faint)]">
            {t("Connect to the API with one command")}
          </p>
        </div>
      </div>
    </section>
  );
}
