// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";

import { exportCardKeys } from "./card-key-export";

function readBlob(blob: Blob) {
  return new Promise<string>((resolve, reject) => {
    const reader = new FileReader();
    reader.onerror = () => reject(reader.error);
    reader.onload = () => resolve(String(reader.result));
    reader.readAsText(blob);
  });
}

describe("exportCardKeys", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    document.body.innerHTML = "";
  });

  it("downloads the selected keys as one-per-line text", async () => {
    const originalCreate = Object.getOwnPropertyDescriptor(
      URL,
      "createObjectURL"
    );
    const originalRevoke = Object.getOwnPropertyDescriptor(
      URL,
      "revokeObjectURL"
    );
    const createObjectURL = vi.fn((_blob: Blob) => "blob:card-keys");
    const revokeObjectURL = vi.fn();
    Object.defineProperty(URL, "createObjectURL", {
      configurable: true,
      value: createObjectURL,
    });
    Object.defineProperty(URL, "revokeObjectURL", {
      configurable: true,
      value: revokeObjectURL,
    });
    const clickedLinks: HTMLAnchorElement[] = [];
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(
      function click(this: HTMLAnchorElement) {
        clickedLinks.push(this);
      }
    );

    try {
      exportCardKeys(["CDK-ONE", "CDK-TWO"]);

      const blob = createObjectURL.mock.calls[0][0];
      expect(await readBlob(blob)).toBe("CDK-ONE\nCDK-TWO");
      expect(clickedLinks[0]?.download).toBe("card-keys.txt");
      expect(revokeObjectURL).toHaveBeenCalledWith("blob:card-keys");
      expect(document.querySelector("a")).toBeNull();
    } finally {
      if (originalCreate) {
        Object.defineProperty(URL, "createObjectURL", originalCreate);
      } else {
        Reflect.deleteProperty(URL, "createObjectURL");
      }
      if (originalRevoke) {
        Object.defineProperty(URL, "revokeObjectURL", originalRevoke);
      } else {
        Reflect.deleteProperty(URL, "revokeObjectURL");
      }
    }
  });
});
