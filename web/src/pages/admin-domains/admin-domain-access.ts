export interface AdminDomainCapabilities {
  canOperateDomains: boolean;
  canWriteDomains: boolean;
}

export function getAdminDomainCapabilities(
  permissions: readonly string[]
): AdminDomainCapabilities {
  const permissionSet = new Set(permissions);
  return {
    canOperateDomains: permissionSet.has("core:resource:operate"),
    canWriteDomains: permissionSet.has("core:resource:write"),
  };
}
