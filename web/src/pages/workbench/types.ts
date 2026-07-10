export type InventoryScope = "private_first" | "public_only";
export type FetchSource = "auto" | "manual";
export type FetchResult = number | void;
export type FetchHandler = (source: FetchSource) => FetchResult | Promise<FetchResult>;
export type OrderStatus =
  | "pending_payment"
  | "paid"
  | "active"
  | "completed"
  | "refunded"
  | "failed"
  | "closed";
export type ProductType = "microsoft" | "domain";
export type ServiceMode = "purchase" | "code";
export type ServiceState =
  | "waiting_mail"
  | "code_received"
  | "pending_activation"
  | "activated"
  | "in_warranty"
  | "activation_timeout"
  | "read_expired"
  | "order_failed"
  | "refunded"
  | "warranty_ended";

export interface WorkbenchProject {
  description: string;
  id: string;
  inventoryLoaded?: boolean;
  logoUrl?: string;
  name: string;
  products: WorkbenchProduct[];
  projectUrl: string;
  visibility: "public" | "private";
}

export interface WorkbenchProduct {
  activationWindowMinutes: number;
  codeEnabled: boolean;
  codeInventory: number;
  codePrice: number;
  codeWindowMinutes: number;
  emailSuffix: string;
  id: string;
  label: string;
  productId: string;
  productType: ProductType;
  publicInventory: number;
  projectId: string;
  purchaseEnabled: boolean;
  purchaseInventory: number;
  purchasePrice: number;
  suffix: string;
  warrantyHours: number;
}

export interface WorkbenchMessage {
  body: string;
  id: string;
  preview: string;
  receivedAt: string;
  sender: string;
  status: "matched" | "received" | "ignored";
  subject: string;
  verificationCode?: string;
}

export interface WorkbenchOrder {
  afterSaleUntil: string;
  activationUntil?: string;
  activatedAt?: string;
  createdAt: string;
  deliveryEmail: string;
  hasDelivery: boolean;
  id: string;
  inventoryScope: InventoryScope;
  lastFetchedAt: string;
  lastMailReceivedAt?: string;
  messages: WorkbenchMessage[];
  orderNo: string;
  payAmount: number;
  productType: ProductType;
  productId: string;
  projectId: string;
  quantity: number;
  receiveUntil?: string;
  serviceMode: ServiceMode;
  serviceState: ServiceState;
  status: OrderStatus;
  token: string;
  verificationCode?: string;
}
