// @vitest-environment jsdom

import { act, cleanup, render } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { FetchControl } from "./fetch-control";

vi.mock("@douyinfe/semi-ui", () => ({
  Button: ({
    children,
    disabled,
    onClick,
  }: {
    children?: React.ReactNode;
    disabled?: boolean;
    onClick?: () => void;
  }) => (
    <button disabled={disabled} onClick={onClick} type="button">
      {children}
    </button>
  ),
  Toast: { error: vi.fn() },
}));

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (key: string, values?: { seconds?: number }) =>
      values?.seconds === undefined ? key : `${key}:${values.seconds}`,
  }),
}));

describe("FetchControl shared scheduler", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    Object.defineProperty(document, "hidden", {
      configurable: true,
      value: false,
    });
  });

  afterEach(() => {
    cleanup();
    vi.runOnlyPendingTimers();
    vi.useRealTimers();
  });

  it("runs one automatic request for duplicate controls", async () => {
    const onFetch = vi.fn(async () => 5);
    render(
      <>
        <FetchControl compact fetchKey="order-1" onFetch={onFetch} />
        <FetchControl compact fetchKey="order-1" onFetch={onFetch} />
      </>
    );

    await act(async () => {
      await vi.advanceTimersByTimeAsync(1100);
    });

    expect(onFetch).toHaveBeenCalledTimes(1);
    expect(onFetch).toHaveBeenCalledWith("auto");
  });

  it("does not auto-fetch while the page is hidden", async () => {
    Object.defineProperty(document, "hidden", {
      configurable: true,
      value: true,
    });
    const onFetch = vi.fn(async () => 5);
    render(<FetchControl compact fetchKey="order-hidden" onFetch={onFetch} />);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(2100);
    });

    expect(onFetch).not.toHaveBeenCalled();
  });

  it("waits for the delay returned by the server", async () => {
    const onFetch = vi.fn(async () => 30);
    render(<FetchControl compact fetchKey="order-delay" onFetch={onFetch} />);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(1100);
    });
    expect(onFetch).toHaveBeenCalledTimes(1);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(29_000);
    });
    expect(onFetch).toHaveBeenCalledTimes(1);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(2_000);
    });
    expect(onFetch).toHaveBeenCalledTimes(2);
  });
});
