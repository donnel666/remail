// @vitest-environment jsdom

import { act, cleanup, fireEvent, render } from "@testing-library/react";
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

  it("stops automatic requests after the control unmounts", async () => {
    let resolveFetch: ((delay: number) => void) | undefined;
    const onFetch = vi.fn(
      () =>
        new Promise<number>((resolve) => {
          resolveFetch = resolve;
        }),
    );
    const { unmount } = render(
      <FetchControl compact fetchKey="order-complete" onFetch={onFetch} />,
    );

    await act(async () => {
      await vi.advanceTimersByTimeAsync(1100);
    });
    expect(onFetch).toHaveBeenCalledTimes(1);

    unmount();
    await act(async () => {
      resolveFetch?.(1);
      await Promise.resolve();
      await vi.advanceTimersByTimeAsync(5000);
    });

    expect(onFetch).toHaveBeenCalledTimes(1);
  });

  it("uses the surviving subscriber handler for automatic requests", async () => {
    const firstHandler = vi.fn(async () => 5);
    const removedHandler = vi.fn(async () => 5);
    const { rerender } = render(
      <>
        <FetchControl
          compact
          fetchKey="order-auto-owner"
          onFetch={firstHandler}
        />
        <FetchControl
          compact
          fetchKey="order-auto-owner"
          onFetch={removedHandler}
        />
      </>,
    );

    rerender(
      <FetchControl compact fetchKey="order-auto-owner" onFetch={firstHandler} />,
    );
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1100);
    });

    expect(firstHandler).toHaveBeenCalledOnce();
    expect(firstHandler).toHaveBeenCalledWith("auto");
    expect(removedHandler).not.toHaveBeenCalled();
  });

  it("uses the clicking subscriber handler for manual requests", async () => {
    const firstHandler = vi.fn(async () => 5);
    const removedHandler = vi.fn(async () => 5);
    const { getByRole, rerender } = render(
      <>
        <FetchControl
          autoEnabled={false}
          compact
          fetchKey="order-manual-owner"
          onFetch={firstHandler}
        />
        <FetchControl
          autoEnabled={false}
          compact
          fetchKey="order-manual-owner"
          onFetch={removedHandler}
        />
      </>,
    );

    rerender(
      <FetchControl
        autoEnabled={false}
        compact
        fetchKey="order-manual-owner"
        onFetch={firstHandler}
      />,
    );
    await act(async () => {
      fireEvent.click(getByRole("button"));
      await Promise.resolve();
    });

    expect(firstHandler).toHaveBeenCalledOnce();
    expect(firstHandler).toHaveBeenCalledWith("manual");
    expect(removedHandler).not.toHaveBeenCalled();
  });

  it("keeps the handler selected by a manual click if the control unmounts immediately", async () => {
    const onFetch = vi.fn(async () => 5);
    const { getByRole, unmount } = render(
      <FetchControl
        autoEnabled={false}
        compact
        fetchKey="order-manual-unmount"
        onFetch={onFetch}
      />,
    );

    fireEvent.click(getByRole("button"));
    unmount();
    await act(async () => {
      await Promise.resolve();
    });

    expect(onFetch).toHaveBeenCalledOnce();
    expect(onFetch).toHaveBeenCalledWith("manual");
  });

  it("clears the automatic countdown when the last auto subscriber is disabled", async () => {
    let resolveFetch: ((delay: number) => void) | undefined;
    const onFetch = vi.fn(
      () =>
        new Promise<number>((resolve) => {
          resolveFetch = resolve;
        }),
    );
    const { getByRole, rerender } = render(
      <FetchControl compact fetchKey="order-auto-disabled" onFetch={onFetch} />,
    );

    await act(async () => {
      await vi.advanceTimersByTimeAsync(1100);
    });
    expect(onFetch).toHaveBeenCalledOnce();

    rerender(
      <FetchControl
        autoEnabled={false}
        compact
        fetchKey="order-auto-disabled"
        onFetch={onFetch}
      />,
    );
    await act(async () => {
      resolveFetch?.(5);
      await Promise.resolve();
    });

    expect(getByRole("button").textContent).toBe("Fetch mail");
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000);
    });
    expect(onFetch).toHaveBeenCalledOnce();
  });

  it("keeps a manual cooldown when the last auto subscriber is removed", async () => {
    const autoHandler = vi.fn(async () => 5);
    const manualHandler = vi.fn(async () => 5);
    const { getAllByRole, rerender } = render(
      <>
        <FetchControl
          compact
          fetchKey="order-manual-cooldown"
          key="auto"
          onFetch={autoHandler}
        />
        <FetchControl
          autoEnabled={false}
          compact
          fetchKey="order-manual-cooldown"
          key="manual"
          onFetch={manualHandler}
        />
      </>,
    );

    await act(async () => {
      fireEvent.click(getAllByRole("button")[1]);
      await Promise.resolve();
    });
    expect(manualHandler).toHaveBeenCalledOnce();

    rerender(
      <FetchControl
        autoEnabled={false}
        compact
        fetchKey="order-manual-cooldown"
        key="manual"
        onFetch={manualHandler}
      />,
    );

    expect(getAllByRole("button")[0].textContent).toContain("Fetch cooldown");
    fireEvent.click(getAllByRole("button")[0]);
    expect(manualHandler).toHaveBeenCalledOnce();
  });

  it("keeps scheduling after an automatic request fails", async () => {
    const onFetch = vi
      .fn()
      .mockRejectedValueOnce(new Error("temporary failure"))
      .mockResolvedValue(5);
    render(<FetchControl compact fetchKey="order-retry" onFetch={onFetch} />);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(1100);
    });
    expect(onFetch).toHaveBeenCalledTimes(1);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000);
    });
    expect(onFetch).toHaveBeenCalledTimes(2);
  });
});
