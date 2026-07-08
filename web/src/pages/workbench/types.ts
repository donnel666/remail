export type InventoryScope = "private_first" | "public_only";
export type FetchSource = "auto" | "manual";
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
  | "activated"
  | "in_warranty"
  | "activation_timeout"
  | "read_expired";

export interface WorkbenchProject {
  description: string;
  id: string;
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
  id: string;
  label: string;
  productType: ProductType;
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
  createdAt: string;
  deliveryEmail: string;
  id: string;
  inventoryScope: InventoryScope;
  lastFetchedAt: string;
  messages: WorkbenchMessage[];
  orderNo: string;
  payAmount: number;
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
