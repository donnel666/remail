import type {
  InventoryScope,
  ServiceMode,
  WorkbenchMessage,
  WorkbenchOrder,
  WorkbenchProduct,
  WorkbenchProject,
} from "./types";

function minutesFromNow(minutes: number) {
  return new Date(Date.now() + minutes * 60 * 1000).toISOString();
}

function minutesAgo(minutes: number) {
  return new Date(Date.now() - minutes * 60 * 1000).toISOString();
}

export const mockProjects: WorkbenchProject[] = [
  {
    description: "GitHub account verification and developer workflow mail.",
    id: "github",
    name: "GitHub",
    products: [
      {
        activationWindowMinutes: 45,
        codeInventory: 4820,
        codePrice: 0.18,
        codeWindowMinutes: 10,
        id: "github-outlook",
        label: "Outlook",
        productType: "microsoft",
        projectId: "github",
        purchaseInventory: 815,
        purchasePrice: 1.2,
        suffix: "@outlook.com",
        warrantyHours: 24,
      },
      {
        activationWindowMinutes: 45,
        codeInventory: 3150,
        codePrice: 0.2,
        codeWindowMinutes: 10,
        id: "github-hotmail",
        label: "Hotmail",
        productType: "microsoft",
        projectId: "github",
        purchaseInventory: 466,
        purchasePrice: 1.28,
        suffix: "@hotmail.com",
        warrantyHours: 24,
      },
    ],
    projectUrl: "https://github.com",
    visibility: "public",
  },
  {
    description: "Google verification code and activation testing.",
    id: "google",
    name: "Google",
    products: [
      {
        activationWindowMinutes: 45,
        codeInventory: 3160,
        codePrice: 0.22,
        codeWindowMinutes: 10,
        id: "google-outlook",
        label: "Outlook",
        productType: "microsoft",
        projectId: "google",
        purchaseInventory: 402,
        purchasePrice: 1.58,
        suffix: "@outlook.com",
        warrantyHours: 24,
      },
      {
        activationWindowMinutes: 45,
        codeInventory: 1280,
        codePrice: 0.24,
        codeWindowMinutes: 10,
        id: "google-domain",
        label: "Custom domain",
        productType: "domain",
        projectId: "google",
        purchaseInventory: 1860,
        purchasePrice: 0.96,
        suffix: "@lclaitech.com",
        warrantyHours: 12,
      },
    ],
    projectUrl: "https://google.com",
    visibility: "private",
  },
  {
    description: "Notion login link and collaboration mail.",
    id: "notion",
    name: "Notion",
    products: [
      {
        activationWindowMinutes: 45,
        codeInventory: 9800,
        codePrice: 0.12,
        codeWindowMinutes: 10,
        id: "notion-domain",
        label: "Domain alias",
        productType: "domain",
        projectId: "notion",
        purchaseInventory: 2600,
        purchasePrice: 0.9,
        suffix: "@lclaitech.com",
        warrantyHours: 12,
      },
    ],
    projectUrl: "https://notion.so",
    visibility: "public",
  },
];

const githubMessages: WorkbenchMessage[] = [
  {
    body:
      "Your GitHub verification code is 843219.\n\nIf you did not request this code, you can ignore this email.",
    id: "msg-1001",
    preview: "Your GitHub verification code is 843219.",
    receivedAt: minutesAgo(2),
    sender: "noreply@github.com",
    status: "matched",
    subject: "GitHub verification code",
    verificationCode: "843219",
  },
  {
    body:
      "We noticed a new sign-in to your account from a browser. Review your recent activity if this was not you.",
    id: "msg-1002",
    preview: "We noticed a new sign-in to your account.",
    receivedAt: minutesAgo(16),
    sender: "notifications@github.com",
    status: "received",
    subject: "New sign-in to GitHub",
  },
];

export const mockOrders: WorkbenchOrder[] = [
  {
    activationUntil: minutesFromNow(46),
    afterSaleUntil: minutesFromNow(1660),
    createdAt: minutesAgo(24),
    deliveryEmail: "mateo.richards@outlook.com",
    id: "order-1001",
    inventoryScope: "private_first",
    lastFetchedAt: minutesAgo(1),
    messages: githubMessages,
    orderNo: "RM202607070014",
    payAmount: 1.2,
    productId: "github-outlook",
    projectId: "github",
    quantity: 1,
    serviceMode: "purchase",
    serviceState: "activated",
    status: "active",
    token: "st_7jr3kfk9k2d",
    verificationCode: "843219",
  },
  {
    afterSaleUntil: minutesFromNow(7),
    createdAt: minutesAgo(3),
    deliveryEmail: "jules.parker@hotmail.com",
    id: "order-1002",
    inventoryScope: "private_first",
    lastFetchedAt: minutesAgo(1),
    messages: [],
    orderNo: "RM202607070015",
    payAmount: 0.2,
    productId: "github-hotmail",
    projectId: "github",
    quantity: 1,
    receiveUntil: minutesFromNow(7),
    serviceMode: "code",
    serviceState: "waiting_mail",
    status: "active",
    token: "st_0t2xn8zc",
  },
  {
    activationUntil: minutesFromNow(28),
    afterSaleUntil: minutesFromNow(1120),
    createdAt: minutesAgo(94),
    deliveryEmail: "center-9142@lclaitech.com",
    id: "order-1003",
    inventoryScope: "public_only",
    lastFetchedAt: minutesAgo(4),
    messages: [
      {
        body:
          "Click the secure link to continue signing in.\n\nThis email has no numeric verification code.",
        id: "msg-2001",
        preview: "Click the secure link to continue signing in.",
        receivedAt: minutesAgo(7),
        sender: "notify@notion.so",
        status: "received",
        subject: "Your login link",
      },
    ],
    orderNo: "RM202607070013",
    payAmount: 0.9,
    productId: "notion-domain",
    projectId: "notion",
    quantity: 1,
    serviceMode: "purchase",
    serviceState: "in_warranty",
    status: "active",
    token: "st_2mca51hn",
  },
  {
    afterSaleUntil: minutesFromNow(42),
    createdAt: minutesAgo(11),
    deliveryEmail: "olivia.stone@outlook.com",
    id: "order-1004",
    inventoryScope: "private_first",
    lastFetchedAt: minutesAgo(1),
    messages: [
      {
        body:
          "627084 is your Google verification code.\n\nDo not share this code with anyone.",
        id: "msg-3001",
        preview: "627084 is your Google verification code.",
        receivedAt: minutesAgo(3),
        sender: "no-reply@accounts.google.com",
        status: "matched",
        subject: "Google verification code",
        verificationCode: "627084",
      },
    ],
    orderNo: "RM202607070012",
    payAmount: 0.22,
    productId: "google-outlook",
    projectId: "google",
    quantity: 1,
    receiveUntil: minutesFromNow(42),
    serviceMode: "code",
    serviceState: "code_received",
    status: "completed",
    token: "st_81cqv3ad",
    verificationCode: "627084",
  },
];

export function createMockOrder({
  inventoryScope,
  product,
  project,
  quantity,
  serviceMode,
}: {
  inventoryScope: InventoryScope;
  product: WorkbenchProduct;
  project: WorkbenchProject;
  quantity: number;
  serviceMode: ServiceMode;
}): WorkbenchOrder {
  const sequence = String(Math.floor(Date.now() % 1000000)).padStart(6, "0");
  const name = ["mateo", "jules", "olivia", "noah", "emma"][
    Math.floor(Math.random() * 5)
  ];
  const suffix = product.suffix || "@outlook.com";
  const local =
    product.productType === "domain"
      ? `${project.id}-${Math.floor(1000 + Math.random() * 9000)}`
      : `${name}.${Math.floor(1000 + Math.random() * 9000)}`;

  return {
    activationUntil:
      serviceMode === "purchase"
        ? minutesFromNow(product.activationWindowMinutes)
        : undefined,
    afterSaleUntil:
      serviceMode === "code"
        ? minutesFromNow(product.codeWindowMinutes)
        : minutesFromNow(product.warrantyHours * 60),
    createdAt: new Date().toISOString(),
    deliveryEmail: `${local}${suffix}`,
    id: `order-${sequence}`,
    inventoryScope,
    lastFetchedAt: new Date().toISOString(),
    messages: [],
    orderNo: `RM202607${sequence}`,
    payAmount:
      serviceMode === "code"
        ? product.codePrice * quantity
        : product.purchasePrice * quantity,
    productId: product.id,
    projectId: project.id,
    quantity,
    receiveUntil:
      serviceMode === "code"
        ? minutesFromNow(product.codeWindowMinutes)
        : undefined,
    serviceMode,
    serviceState: "waiting_mail",
    status: "active",
    token: `st_${sequence}${Math.random().toString(36).slice(2, 6)}`,
  };
}
