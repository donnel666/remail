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
    userGroup: {
      id: 2, code: "vip1", name: "VIP 1", description: "稳定使用平台服务的进阶会员", enabled: true,
      apiConcurrencyLimit: 10,
      priceDiscountRatio: "0.90", topupThreshold: "100.00", autoUpgradeEnabled: true,
      createdAt: "2024-01-01T00:00:00Z", updatedAt: "2024-01-01T00:00:00Z",
    },
    permissions,
    hasLocalPassword: true,
    enabled: true,
    createdAt: "2024-01-01T00:00:00Z",
    updatedAt: "2024-01-01T00:00:00Z",
  },
};

export const DEV_WALLET = {
  userId: 1,
  consumerBalance: "9999.00",
  supplierAvailable: "286.50",
  supplierFrozen: "42.00",
  totalRecharged: "268.00",
  historicalSpend: "128.50",
  orderCount: 12,
  updatedAt: "2024-01-01T00:00:00Z",
};

export const DEV_WALLET_TRANSACTIONS = {
  items: [
    {
      id: 3,
      transactionNo: "TX-DEV-003",
      userId: 1,
      transactionType: "transfer",
      balanceBucket: "supplier_available",
      direction: "out",
      amount: "-13.50",
      balanceBefore: "300.00",
      balanceAfter: "286.50",
      bizType: "supplier_transfer",
      bizId: "DEV-TRANSFER-1",
      createdAt: "2026-07-26T00:30:00Z",
    },
    {
      id: 2,
      transactionNo: "TX-DEV-002",
      userId: 1,
      transactionType: "freeze",
      balanceBucket: "supplier_frozen",
      direction: "in",
      amount: "42.00",
      balanceBefore: "0.00",
      balanceAfter: "42.00",
      bizType: "settlement",
      bizId: "DEV-SETTLEMENT-2",
      createdAt: "2026-07-25T06:20:00Z",
    },
    {
      id: 1,
      transactionNo: "TX-DEV-001",
      userId: 1,
      transactionType: "credit",
      balanceBucket: "supplier_available",
      direction: "in",
      amount: "300.00",
      balanceBefore: "0.00",
      balanceAfter: "300.00",
      bizType: "settlement",
      bizId: "DEV-SETTLEMENT-1",
      createdAt: "2026-07-24T03:10:00Z",
    },
  ],
  hasNext: false,
  limit: 100,
};

export const DEV_USER_GROUPS = {
  groups: [
    {
      id: 1, code: "normal", name: "普通用户", description: "默认会员等级", enabled: true,
      apiConcurrencyLimit: 3,
      priceDiscountRatio: "1.00", topupThreshold: "0.00", autoUpgradeEnabled: false,
    },
    DEV_ME.user.userGroup,
    {
      id: 3, code: "vip2", name: "VIP 2", description: "面向高频业务的高级会员", enabled: true,
      apiConcurrencyLimit: 20,
      priceDiscountRatio: "0.80", topupThreshold: "500.00", autoUpgradeEnabled: true,
    },
  ],
};
