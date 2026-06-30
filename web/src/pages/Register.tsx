import { Link, useNavigate } from "@tanstack/react-router";
import { useState, type FormEvent } from "react";
import { useTranslation } from "react-i18next";
import { useAuth } from "@/context/auth-provider";

export default function Register() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const { signIn } = useAuth();
  const [email, setEmail] = useState("");

  const handleSubmit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    signIn({
      email,
      name: email.includes("@") ? email.split("@")[0] : email,
    });
    void navigate({ to: "/dashboard" });
  };

  return (
    <div className="flex min-h-[calc(100svh-64px)] items-center justify-center bg-[var(--canvas)] px-4">
      <div className="w-full max-w-sm rounded-xl border border-[var(--divider)] bg-[var(--surface)] p-8 shadow-sm">
        <div className="mb-8 flex flex-col items-center gap-2">
          <img src="/logo.png" alt="Remail" className="h-12 w-12" />
          <h1 className="text-xl font-bold text-[var(--ink-primary)]">Remail</h1>
          <p className="text-sm text-[var(--ink-muted)]">{t("Create your account")}</p>
        </div>
        <form className="space-y-4" onSubmit={handleSubmit}>
          <input
            type="email"
            value={email}
            onChange={(event) => setEmail(event.target.value)}
            placeholder={t("Email")}
            className="input-antd w-full"
          />
          <input type="password" placeholder={t("Password")} className="input-antd w-full" />
          <input
            type="password"
            placeholder={t("Confirm password")}
            className="input-antd w-full"
          />
          <button className="h-10 w-full rounded-lg bg-gradient-to-br from-[var(--brand-start)] to-[var(--brand-end)] text-[14px] font-semibold text-white shadow-sm transition-all hover:shadow-md">
            {t("Register")}
          </button>
        </form>
        <div className="mt-5 text-center text-sm text-[var(--ink-muted)]">
          {t("Already have an account")}{" "}
          <Link to="/login" className="font-medium text-brand hover:text-brand-hover">
            {t("Login")}
          </Link>
        </div>
      </div>
    </div>
  );
}
