// @vitest-environment jsdom

import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  completeGitHub: vi.fn(),
  getGitHubPending: vi.fn(),
  sendGitHubEmailCode: vi.fn(),
  t: (key: string) => key,
}));

vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: mocks.t }) }));
vi.mock("@douyinfe/semi-ui", () => ({
  Button: ({ children, disabled, loading, onClick }: any) => (
    <button disabled={disabled || loading} onClick={onClick} type="button">{children}</button>
  ),
  Modal: ({ children, footer, title, visible }: any) => visible ? (
    <div aria-label={title} role="dialog">{children}{footer}</div>
  ) : null,
}));
vi.mock("@/components/auth/SendCodeField", () => ({
  SendCodeField: ({ code, email, onCodeChange, send }: any) => (
    <div>
      <input aria-label="Verification code" onChange={(event) => onCodeChange(event.target.value)} value={code} />
      <button onClick={() => void send({ email, turnstileToken: "turnstile" })} type="button">Send code</button>
    </div>
  ),
}));
vi.mock("@/lib/iam-api", () => ({
  completeGitHub: mocks.completeGitHub,
  getGitHubPending: mocks.getGitHubPending,
  sendGitHubEmailCode: mocks.sendGitHubEmailCode,
}));
vi.mock("@/lib/iam-errors", () => ({
  getIamErrorMessage: (_t: unknown, _error: unknown, fallback: string) => fallback,
}));

import { GitHubVerificationModal } from "./GitHubVerificationModal";

beforeEach(() => {
  window.history.replaceState({}, "", "/account?oauth_setup=github");
  mocks.getGitHubPending.mockResolvedValue({
    provider: "github",
    providerUserId: "42",
    username: "octocat",
    email: "owner@example.com",
    intent: "bind",
  });
  mocks.sendGitHubEmailCode.mockResolvedValue(60);
  mocks.completeGitHub.mockResolvedValue(undefined);
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

it("verifies the server-selected local email before completing a GitHub binding", async () => {
  const onComplete = vi.fn().mockResolvedValue(undefined);
  render(
    <GitHubVerificationModal
      onCancel={vi.fn()}
      onComplete={onComplete}
      open
    />
  );

  expect(await screen.findByDisplayValue("owner@example.com")).toHaveAttribute("readonly");
  fireEvent.click(screen.getByRole("button", { name: "Send code" }));
  await waitFor(() => expect(mocks.sendGitHubEmailCode).toHaveBeenCalledWith({
    email: "owner@example.com",
    turnstileToken: "turnstile",
  }));

  fireEvent.change(screen.getByLabelText("Verification code"), { target: { value: "123456" } });
  fireEvent.click(screen.getByRole("button", { name: "Continue" }));

  await waitFor(() => expect(mocks.completeGitHub).toHaveBeenCalledWith({ code: "123456" }));
  expect(onComplete).toHaveBeenCalledWith("bind");
  expect(window.location.search).toBe("");
});
