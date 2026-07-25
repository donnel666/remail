// @vitest-environment jsdom

import "@testing-library/jest-dom/vitest";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  getSystemAnnouncements: vi.fn(),
}));

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

vi.mock("@/lib/system-settings-api", () => ({
  getSystemAnnouncements: mocks.getSystemAnnouncements,
}));

import { NotificationPopover } from "./notification-popover";

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

describe("NotificationPopover", () => {
  afterEach(() => {
    cleanup();
    vi.clearAllMocks();
  });

  it("loads and renders active system announcements when opened", async () => {
    mocks.getSystemAnnouncements.mockResolvedValue([{
      id: 1,
      title: "Maintenance",
      content: "Service will restart soon.",
      type: "warning",
      startTime: "",
      endTime: "",
      enabled: true,
    }]);
    render(<NotificationPopover />);

    fireEvent.click(screen.getByRole("button", { name: "System announcements" }));

    expect(await screen.findByText("Maintenance")).toBeVisible();
    expect(screen.getByText("Service will restart soon.")).toBeVisible();
    expect(screen.getByText("Effective immediately")).toBeVisible();
    expect(mocks.getSystemAnnouncements).toHaveBeenCalledOnce();
    expect(screen.queryByText("Close today")).not.toBeInTheDocument();
  });

  it("does not let an aborted request overwrite a newer response", async () => {
    const first = deferred<any[]>();
    const second = deferred<any[]>();
    mocks.getSystemAnnouncements.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise);
    render(<NotificationPopover />);

    fireEvent.click(screen.getByRole("button", { name: "System announcements" }));
    const firstSignal = mocks.getSystemAnnouncements.mock.calls[0][0] as AbortSignal;
    fireEvent.click(screen.getAllByRole("button", { name: "Close announcements" })[0]);
    expect(firstSignal.aborted).toBe(true);

    fireEvent.click(screen.getByRole("button", { name: "System announcements" }));
    await act(async () => second.resolve([{
      id: 2, title: "Newest", content: "New", type: "success", startTime: "", endTime: "", enabled: true,
    }]));
    expect(await screen.findByText("Newest")).toBeVisible();

    await act(async () => first.resolve([{
      id: 1, title: "Stale", content: "Old", type: "default", startTime: "", endTime: "", enabled: true,
    }]));
    expect(screen.queryByText("Stale")).not.toBeInTheDocument();
  });
});
