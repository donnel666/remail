// @vitest-environment jsdom

import { afterEach, describe, expect, it } from "vitest";

import { csrfHeader } from "./api-client";

afterEach(() => {
  for (const name of ["csrf_token", "csrf_token_points_v2"]) {
    document.cookie = `${name}=; Max-Age=0; path=/`;
  }
});

describe("csrfHeader", () => {
  it("uses the csrf cookie and ignores the points-v2 namespace", () => {
    document.cookie = "csrf_token_points_v2=points-v2-csrf; path=/";
    document.cookie = "csrf_token=csrf; path=/";

    expect(csrfHeader()).toEqual({ "X-CSRF-Token": "csrf" });
  });
});
