// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";

import { exportProxyURLs } from "./proxy-export";

function readBlob(blob: Blob) {
  return new Promise<string>((resolve, reject) => {
    const reader = new FileReader();
    reader.onerror = () => reject(reader.error);
    reader.onload = () => resolve(String(reader.result));
    reader.readAsText(blob);
  });
}

describe("exportProxyURLs", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    document.body.innerHTML = "";
  });

  it("downloads proxy URLs one per line", async () => {
    const originalCreate = Object.getOwnPropertyDescriptor(
      URL,
      "createObjectURL"
    );
    const originalRevoke = Object.getOwnPropertyDescriptor(
      URL,
      "revokeObjectURL"
    );
    const createObjectURL = vi.fn((_blob: Blob) => "blob:proxies");
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
      exportProxyURLs([
        "http://user:pass@proxy-one:8080",
        "socks5://proxy-two:1080",
      ]);

      const blob = createObjectURL.mock.calls[0][0];
      expect(await readBlob(blob)).toBe(
        "http://user:pass@proxy-one:8080\nsocks5://proxy-two:1080"
      );
      expect(clickedLinks[0]?.download).toBe("proxies.txt");
      expect(revokeObjectURL).toHaveBeenCalledWith("blob:proxies");
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
