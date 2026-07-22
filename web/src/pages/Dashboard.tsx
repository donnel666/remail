import { useEffect, useMemo, useRef, useState } from "react";
import { Toast } from "@douyinfe/semi-ui";
import { useTranslation } from "react-i18next";

import { IamApiError } from "@/lib/api-client";
import {
  createOrder,
  createOrderBatch,
  getOrder,
  listOrders,
  type OrderResponse,
} from "@/lib/orders-api";
import {
  readPickupMail,
  readPickupMailBatch,
  readPickupMessage,
  type OrderMailResponse,
} from "@/lib/mailmatch-api";
import {
  getProjectInventory,
  listProjects,
  type ProjectInventoryTotalResponse,
  type ProjectItem,
  type ProjectProductSummary,
} from "@/lib/projects-api";

import { MailboxClientModal } from "./workbench/mailbox-client";
import { ApplyProjectModal } from "./apply-project-modal";
import {
  clearCheckoutAttempt,
  loadCheckoutAttempt,
  saveCheckoutAttempt,
  type CheckoutAttempt,
} from "./workbench/checkout-attempt";
import {
  mergeOrderRuntimeState,
  shouldAutoFetchOrderMail,
} from "./workbench/order-runtime";
import { OrderPanel } from "./workbench/order-panel";
import { ProductPickerPanel } from "./workbench/product-picker-panel";
import { ProjectListPanel } from "./workbench/project-list-panel";
import type {
  FetchSource,
  InventoryScope,
  ServiceMode,
  ServiceState,
  WorkbenchOrder,
  WorkbenchMessage,
  WorkbenchProduct,
  WorkbenchProject,
} from "./workbench/types";
import { matchesProjectEmailSearch } from "./workbench/utils";

function filterProjects(projects: WorkbenchProject[], search: string) {
  const q = search.trim().toLowerCase();
  const filtered = q
    ? projects.filter((project) =>
        [project.name, project.description, project.projectUrl]
          .join(" ")
          .toLowerCase()
          .includes(q),
      )
    : projects;
  return [...filtered].sort((a, b) => {
    if (a.visibility === b.visibility) return a.name.localeCompare(b.name);
    return a.visibility === "private" ? -1 : 1;
  });
}

function filterProducts(
  products: WorkbenchProduct[],
  search: string,
  serviceMode: ServiceMode,
) {
  const q = search.trim().toLowerCase();
  return products
    .filter((product) =>
      serviceMode === "code" ? product.codeEnabled : product.purchaseEnabled,
    )
    .filter((product) =>
      q
        ? [
            product.label,
            product.suffix,
            product.emailSuffix,
            product.productType,
          ]
            .join(" ")
            .toLowerCase()
            .includes(q)
        : true,
    );
}

type ProductInventoryTotal = ProjectInventoryTotalResponse["products"][number];

const maxCreateOrderQuantity = 100;
const inventoryFetchConcurrency = 6;
const orderPageLimit = 100;
const initialOrderCursors: Record<ServiceMode, number | undefined> = {
  code: undefined,
  purchase: undefined,
};
const initialOrderHasMore: Record<ServiceMode, boolean> = {
  code: false,
  purchase: false,
};

function toWorkbenchProject(
  project: ProjectItem,
  inventory?: ProjectInventoryTotalResponse,
): WorkbenchProject {
  const inventoryByProductId = new Map(
    (inventory?.products ?? []).map((item) => [String(item.productId), item]),
  );
  return {
    description: project.description ?? "",
    id: String(project.id),
    inventoryLoaded: Boolean(inventory),
    logoUrl: project.logoUrl,
    name: project.name,
    products: (project.products ?? []).flatMap((product) =>
      toWorkbenchProducts(
        project.id,
        product,
        inventoryByProductId.get(String(product.id)),
      ),
    ),
    projectUrl: project.targetPlatform,
    visibility: project.accessType,
  };
}

function toWorkbenchProducts(
  projectId: number,
  product: ProjectProductSummary,
  inventory?: ProductInventoryTotal,
): WorkbenchProduct[] {
  const label = product.type === "microsoft" ? "Microsoft" : "Domain";
  const totalAvailable =
    inventory?.totalAvailable ?? product.totalAvailable ?? 0;
  const publicAvailable =
    inventory?.publicAvailable ?? product.publicAvailable ?? 0;
  const baseProduct: WorkbenchProduct = {
    activationWindowMinutes: product.activationWindowMinutes,
    codeEnabled: product.codeEnabled,
    codeInventory: totalAvailable,
    codePrice: moneyToNumber(product.codePrice),
    codeWindowMinutes: product.codeWindowMinutes,
    emailSuffix: "",
    id: String(product.id),
    label,
    productId: String(product.id),
    productType: product.type,
    publicInventory: publicAvailable,
    projectId: String(projectId),
    purchaseEnabled: product.purchaseEnabled,
    purchaseInventory: totalAvailable,
    purchasePrice: moneyToNumber(product.purchasePrice),
    suffix: label,
    warrantyHours: Math.max(1, Math.ceil(product.warrantyMinutes / 60)),
  };
  const suffixProducts = (inventory?.suffixes ?? product.suffixes ?? [])
    .map((suffix) => ({
      ...suffix,
      suffix: String(suffix.suffix ?? "").replace(/^@/, ""),
    }))
    .filter((suffix) => suffix.suffix)
    .map((suffix) => ({
      ...baseProduct,
      codeInventory: suffix.totalAvailable ?? 0,
      emailSuffix: suffix.suffix,
      id: `${product.id}:${suffix.suffix}`,
      publicInventory: suffix.publicAvailable ?? 0,
      purchaseInventory: suffix.totalAvailable ?? 0,
      suffix: `@${suffix.suffix}`,
    }));
  return [baseProduct, ...suffixProducts];
}

function mergeProjectInventory(
  project: WorkbenchProject,
  inventory: ProjectInventoryTotalResponse,
): WorkbenchProject {
  const inventoryByProductId = new Map(
    (inventory.products ?? []).map((item) => [String(item.productId), item]),
  );
  const baseProducts = project.products.filter(
    (product) => product.id === product.productId,
  );
  return {
    ...project,
    inventoryLoaded: true,
    products: baseProducts.flatMap((product) => {
      const inventoryItem = inventoryByProductId.get(product.productId);
      if (!inventoryItem) return [product];
      const totalAvailable = inventoryItem.totalAvailable ?? 0;
      const publicAvailable = inventoryItem.publicAvailable ?? 0;
      const baseProduct: WorkbenchProduct = {
        ...product,
        codeInventory: totalAvailable,
        emailSuffix: "",
        id: product.productId,
        publicInventory: publicAvailable,
        purchaseInventory: totalAvailable,
        suffix: product.label,
      };
      const suffixProducts = (inventoryItem.suffixes ?? [])
        .map((suffix) => ({
          ...suffix,
          suffix: String(suffix.suffix ?? "").replace(/^@/, ""),
        }))
        .filter((suffix) => suffix.suffix)
        .map((suffix) => ({
          ...baseProduct,
          codeInventory: suffix.totalAvailable ?? 0,
          emailSuffix: suffix.suffix,
          id: `${product.productId}:${suffix.suffix}`,
          publicInventory: suffix.publicAvailable ?? 0,
          purchaseInventory: suffix.totalAvailable ?? 0,
          suffix: `@${suffix.suffix}`,
        }));
      return [baseProduct, ...suffixProducts];
    }),
  };
}

function toWorkbenchOrder(order: OrderResponse): WorkbenchOrder {
  return {
    activationUntil:
      order.serviceMode === "purchase"
        ? (order.receiveUntil ?? order.afterSaleUntil ?? undefined)
        : undefined,
    activatedAt: order.activatedAt ?? undefined,
    afterSaleUntil:
      order.afterSaleUntil ?? order.receiveUntil ?? order.updatedAt,
    createdAt: order.createdAt,
    deliveryEmail: order.deliveryEmail,
    hasDelivery: order.hasDelivery ?? false,
    id: String(order.id),
    inventoryScope: order.supplyPolicy,
    lastFetchedAt: order.lastMailReceivedAt ?? order.updatedAt,
    lastMailReceivedAt: order.lastMailReceivedAt ?? undefined,
    messages: [],
    orderNo: order.orderNo,
    payAmount: moneyToNumber(order.payAmount),
    productType: order.productType,
    productId: String(order.projectProductId),
    projectId: String(order.projectId),
    quantity: 1,
    receiveUntil:
      order.serviceMode === "code"
        ? (order.receiveUntil ?? undefined)
        : undefined,
    serviceMode: order.serviceMode,
    serviceState: orderServiceState(order),
    status: order.status,
    token: order.serviceToken ?? "",
    verificationCode: order.verificationCode ?? undefined,
  };
}

function toWorkbenchMessages(
  items: OrderMailResponse["items"],
): WorkbenchMessage[] {
  return items.map((item) => {
    return {
      body: "",
      id: String(item.id),
      preview: item.bodyPreview,
      receivedAt: item.receivedAt,
      recipient: item.recipient,
      sender: item.sender,
      status: "matched",
      subject: item.subject || "(No subject)",
      verificationCode: item.verificationCode,
    };
  });
}

function messageTimestamp(value: string) {
  const time = Date.parse(value);
  return Number.isFinite(time) ? time : 0;
}

function latestVerificationMessage(messages: WorkbenchMessage[]) {
  return [...messages]
    .filter((item) => item.verificationCode)
    .sort(
      (a, b) => messageTimestamp(b.receivedAt) - messageTimestamp(a.receivedAt),
    )[0];
}

function orderServiceState(order: OrderResponse): ServiceState {
  if (order.status === "completed") {
    return order.serviceMode === "code" ? "code_received" : "warranty_ended";
  }
  if (order.status === "active") {
    if (order.serviceMode === "purchase") {
      return order.activatedAt ? "in_warranty" : "pending_activation";
    }
    return "waiting_mail";
  }
  if (order.status === "failed") {
    return "order_failed";
  }
  if (order.status === "refunded") {
    return "refunded";
  }
  return "waiting_mail";
}

function moneyToNumber(value?: string) {
  const parsed = Number.parseFloat(value ?? "0");
  return Number.isFinite(parsed) ? parsed : 0;
}

function getProductInventory(
  product: WorkbenchProduct | undefined,
  serviceMode: ServiceMode,
  inventoryScope: InventoryScope,
) {
  if (!product) return 0;
  if (inventoryScope === "public_only") return product.publicInventory;
  return serviceMode === "code"
    ? product.codeInventory
    : product.purchaseInventory;
}

function clampQuantity(value: number, inventory: number) {
  if (inventory <= 0) return 0;
  if (!Number.isFinite(value)) return 1;
  return Math.max(
    1,
    Math.min(Math.trunc(value), inventory, maxCreateOrderQuantity),
  );
}

function checkoutBatchSignature(input: {
  inventoryScope: InventoryScope;
  productId: string;
  projectId: string;
  quantity: number;
  serviceMode: ServiceMode;
  suffix: string;
}) {
  return [
    input.serviceMode,
    input.inventoryScope,
    input.projectId,
    input.productId,
    input.suffix,
    input.quantity,
  ].join("|");
}

function apiErrorMessage(err: unknown, fallback: string) {
  if (err instanceof IamApiError && err.message) return err.message;
  if (err instanceof Error && err.message) return err.message;
  return fallback;
}

export default function Dashboard() {
  const { t } = useTranslation();
  const [applyOpen, setApplyOpen] = useState(false);
  const [creating, setCreating] = useState(false);
  const [inventoryScope, setInventoryScope] =
    useState<InventoryScope>("private_first");
  const [mailClientParams, setMailClientParams] = useState<{
    email: string;
    orderNo: string;
    token: string;
  } | null>(null);
  const [orderSearch, setOrderSearch] = useState("");
  const [orders, setOrders] = useState<WorkbenchOrder[]>([]);
  const [orderCursors, setOrderCursors] =
    useState<Record<ServiceMode, number | undefined>>(initialOrderCursors);
  const [orderHasMore, setOrderHasMore] =
    useState<Record<ServiceMode, boolean>>(initialOrderHasMore);
  const [loadingMoreOrders, setLoadingMoreOrders] = useState(false);
  const [productSearch, setProductSearch] = useState("");
  const [projectSearch, setProjectSearch] = useState("");
  const [projects, setProjects] = useState<WorkbenchProject[]>([]);
  const [quantity, setQuantity] = useState(1);
  const [selectedOrderNo, setSelectedOrderNo] = useState("");
  const [selectedProductId, setSelectedProductId] = useState("");
  const [selectedProjectId, setSelectedProjectId] = useState("");
  const [serviceMode, setServiceMode] = useState<ServiceMode>("purchase");
  const fetchInFlightRef = useRef(new Map<string, Promise<number | void>>());
  const fetchSeqRef = useRef(new Map<string, number>());
  const refreshOrdersSeqRef = useRef(new Map<ServiceMode, number>());
  const loadingMoreOrdersRef = useRef(false);
  const checkoutAttemptRef = useRef<CheckoutAttempt | null>(null);

  const projectsById = useMemo(() => {
    return new Map(projects.map((project) => [project.id, project]));
  }, [projects]);

  const productsById = useMemo(() => {
    return new Map(
      projects.flatMap((project) =>
        project.products.map((product) => [product.id, product] as const),
      ),
    );
  }, [projects]);

  const filteredProjects = useMemo(
    () => filterProjects(projects, projectSearch),
    [projects, projectSearch],
  );

  const selectedProject =
    projectsById.get(selectedProjectId) ?? filteredProjects[0];

  const filteredProducts = useMemo(
    () =>
      filterProducts(
        selectedProject?.products ?? [],
        productSearch,
        serviceMode,
      ),
    [productSearch, selectedProject?.products, serviceMode],
  );

  useEffect(() => {
    void loadWorkbenchProjects();
  }, []);

  useEffect(() => {
    void refreshOrders(serviceMode);
  }, [serviceMode]);

  useEffect(() => {
    if (!selectedProjectId && filteredProjects[0]) {
      setSelectedProjectId(filteredProjects[0].id);
    }
  }, [filteredProjects, selectedProjectId]);

  useEffect(() => {
    if (!selectedProjectId) return;
    void loadProjectInventory(selectedProjectId);
  }, [selectedProjectId]);

  useEffect(() => {
    if (!selectedProject) return;
    if (
      selectedProject.products.some(
        (product) => product.id === selectedProductId,
      )
    ) {
      return;
    }
    setSelectedProductId(
      filterProducts(selectedProject.products, productSearch, serviceMode)[0]
        ?.id ?? "",
    );
  }, [productSearch, selectedProject, selectedProductId, serviceMode]);

  useEffect(() => {
    if (filteredProducts.some((product) => product.id === selectedProductId)) {
      return;
    }
    setSelectedProductId(
      filteredProducts[0]?.id ?? selectedProject?.products[0]?.id ?? "",
    );
  }, [filteredProducts, selectedProductId, selectedProject?.products]);

  const selectedProduct =
    productsById.get(selectedProductId) ??
    filteredProducts[0] ??
    selectedProject?.products[0];
  const selectedInventory = getProductInventory(
    selectedProduct,
    serviceMode,
    inventoryScope,
  );

  useEffect(() => {
    setQuantity((current) => clampQuantity(current, selectedInventory));
  }, [selectedInventory]);

  const visibleOrders = useMemo(() => {
    return orders
      .filter((order) => order.serviceMode === serviceMode)
      .filter((order) =>
        matchesProjectEmailSearch(
          order,
          orderSearch,
          projectsById.get(order.projectId)?.name,
        ),
      );
  }, [orderSearch, orders, projectsById, serviceMode]);

  useEffect(() => {
    if (!selectedOrderNo) return;
    if (visibleOrders.some((order) => order.orderNo === selectedOrderNo))
      return;
    setSelectedOrderNo("");
  }, [selectedOrderNo, visibleOrders]);

  const selectedOrder = visibleOrders.find(
    (order) => order.orderNo === selectedOrderNo,
  );
  const mailClientOrder = mailClientParams
    ? orders.find(
        (order) =>
          order.orderNo === mailClientParams.orderNo &&
          order.deliveryEmail === mailClientParams.email &&
          order.token === mailClientParams.token,
      )
    : undefined;
  const hasMoreOrders = orderHasMore[serviceMode];

  async function loadWorkbenchProjects() {
    try {
      const list = await listProjects(
        { scope: "visible", status: "listed" },
        0,
        100,
      );
      const listed = list.items.filter(
        (project) => project.status === "listed",
      );
      setProjects(listed.map((project) => toWorkbenchProject(project)));
      // Totals aren't in the project list payload; fetch them for every project
      // so the list shows stock without needing a selection.
      void loadAllProjectInventory(listed.map((project) => String(project.id)));
    } catch (err) {
      Toast.error(apiErrorMessage(err, t("An unexpected error occurred.")));
    }
  }

  async function loadAllProjectInventory(projectIds: string[]) {
    // ponytail: capped per-project fan-out; add a bulk inventory endpoint if
    // project counts grow past a couple hundred. Backend caches each project inventory.
    for (
      let start = 0;
      start < projectIds.length;
      start += inventoryFetchConcurrency
    ) {
      await Promise.all(
        projectIds
          .slice(start, start + inventoryFetchConcurrency)
          .map((id) => loadProjectInventory(id, { silent: true })),
      );
    }
  }

  async function loadProjectInventory(
    projectId: string,
    { silent = false }: { silent?: boolean } = {},
  ) {
    const numericProjectId = Number(projectId);
    if (!Number.isInteger(numericProjectId) || numericProjectId <= 0) return;
    try {
      const inventory = await getProjectInventory(numericProjectId);
      setProjects((prev) =>
        prev.map((project) =>
          project.id === projectId
            ? mergeProjectInventory(project, inventory)
            : project,
        ),
      );
    } catch (err) {
      if (!silent) {
        Toast.error(apiErrorMessage(err, t("An unexpected error occurred.")));
      }
    }
  }

  async function refreshOrders(mode: ServiceMode = serviceMode) {
    const seq = (refreshOrdersSeqRef.current.get(mode) ?? 0) + 1;
    refreshOrdersSeqRef.current.set(mode, seq);
    try {
      const list = await listOrders({
        limit: orderPageLimit,
        serviceMode: mode,
        status: "active",
      });
      if (refreshOrdersSeqRef.current.get(mode) !== seq) return;
      const nextOrders = list.items.map(toWorkbenchOrder);
      setOrderCursors((prev) => ({ ...prev, [mode]: list.nextAfterId }));
      setOrderHasMore((prev) => ({ ...prev, [mode]: list.hasNext }));
      setOrders((prev) => {
        const currentByOrderNo = new Map(
          prev.map((order) => [order.orderNo, order])
        );
        return [
          ...nextOrders.map((order) =>
            mergeOrderRuntimeState(order, currentByOrderNo.get(order.orderNo))
          ),
          ...prev.filter((order) => order.serviceMode !== mode),
        ];
      });
    } catch (err) {
      Toast.error(apiErrorMessage(err, t("An unexpected error occurred.")));
    }
  }

  async function loadMoreOrders() {
    if (loadingMoreOrdersRef.current) return;
    const mode = serviceMode;
    const afterId = orderCursors[mode];
    if (!orderHasMore[mode] || !afterId) return;
    const refreshSeq = refreshOrdersSeqRef.current.get(mode) ?? 0;
    loadingMoreOrdersRef.current = true;
    setLoadingMoreOrders(true);
    try {
      const list = await listOrders({
        afterId,
        limit: orderPageLimit,
        serviceMode: mode,
        status: "active",
      });
      if (refreshOrdersSeqRef.current.get(mode) !== refreshSeq) return;
      const nextOrders = list.items.map(toWorkbenchOrder);
      setOrderCursors((prev) => ({ ...prev, [mode]: list.nextAfterId }));
      setOrderHasMore((prev) => ({ ...prev, [mode]: list.hasNext }));
      setOrders((prev) => {
        const currentByOrderNo = new Map(
          prev.map((order) => [order.orderNo, order])
        );
        const additions = nextOrders
          .filter((order) => !currentByOrderNo.has(order.orderNo))
          .map((order) => mergeOrderRuntimeState(order));
        return [...prev, ...additions];
      });
    } catch (err) {
      Toast.error(apiErrorMessage(err, t("An unexpected error occurred.")));
    } finally {
      loadingMoreOrdersRef.current = false;
      setLoadingMoreOrders(false);
    }
  }

  async function loadOrderDetail(orderNo: string) {
    const detail = toWorkbenchOrder(await getOrder(orderNo));
    setOrders((prev) =>
      prev.map((item) =>
        item.orderNo === orderNo
          ? {
              ...detail,
              hasDelivery: detail.hasDelivery || item.hasDelivery,
              messages: item.messages,
              lastFetchedAt:
                detail.lastMailReceivedAt ?? item.lastFetchedAt,
              lastMailReceivedAt:
                detail.lastMailReceivedAt ?? item.lastMailReceivedAt,
              verificationCode:
                detail.verificationCode || item.verificationCode,
            }
          : item,
      ),
    );
    return detail;
  }

  function handleSelectProject(projectId: string) {
    const project = projectsById.get(projectId);
    setSelectedProjectId(projectId);
    setProductSearch("");
    setSelectedProductId(
      filterProducts(project?.products ?? [], "", serviceMode)[0]?.id ?? "",
    );
  }

  async function handleCreateOrder() {
    if (!selectedProject || !selectedProduct || creating) return;
    const requestedQuantity = clampQuantity(quantity, selectedInventory);
    if (requestedQuantity <= 0) return;
    setCreating(true);
    const signature = checkoutBatchSignature({
      inventoryScope,
      productId: selectedProduct.id,
      projectId: selectedProject.id,
      quantity: requestedQuantity,
      serviceMode,
      suffix: selectedProduct.emailSuffix,
    });
    const attempt =
      checkoutAttemptRef.current?.signature === signature
        ? checkoutAttemptRef.current
        : loadCheckoutAttempt(signature);
    checkoutAttemptRef.current = attempt;
    saveCheckoutAttempt(attempt);
    try {
      const payload = {
        emailSuffix: selectedProduct.emailSuffix || undefined,
        productId: Number(selectedProduct.productId),
        projectId: Number(selectedProject.id),
      };
      const options = {
        idempotencyKey: attempt.key,
        serviceMode,
        supply: inventoryScope,
      };
      let createdOrders: OrderResponse[];
      let failedItems: Array<{ error?: { message: string } }> = [];
      if (requestedQuantity === 1) {
        createdOrders = [await createOrder(payload, options)];
      } else {
        const results = await createOrderBatch(
          { ...payload, quantity: requestedQuantity },
          options,
        );
        createdOrders = results.flatMap((item) =>
          item.status === "succeeded" && item.order ? [item.order] : [],
        );
        failedItems = results.filter((item) => item.status === "failed");
      }
      const nextOrders = createdOrders.map(toWorkbenchOrder);
      const nextOrderNos = new Set(nextOrders.map((order) => order.orderNo));
      setOrders((prev) => [
        ...nextOrders,
        ...prev.filter((item) => !nextOrderNos.has(item.orderNo)),
      ]);
      if (nextOrders[0]) setSelectedOrderNo(nextOrders[0].orderNo);
      clearCheckoutAttempt(attempt);
      checkoutAttemptRef.current = null;
      if (failedItems.length === 0) {
        Toast.success(t("Order created."));
      } else {
        Toast.error(
          `${failedItems[0]?.error?.message ?? t("An unexpected error occurred.")} (${createdOrders.length}/${requestedQuantity})`,
        );
      }
      if (requestedQuantity > 1 && nextOrders.length > 0) {
        void handleFetchOrderMailBatch(nextOrders);
      }
      void refreshOrders();
      void loadProjectInventory(selectedProject.id);
    } catch (err) {
      void refreshOrders();
      void loadProjectInventory(selectedProject.id);
      Toast.error(apiErrorMessage(err, t("An unexpected error occurred.")));
    } finally {
      setCreating(false);
    }
  }

  async function handleFetchOrderMail(
    order: WorkbenchOrder,
    source: FetchSource,
  ) {
    if (source === "auto" && !shouldAutoFetchOrderMail(order)) return;
    const existing = fetchInFlightRef.current.get(order.orderNo);
    if (existing) return existing;

    const seq = (fetchSeqRef.current.get(order.orderNo) ?? 0) + 1;
    fetchSeqRef.current.set(order.orderNo, seq);
    const request = (async () => {
      try {
        let target = order;
        if (!target.token) {
          target = await loadOrderDetail(order.orderNo);
        }
        if (!target.deliveryEmail || !target.token) {
          Toast.error(t("Service credential is unavailable."));
          return;
        }
        const result = await readPickupMail(target.deliveryEmail, target.token);
        if (fetchSeqRef.current.get(target.orderNo) !== seq) return;

        const messages = toWorkbenchMessages(result.items);
        const latestDelivery = latestVerificationMessage(messages);
        const latestCode = latestDelivery?.verificationCode;
        const lastFetchedAt =
          result.fetch?.lastReceivedAt ??
          result.fetch?.lastSuccessAt ??
          result.fetch?.lastSubmittedAt ??
          new Date().toISOString();
        let refreshedDetail: WorkbenchOrder | undefined;
        if (messages.length > 0) {
          try {
            refreshedDetail = toWorkbenchOrder(await getOrder(target.orderNo));
          } catch {
            refreshedDetail = undefined;
          }
        }
        if (fetchSeqRef.current.get(target.orderNo) !== seq) return;
        setOrders((prev) =>
          prev.map((item) =>
            item.orderNo === target.orderNo
              ? {
                  ...(refreshedDetail ?? item),
                  messages,
                  hasDelivery:
                    Boolean(latestCode) ||
                    refreshedDetail?.hasDelivery ||
                    item.hasDelivery,
                  lastFetchedAt,
                  lastMailReceivedAt:
                    refreshedDetail?.lastMailReceivedAt ??
                    latestDelivery?.receivedAt ??
                    item.lastMailReceivedAt,
                  verificationCode: latestCode ?? item.verificationCode,
                  serviceState: latestCode
                    ? target.serviceMode === "code"
                      ? "code_received"
                      : "in_warranty"
                    : (refreshedDetail?.serviceState ?? item.serviceState),
                }
              : item,
          ),
        );
        if (result.fetch?.nextFetchAllowedAt) {
          return Math.max(
            1,
            Math.ceil(
              (Date.parse(result.fetch.nextFetchAllowedAt) - Date.now()) / 1000
            )
          );
        }
        return 5;
      } catch (err) {
        if (err instanceof IamApiError && err.status === 429) {
          return err.retryAfterSeconds;
        }
        if (source === "manual") {
          Toast.error(apiErrorMessage(err, t("An unexpected error occurred.")));
        }
      } finally {
        fetchInFlightRef.current.delete(order.orderNo);
      }
    })();
    fetchInFlightRef.current.set(order.orderNo, request);
    return request;
  }

  function handleFetchOrderMailBatch(batchOrders: WorkbenchOrder[]) {
    const targets = batchOrders.filter(
      (order) =>
        order.deliveryEmail &&
        order.token &&
        shouldAutoFetchOrderMail(order) &&
        !fetchInFlightRef.current.has(order.orderNo),
    );
    if (targets.length < 2) {
      if (targets[0]) return handleFetchOrderMail(targets[0], "auto");
      return;
    }
    const sequences = new Map(
      targets.map((order) => {
        const seq = (fetchSeqRef.current.get(order.orderNo) ?? 0) + 1;
        fetchSeqRef.current.set(order.orderNo, seq);
        return [order.orderNo, seq] as const;
      }),
    );
    const requestRef: { current?: Promise<void> } = {};
    const request = (async () => {
      try {
        const results = await readPickupMailBatch(
          targets.map((order) => ({
            email: order.deliveryEmail,
            token: order.token,
          })),
        );
        const byOrderNo = new Map(
          results.flatMap((item) => {
            const order = targets[item.index];
            return item.status === "succeeded" && item.data && order
              ? [[order.orderNo, item.data] as const]
              : [];
          }),
        );
        setOrders((prev) =>
          prev.map((order) => {
            const result = byOrderNo.get(order.orderNo);
            if (
              !result ||
              fetchSeqRef.current.get(order.orderNo) !==
                sequences.get(order.orderNo)
            ) {
              return order;
            }
            const messages = toWorkbenchMessages(result.items);
            const latestDelivery = latestVerificationMessage(messages);
            const latestCode = latestDelivery?.verificationCode;
            return {
              ...order,
              messages,
              hasDelivery: Boolean(latestCode) || order.hasDelivery,
              lastFetchedAt:
                result.fetch?.lastReceivedAt ??
                result.fetch?.lastSuccessAt ??
                result.fetch?.lastSubmittedAt ??
                new Date().toISOString(),
              lastMailReceivedAt:
                latestDelivery?.receivedAt ?? order.lastMailReceivedAt,
              verificationCode: latestCode ?? order.verificationCode,
              serviceState: latestCode
                ? order.serviceMode === "code"
                  ? "code_received"
                  : "in_warranty"
                : order.serviceState,
            };
          }),
        );
      } catch {
        // Individual order controls remain available when the whole request fails.
      } finally {
        for (const order of targets) {
          if (
            fetchInFlightRef.current.get(order.orderNo) === requestRef.current
          ) {
            fetchInFlightRef.current.delete(order.orderNo);
          }
        }
      }
    })();
    requestRef.current = request;
    for (const order of targets) {
      fetchInFlightRef.current.set(order.orderNo, request);
    }
    return request;
  }

  function handleSelectOrder(orderNo: string) {
    setSelectedOrderNo((current) => {
      if (current === orderNo) return "";
      const order = orders.find((item) => item.orderNo === orderNo);
      if (order && !order.token) {
        void loadOrderDetail(orderNo)
          .then((detail) => {
            if (!detail.hasDelivery && shouldAutoFetchOrderMail(detail)) {
              void handleFetchOrderMail(detail, "auto");
            }
          })
          .catch((err) =>
            Toast.error(
              apiErrorMessage(err, t("An unexpected error occurred.")),
            ),
          );
      } else if (
        order &&
        !order.hasDelivery &&
        shouldAutoFetchOrderMail(order)
      ) {
        void handleFetchOrderMail(order, "auto");
      }
      return orderNo;
    });
  }

  function handleOpenMailbox(params: {
    email: string;
    orderNo: string;
    token: string;
  }) {
    if (params.token) {
      setMailClientParams(params);
      const order = orders.find((item) => item.orderNo === params.orderNo);
      if (order && shouldAutoFetchOrderMail(order)) {
        void handleFetchOrderMail(order, "auto");
      }
      return;
    }
    void loadOrderDetail(params.orderNo)
      .then((order) => {
        setMailClientParams({
          email: order.deliveryEmail,
          orderNo: order.orderNo,
          token: order.token,
        });
        if (shouldAutoFetchOrderMail(order)) {
          void handleFetchOrderMail(order, "auto");
        }
      })
      .catch((err) =>
        Toast.error(apiErrorMessage(err, t("An unexpected error occurred."))),
      );
  }

  return (
    <>
      <div className="workbench-responsive-shell">
        <div className="workbench-layout">
          <ProjectListPanel
            onApply={() => setApplyOpen(true)}
            onSearchChange={setProjectSearch}
            onSelectProject={handleSelectProject}
            projects={filteredProjects}
            search={projectSearch}
            selectedProjectId={selectedProjectId}
            serviceMode={serviceMode}
          />

          <ProductPickerPanel
            inventoryScope={inventoryScope}
            onInventoryScopeChange={setInventoryScope}
            onProductSearchChange={setProductSearch}
            onSelectProduct={setSelectedProductId}
            onServiceModeChange={(mode) => {
              setServiceMode(mode);
            }}
            productSearch={productSearch}
            products={filteredProducts}
            selectedProductId={selectedProductId}
            selectedProject={selectedProject}
            serviceMode={serviceMode}
          />

          <OrderPanel
            creating={creating}
            hasMoreOrders={hasMoreOrders}
            inventoryScope={inventoryScope}
            loadingMoreOrders={loadingMoreOrders}
            onCreateOrder={handleCreateOrder}
            onFetchOrderMail={handleFetchOrderMail}
            onLoadMoreOrders={loadMoreOrders}
            onOpenMailbox={handleOpenMailbox}
            onQuantityChange={(value) =>
              setQuantity(clampQuantity(value, selectedInventory))
            }
            onSearchChange={setOrderSearch}
            onSelectOrder={handleSelectOrder}
            orderSearch={orderSearch}
            orders={visibleOrders}
            productsById={productsById}
            projectsById={projectsById}
            quantity={quantity}
            maxQuantity={Math.min(selectedInventory, maxCreateOrderQuantity)}
            selectedOrder={selectedOrder}
            selectedProduct={selectedProduct}
            selectedProject={selectedProject}
            selectedProductInventory={selectedInventory}
            serviceMode={serviceMode}
          />
        </div>
      </div>

      <ApplyProjectModal
        mode="create"
        onCancel={() => setApplyOpen(false)}
        onSuccess={() => setApplyOpen(false)}
        visible={applyOpen}
      />

      <MailboxClientModal
        autoFetchEnabled={
          !mailClientOrder?.verificationCode &&
          Boolean(mailClientOrder && shouldAutoFetchOrderMail(mailClientOrder))
        }
        email={mailClientParams?.email}
        fetchKey={mailClientParams?.orderNo}
        messages={mailClientOrder?.messages ?? []}
        onClose={() => setMailClientParams(null)}
        onFetch={(source) => {
          if (!mailClientOrder) return;
          return handleFetchOrderMail(mailClientOrder, source);
        }}
        onLoadMessage={async (messageId) => {
          if (!mailClientParams) return "";
          const detail = await readPickupMessage(
            mailClientParams.email,
            mailClientParams.token,
            Number(messageId)
          );
          return detail.body;
        }}
      />
    </>
  );
}
