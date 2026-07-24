// Loaded through DEV-only dynamic imports. Production builds must not emit this module.

const permissions = [
  "iam:user:read", "iam:user:write", "iam:user:operate",
  "iam:user_group:read", "iam:user_group:write",
  "iam:permission:read", "iam:permission:write", "iam:permission:sensitive",
  "system:settings:read", "system:settings:write", "system:settings:sensitive",
  "iam:invite:read", "iam:invite:write", "iam:invite:operate",
  "iam:supplier_application:read", "iam:supplier_application:operate",
  "core:resource:read", "core:resource:write", "core:resource:operate",
  "core:project:read", "core:project:write", "core:project:operate",
  "trade:order:read", "trade:order:write", "trade:order:operate",
  "billing:wallet:read", "billing:wallet:write", "billing:wallet:operate", "billing:wallet:sensitive",
  "billing:card:read", "billing:card:write", "billing:card:operate", "billing:card:sensitive",
  "proxy:proxy:read", "proxy:proxy:write", "proxy:proxy:operate",
  "alloc:allocation:read", "alloc:allocation:operate",
  "mailmatch:message:read", "mailmatch:message:operate",
  "mailtransport:binding:read", "mailtransport:binding:write",
  "governance:task:read", "governance:log:read", "governance:log:operate",
];

export const DEV_ACTIVATION = { needed: false };

export const DEV_ME = {
  user: {
    id: 1,
    email: "admin@remail.dev",
    nickname: "Admin",
    role: "super_admin",
    userGroup: { id: 1, code: "super_admin", name: "超级管理员", description: "", enabled: true, createdAt: "2024-01-01T00:00:00Z", updatedAt: "2024-01-01T00:00:00Z" },
    permissions,
    enabled: true,
    createdAt: "2024-01-01T00:00:00Z",
    updatedAt: "2024-01-01T00:00:00Z",
  },
};

export const DEV_WALLET = {
  userId: 1,
  consumerBalance: "9999.00",
  supplierAvailable: "0.00",
  supplierFrozen: "0.00",
  historicalSpend: "128.50",
  orderCount: 12,
  updatedAt: "2024-01-01T00:00:00Z",
};
