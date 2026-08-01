// @vitest-environment jsdom

import "@testing-library/jest-dom/vitest";
import { act, fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { cancelTurnstile, requireTurnstile } from "./TurnstileGate";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

vi.mock("./TurnstileField", () => ({
  TurnstileField: ({ onTokenChange }: any) => (
    <button onClick={() => onTokenChange("verified-token")} type="button">
      challenge
    </button>
  ),
}));

afterEach(async () => {
  cancelTurnstile();
  await new Promise((resolve) => globalThis.setTimeout(resolve, 0));
  document.body.replaceChildren();
});

describe("requireTurnstile", () => {
  it("settles and removes its React host on browser navigation", async () => {
    const result = requireTurnstile("ticket_create");
    expect(document.body.childElementCount).toBe(1);

    act(() => window.dispatchEvent(new PopStateEvent("popstate")));

    await expect(result).resolves.toBeNull();
    await waitFor(() => expect(document.body).toBeEmptyDOMElement());
  });

  it("exposes an accessible focus-trapped dialog and restores focus", async () => {
    const opener = document.createElement("button");
    document.body.appendChild(opener);
    opener.focus();

    const result = requireTurnstile("ticket_create");
    const dialog = await screen.findByRole(
      "dialog",
      { name: "Human verification" },
      { timeout: 1_200 }
    );

    expect(dialog).toHaveAttribute("aria-modal", "true");
    expect(dialog).toHaveAccessibleDescription(
      "Please complete human verification to continue."
    );
    const challenge = screen.getByRole("button", { name: "challenge" });
    const cancel = screen.getByRole("button", { name: "Cancel" });
    expect(challenge).toHaveFocus();

    fireEvent.keyDown(document, { key: "Tab", shiftKey: true });
    expect(cancel).toHaveFocus();
    fireEvent.keyDown(document, { key: "Tab" });
    expect(challenge).toHaveFocus();

    fireEvent.keyDown(document, { key: "Escape" });
    await expect(result).resolves.toBeNull();
    await waitFor(() => expect(opener).toHaveFocus());
  });
});
