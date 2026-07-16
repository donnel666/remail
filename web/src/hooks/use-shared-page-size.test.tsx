// @vitest-environment jsdom

import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";

import { useSharedPageSize } from "./use-shared-page-size";

function PageSizeConsumer({ label }: { label: string }) {
  const [pageSize, setPageSize] = useSharedPageSize();
  return (
    <button onClick={() => setPageSize(50)} type="button">
      {label}:{pageSize}
    </button>
  );
}

afterEach(() => cleanup());

describe("shared page size", () => {
  it("updates every mounted pagination consumer and persists the value", () => {
    render(
      <>
        <PageSizeConsumer label="first" />
        <PageSizeConsumer label="second" />
      </>
    );

    fireEvent.click(screen.getByRole("button", { name: /first:/ }));

    expect(screen.getByRole("button", { name: "first:50" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "second:50" })).toBeInTheDocument();
    expect(window.localStorage.getItem("page-size")).toBe("50");
  });

  it("rereads storage when consumers remount after an unsubscribed interval", () => {
    const view = render(<PageSizeConsumer label="first" />);
    fireEvent.click(screen.getByRole("button", { name: /first:/ }));
    view.unmount();

    window.localStorage.setItem("page-size", "20");
    window.dispatchEvent(
      new StorageEvent("storage", { key: "page-size", newValue: "20" })
    );

    render(<PageSizeConsumer label="second" />);
    expect(screen.getByRole("button", { name: "second:20" })).toBeInTheDocument();
  });
});
