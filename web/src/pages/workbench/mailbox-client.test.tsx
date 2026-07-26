// @vitest-environment jsdom

import "@testing-library/jest-dom/vitest";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { WorkbenchMessage } from "./types";
import { MailboxClient } from "./mailbox-client";

vi.mock("@douyinfe/semi-ui", () => ({
  Button: ({ children, onClick }: { children?: ReactNode; onClick?: () => void }) => (
    <button onClick={onClick} type="button">{children}</button>
  ),
  Empty: ({ description }: { description?: ReactNode }) => <div>{description}</div>,
  Input: () => <input aria-label="search" />,
  Modal: ({ children }: { children?: ReactNode }) => <div>{children}</div>,
  Tag: ({ children }: { children?: ReactNode }) => <span>{children}</span>,
  Typography: {
    Text: ({ children }: { children?: ReactNode }) => <span>{children}</span>,
  },
}));

vi.mock("@douyinfe/semi-icons", () => ({ IconSearch: () => null }));
vi.mock("lucide-react", () => ({ Mail: () => <svg aria-label="mail" /> }));
vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));
vi.mock("@/components/semi/copyable-config", () => ({
  createCopyableConfig: () => undefined,
}));
vi.mock("@/components/semi/overflow-tooltip", () => ({
  OverflowTooltip: ({ children, className }: { children?: ReactNode; className?: string }) => (
    <span className={className}>{children}</span>
  ),
}));
vi.mock("./fetch-control", () => ({ FetchControl: () => null }));
vi.mock("./utils", () => ({ formatDateTime: (value: string) => value }));

afterEach(cleanup);

function message(email: string): WorkbenchMessage {
  return {
    body: "",
    id: "1",
    preview: "preview",
    receivedAt: "2026-07-26T12:00:00Z",
    recipient: email,
    sender: "sender@example.net",
    status: "received",
    subject: "Verification",
  };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

describe("MailboxClient message body loading", () => {
  it("retries a failed load and keeps unsafe HTML as text", async () => {
    const unsafeBody = `<script>alert(1)</script><img src=x onerror=alert(2)><a href="https://example.com">Verify</a>`;
    const loader = vi.fn()
      .mockRejectedValueOnce(new Error("temporary failure"))
      .mockResolvedValueOnce(unsafeBody);
    const view = render(
      <MailboxClient
        email="first@example.com"
        fetchKey="first"
        messages={[message("first@example.com")]}
        onFetch={vi.fn()}
        onLoadMessage={loader}
      />
    );

    expect(await screen.findByText("Mail load failed.")).toBeVisible();
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));

    await waitFor(() => expect(view.container.querySelector(".mailbox-client-body")).toHaveTextContent(unsafeBody));
    expect(loader).toHaveBeenCalledTimes(2);
    expect(view.container.querySelector("script, img, a")).toBeNull();
  });

  it("treats an empty response as successfully loaded", async () => {
    const loader = vi.fn().mockResolvedValue("");
    const view = render(
      <MailboxClient
        email="empty@example.com"
        fetchKey="empty"
        messages={[message("empty@example.com")]}
        onFetch={vi.fn()}
        onLoadMessage={loader}
      />
    );

    await waitFor(() => expect(loader).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(screen.queryByText("Loading...")).not.toBeInTheDocument());
    view.rerender(
      <MailboxClient
        email="empty@example.com"
        fetchKey="empty"
        messages={[message("empty@example.com")]}
        onFetch={vi.fn()}
        onLoadMessage={loader}
      />
    );

    await act(async () => Promise.resolve());
    expect(loader).toHaveBeenCalledTimes(1);
  });

  it("keeps an in-flight result when the loader function identity changes", async () => {
    const pending = deferred<string>();
    const firstLoader = vi.fn(() => pending.promise);
    const replacementLoader = vi.fn().mockResolvedValue("replacement");
    const view = render(
      <MailboxClient
        email="same@example.com"
        fetchKey="same"
        messages={[message("same@example.com")]}
        onFetch={vi.fn()}
        onLoadMessage={firstLoader}
      />
    );
    await waitFor(() => expect(firstLoader).toHaveBeenCalledTimes(1));

    view.rerender(
      <MailboxClient
        email="same@example.com"
        fetchKey="same"
        messages={[message("same@example.com")]}
        onFetch={vi.fn()}
        onLoadMessage={replacementLoader}
      />
    );
    await act(async () => pending.resolve("original result"));

    await waitFor(() => expect(view.container.querySelector(".mailbox-client-body")).toHaveTextContent("original result"));
    expect(replacementLoader).not.toHaveBeenCalled();
  });

  it("does not let an old email request overwrite the new email", async () => {
    const oldRequest = deferred<string>();
    const oldLoader = vi.fn(() => oldRequest.promise);
    const newLoader = vi.fn().mockResolvedValue("new email body");
    const view = render(
      <MailboxClient
        email="old@example.com"
        fetchKey="old"
        messages={[message("old@example.com")]}
        onFetch={vi.fn()}
        onLoadMessage={oldLoader}
      />
    );
    await waitFor(() => expect(oldLoader).toHaveBeenCalledTimes(1));

    view.rerender(
      <MailboxClient
        email="new@example.com"
        fetchKey="new"
        messages={[message("new@example.com")]}
        onFetch={vi.fn()}
        onLoadMessage={newLoader}
      />
    );
    await waitFor(() => expect(view.container.querySelector(".mailbox-client-body")).toHaveTextContent("new email body"));

    await act(async () => oldRequest.resolve("stale body"));
    expect(view.container.querySelector(".mailbox-client-body")).toHaveTextContent("new email body");
    expect(view.container.querySelector(".mailbox-client-body")).not.toHaveTextContent("stale body");
  });
});
