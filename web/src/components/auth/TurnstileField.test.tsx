// @vitest-environment jsdom

import { act, cleanup, render, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { TurnstileField } from "./TurnstileField";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

vi.mock("@/lib/iam-api", () => ({
  getTurnstileConfig: vi.fn().mockResolvedValue({ siteKey: "site-key" }),
}));

afterEach(() => {
  cleanup();
  delete window.turnstile;
});

it("renders the configured action and returns the verified token", async () => {
  let callback: ((token: string) => void) | undefined;
  const renderWidget = vi.fn((_container, options) => {
    callback = options.callback;
    expect(options.sitekey).toBe("site-key");
    expect(options.action).toBe("login");
    return "widget-1";
  });
  const remove = vi.fn();
  window.turnstile = { render: renderWidget, remove };
  const onTokenChange = vi.fn();

  const view = render(
    <TurnstileField action="login" resetKey={0} onTokenChange={onTokenChange} />
  );
  await waitFor(() => expect(renderWidget).toHaveBeenCalledOnce());

  act(() => callback?.("verified-token"));
  expect(onTokenChange).toHaveBeenLastCalledWith("verified-token");

  view.rerender(
    <TurnstileField action="login" resetKey={1} onTokenChange={onTokenChange} />
  );
  await waitFor(() => expect(renderWidget).toHaveBeenCalledTimes(2));
  expect(remove).toHaveBeenCalledWith("widget-1");
  expect(onTokenChange).toHaveBeenLastCalledWith("");
});
