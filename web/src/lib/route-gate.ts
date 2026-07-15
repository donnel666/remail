export type RouteAuthorizationRedirect = "/login" | "/403" | null;

interface ResolveRouteAuthorizationRedirectOptions {
  activationNeeded: boolean | null;
  authLoading: boolean;
  currentUser: { permissions: readonly string[] } | null;
  isProtectedRoute: boolean;
  pathname: string;
  requiredPermissions: readonly string[];
}

const ROUTE_AUTHORIZATION_BYPASS_PATHS = new Set([
  "/activation",
  "/login",
  "/403",
]);

export function resolveRouteAuthorizationRedirect({
  activationNeeded,
  authLoading,
  currentUser,
  isProtectedRoute,
  pathname,
  requiredPermissions,
}: ResolveRouteAuthorizationRedirectOptions): RouteAuthorizationRedirect {
  if (
    activationNeeded !== false ||
    authLoading ||
    ROUTE_AUTHORIZATION_BYPASS_PATHS.has(pathname)
  ) {
    return null;
  }

  if (!currentUser && isProtectedRoute) {
    return "/login";
  }

  if (
    currentUser &&
    requiredPermissions.some(
      (permission) => !currentUser.permissions.includes(permission)
    )
  ) {
    return "/403";
  }

  return null;
}
