import { describe, expect, it } from "vitest";

import { resolveRouteAuthorizationRedirect } from "./route-gate";

const defaultOptions = {
  activationNeeded: false,
  authLoading: false,
  currentUser: { permissions: ["core:resource:read"] },
  isProtectedRoute: true,
  pathname: "/admin/microsoft",
  requiredPermissions: ["core:resource:read"],
} as const;

describe("route authorization gate", () => {
  it("blocks an unauthenticated protected route until it redirects to login", () => {
    expect(
      resolveRouteAuthorizationRedirect({
        ...defaultOptions,
        currentUser: null,
      })
    ).toBe("/login");
  });

  it("blocks a user missing any required permission until it redirects to 403", () => {
    expect(
      resolveRouteAuthorizationRedirect({
        ...defaultOptions,
        requiredPermissions: [
          "core:resource:read",
          "billing:wallet:read",
        ],
      })
    ).toBe("/403");

    expect(
      resolveRouteAuthorizationRedirect({
        ...defaultOptions,
        currentUser: {
          permissions: ["core:resource:read", "billing:wallet:read"],
        },
        requiredPermissions: [
          "core:resource:read",
          "billing:wallet:read",
        ],
      })
    ).toBeNull();
  });

  it("waits for activation and authentication checks before deciding", () => {
    expect(
      resolveRouteAuthorizationRedirect({
        ...defaultOptions,
        activationNeeded: null,
        currentUser: null,
      })
    ).toBeNull();
    expect(
      resolveRouteAuthorizationRedirect({
        ...defaultOptions,
        authLoading: true,
        currentUser: null,
      })
    ).toBeNull();
  });

  it.each(["/login", "/activation", "/403"])(
    "never applies the authorization mount guard to %s",
    (pathname) => {
      expect(
        resolveRouteAuthorizationRedirect({
          ...defaultOptions,
          currentUser: null,
          pathname,
          requiredPermissions: ["missing:permission"],
        })
      ).toBeNull();
    }
  );
});
