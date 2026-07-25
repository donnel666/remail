// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to }: { children: React.ReactNode; to: string }) => <a href={to}>{children}</a>,
}));

vi.mock("@/lib/system-settings-api", () => ({
  getSystemFAQs: vi.fn(async () => ({
    enabled: true,
    items: [
      { id: 1, question: "How do I reset my password?", answer: "Use the reset page.", weight: 10 },
      { id: 2, question: "How does billing work?", answer: "Your wallet is charged per order.", weight: 5 },
    ],
  })),
}));

import i18n from "@/i18n/config";
import Qna from "./Qna";

describe("Qna", () => {
  beforeEach(async () => {
    await i18n.changeLanguage("en");
  });

  afterEach(() => {
    cleanup();
    vi.clearAllMocks();
  });

  it("loads published FAQs and filters questions and answers", async () => {
    render(<Qna />);

    expect(await screen.findByText("How do I reset my password?")).not.toBeNull();
    fireEvent.change(screen.getByRole("searchbox", { name: "Search questions" }), { target: { value: "wallet" } });

    expect(screen.queryByText("How do I reset my password?")).toBeNull();
    expect(screen.getByText("How does billing work?")).not.toBeNull();
  });
});
