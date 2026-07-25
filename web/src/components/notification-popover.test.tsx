// @vitest-environment jsdom

import "@testing-library/jest-dom/vitest";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  getSystemAnnouncements: vi.fn(),
  getSystemNotice: vi.fn(),
}));

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

vi.mock("@/lib/system-settings-api", () => ({
  getSystemAnnouncements: mocks.getSystemAnnouncements,
  getSystemNotice: mocks.getSystemNotice,
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
    window.localStorage.clear();
    vi.clearAllMocks();
  });

  it("opens active system announcements automatically on entry", async () => {
    mocks.getSystemNotice.mockResolvedValue("");
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

    expect(await screen.findByText("Maintenance")).toBeVisible();
    expect(screen.getByText("Service will restart soon.")).toBeVisible();
    expect(screen.getByText("Effective immediately")).toBeVisible();
    expect(screen.getByRole("tab", { name: "System announcements" })).toHaveAttribute("aria-selected", "true");
    expect(mocks.getSystemAnnouncements).toHaveBeenCalledOnce();
    expect(screen.getByRole("button", { name: "Close today" })).toBeVisible();
  });

  it("opens again after a regular close and refresh", async () => {
    mocks.getSystemNotice.mockResolvedValue("");
    mocks.getSystemAnnouncements.mockResolvedValue([{
      id: 1, title: "Maintenance", content: "Restart soon", type: "warning", startTime: "", endTime: "", enabled: true,
    }]);
    const firstView = render(<NotificationPopover />);

    expect(await screen.findByText("Maintenance")).toBeVisible();
    fireEvent.click(screen.getAllByRole("button", { name: "Close notifications" })[0]);
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();

    firstView.unmount();
    render(<NotificationPopover />);

    expect(await screen.findByText("Maintenance")).toBeVisible();
    expect(mocks.getSystemAnnouncements).toHaveBeenCalledTimes(2);
  });

  it("keeps today's automatic popup closed while allowing manual access", async () => {
    mocks.getSystemNotice.mockResolvedValue("");
    mocks.getSystemAnnouncements.mockResolvedValue([{
      id: 1, title: "Maintenance", content: "Restart soon", type: "warning", startTime: "", endTime: "", enabled: true,
    }]);
    const firstView = render(<NotificationPopover />);

    expect(await screen.findByText("Maintenance")).toBeVisible();
    fireEvent.click(screen.getByRole("button", { name: "Close today" }));
    firstView.unmount();
    render(<NotificationPopover />);

    await waitFor(() => expect(mocks.getSystemAnnouncements).toHaveBeenCalledTimes(2));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Notifications" }));
    fireEvent.click(screen.getByRole("tab", { name: "System announcements" }));
    expect(await screen.findByText("Maintenance")).toBeVisible();
  });

  it("does not let an aborted request overwrite a newer response", async () => {
    mocks.getSystemNotice.mockResolvedValue("");
    const first = deferred<any[]>();
    const second = deferred<any[]>();
    mocks.getSystemAnnouncements.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise);
    render(<NotificationPopover />);

    const firstSignal = mocks.getSystemAnnouncements.mock.calls[0][0] as AbortSignal;
    fireEvent.click(screen.getByRole("button", { name: "Notifications" }));
    expect(firstSignal.aborted).toBe(true);
    fireEvent.click(await screen.findByRole("tab", { name: "System announcements" }));
    await act(async () => second.resolve([{
      id: 2, title: "Newest", content: "New", type: "success", startTime: "", endTime: "", enabled: true,
    }]));
    expect(await screen.findByText("Newest")).toBeVisible();

    await act(async () => first.resolve([{
      id: 1, title: "Stale", content: "Old", type: "default", startTime: "", endTime: "", enabled: true,
    }]));
    expect(screen.queryByText("Stale")).not.toBeInTheDocument();
  });

  it("shows the current system notice as escaped text", async () => {
    mocks.getSystemNotice.mockResolvedValue("Maintenance tonight\n<b>Plain text</b>");
    mocks.getSystemAnnouncements.mockResolvedValue([]);
    render(<NotificationPopover />);

    fireEvent.click(screen.getByRole("button", { name: "Notifications" }));

    const content = await screen.findByText(/Maintenance tonight/);
    expect(content).toHaveTextContent("Maintenance tonight <b>Plain text</b>");
    expect(document.querySelector("b")).toBeNull();
    expect(content.closest("[aria-live]")).toBeNull();
    expect(screen.getByRole("tabpanel", { name: "Notifications" })).toContainElement(content);
  });

  it("shows a notice without waiting for the announcements request", async () => {
    const announcements = deferred<any[]>();
    mocks.getSystemNotice.mockResolvedValue("Available immediately");
    mocks.getSystemAnnouncements.mockReturnValue(announcements.promise);
    render(<NotificationPopover />);

    fireEvent.click(screen.getByRole("button", { name: "Notifications" }));

    expect(await screen.findByText("Available immediately")).toBeVisible();
    expect(screen.queryByText("Loading notifications")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("tab", { name: "System announcements" }));
    expect(screen.getByText("Loading announcements")).toBeVisible();

    await act(async () => announcements.resolve([]));
  });

  it("retries only the failed tab", async () => {
    mocks.getSystemNotice.mockRejectedValueOnce(new Error("failed")).mockResolvedValueOnce("Recovered");
    mocks.getSystemAnnouncements.mockResolvedValue([{
      id: 1, title: "Available announcement", content: "Ready", type: "default", startTime: "", endTime: "", enabled: true,
    }]);
    render(<NotificationPopover />);

    expect(await screen.findByText("Available announcement")).toBeVisible();
    fireEvent.click(screen.getByRole("tab", { name: "Notifications" }));
    expect(await screen.findByText("Failed to load notifications")).toBeVisible();
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));

    expect(await screen.findByText("Recovered")).toBeVisible();
    expect(mocks.getSystemNotice).toHaveBeenCalledTimes(2);
    expect(mocks.getSystemAnnouncements).toHaveBeenCalledOnce();
  });
});
