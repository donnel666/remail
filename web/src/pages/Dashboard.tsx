import { useEffect, useMemo, useState } from "react";
import { Toast } from "@douyinfe/semi-ui";
import { useTranslation } from "react-i18next";

import { IamApiError } from "@/lib/api-client";
import { generateIdempotencyKey } from "@/lib/idempotency";
import {
  createOrder,
  getOrder,
  listOrders,
  type OrderResponse,
} from "@/lib/orders-api";
import {
  readPickupMail,
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
          .includes(q)
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
  serviceMode: ServiceMode
) {
  const q = search.trim().toLowerCase();
  return products
    .filter((product) =>
      serviceMode === "code" ? product.codeEnabled : product.purchaseEnabled
    )
    .filter((product) =>
      q
        ? [product.label, product.suffix, product.emailSuffix, product.productType]
            .join(" ")
            .toLowerCase()
            .includes(q)
        : true
    );
}

type ProductInventoryTotal = ProjectInventoryTotalResponse["products"][number];

const checkoutBatchStorageKey = "remail.workbench.checkoutBatch";
const checkoutBatchConcurrency = 5;

interface CheckoutBatchState {
  batchId: string;
  quantity: number;
  signature: string;
  succeededIndexes: number[];
}

function toWorkbenchProject(
  project: ProjectItem,
  inventory?: ProjectInventoryTotalResponse
): WorkbenchProject {
  const inventoryByProductId = new Map(
    (inventory?.products ?? []).map((item) => [String(item.productId), item])
  );
  return {
    description: project.description ?? "",
    id: String(project.id),
    inventoryLoaded: Boolean(inventory),
    logoUrl: project.logoUrl,
    name: project.name,
    products: (project.products ?? []).flatMap((product) =>
      toWorkbenchProducts(project.id, product, inventoryByProductId.get(String(product.id)))
    ),
    projectUrl: project.targetPlatform,
    visibility: project.accessType,
  };
}

function toWorkbenchProducts(
  projectId: number,
  product: ProjectProductSummary,
  inventory?: ProductInventoryTotal
): WorkbenchProduct[] {
  const label = product.type === "microsoft" ? "Microsoft" : "Domain";
  const totalAvailable = inventory?.totalAvailable ?? product.totalAvailable ?? 0;
  const publicAvailable = inventory?.publicAvailable ?? product.publicAvailable ?? 0;
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
  inventory: ProjectInventoryTotalResponse
): WorkbenchProject {
  const inventoryByProductId = new Map(
    (inventory.products ?? []).map((item) => [String(item.productId), item])
  );
  const baseProducts = project.products.filter(
    (product) => product.id === product.productId
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
        ? order.receiveUntil ?? order.afterSaleUntil ?? undefined
        : undefined,
    activatedAt: order.activatedAt ?? undefined,
    afterSaleUntil: order.afterSaleUntil ?? order.receiveUntil ?? order.updatedAt,
    createdAt: order.createdAt,
    deliveryEmail: order.deliveryEmail,
    id: String(order.id),
    inventoryScope: order.supplyPolicy,
    lastFetchedAt: order.updatedAt,
    messages: [],
    orderNo: order.orderNo,
    payAmount: moneyToNumber(order.payAmount),
    productId: String(order.projectProductId),
    projectId: String(order.projectId),
    quantity: 1,
    receiveUntil: order.serviceMode === "code" ? order.receiveUntil ?? undefined : undefined,
    serviceMode: order.serviceMode,
    serviceState: orderServiceState(order),
    status: order.status,
    token: order.serviceToken ?? "",
  };
}

function toWorkbenchMessages(items: OrderMailResponse["items"]): WorkbenchMessage[] {
  return items.map((item, index) => {
    const body = item.body ?? "";
    const preview = body.replace(/\s+/g, " ").trim().slice(0, 180);
    return {
      body,
      id: `${item.receivedAt}-${index}-${item.sender}-${item.subject}`,
      preview,
      receivedAt: item.receivedAt,
      sender: item.sender,
      status: item.verificationCode ? "matched" : "received",
      subject: item.subject || "(No subject)",
      verificationCode: item.verificationCode,
    };
  });
}

function messageTimestamp(value: string) {
  const time = Date.parse(value);
  return Number.isFinite(time) ? time : 0;
}

function latestVerificationCode(messages: WorkbenchMessage[]) {
  return [...messages]
    .filter((item) => item.verificationCode)
    .sort((a, b) => messageTimestamp(b.receivedAt) - messageTimestamp(a.receivedAt))[0]
    ?.verificationCode;
}

function orderServiceState(order: OrderResponse): ServiceState {
  if (order.status === "completed") {
    return order.serviceMode === "code" ? "code_received" : "read_expired";
  }
  if (order.status === "active") {
    if (order.serviceMode === "purchase") {
      return order.activatedAt ? "in_warranty" : "pending_activation";
    }
    return "waiting_mail";
  }
  if (order.status === "failed" || order.status === "refunded") {
    return "activation_timeout";
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
  inventoryScope: InventoryScope
) {
  if (!product) return 0;
  if (inventoryScope === "public_only") return product.publicInventory;
  return serviceMode === "code" ? product.codeInventory : product.purchaseInventory;
}

function clampQuantity(value: number, inventory: number) {
  if (inventory <= 0) return 0;
  if (!Number.isFinite(value)) return 1;
  return Math.max(1, Math.min(Math.trunc(value), inventory));
}

function nextIdempotencyKey() {
  return generateIdempotencyKey();
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

function sanitizeSucceededIndexes(value: unknown, quantity: number) {
  if (!Array.isArray(value)) return [];
  return Array.from(
    new Set(
      value.filter(
        (item): item is number =>
          Number.isInteger(item) && item >= 0 && item < quantity
      )
    )
  ).sort((a, b) => a - b);
}

function loadCheckoutBatchState(
  signature: string,
  quantity: number
): CheckoutBatchState {
  const fresh = {
    batchId: nextIdempotencyKey(),
    quantity,
    signature,
    succeededIndexes: [],
  };
  try {
    const raw = globalThis.sessionStorage?.getItem(checkoutBatchStorageKey);
    if (!raw) return fresh;
    const parsed = JSON.parse(raw) as Partial<CheckoutBatchState>;
    if (
      parsed.signature !== signature ||
      parsed.quantity !== quantity ||
      typeof parsed.batchId !== "string" ||
      parsed.batchId.trim() === ""
    ) {
      return fresh;
    }
    return {
      batchId: parsed.batchId,
      quantity,
      signature,
      succeededIndexes: sanitizeSucceededIndexes(parsed.succeededIndexes, quantity),
    };
  } catch {
    return fresh;
  }
}

function saveCheckoutBatchState(state: CheckoutBatchState) {
  try {
    globalThis.sessionStorage?.setItem(
      checkoutBatchStorageKey,
      JSON.stringify(state)
    );
  } catch {
    // The batch is still protected by per-request idempotency keys in memory.
  }
}

function clearCheckoutBatchState(batchId: string) {
  try {
    const raw = globalThis.sessionStorage?.getItem(checkoutBatchStorageKey);
    if (!raw) return;
    const parsed = JSON.parse(raw) as Partial<CheckoutBatchState>;
    if (parsed.batchId === batchId) {
      globalThis.sessionStorage?.removeItem(checkoutBatchStorageKey);
    }
  } catch {
    globalThis.sessionStorage?.removeItem(checkoutBatchStorageKey);
  }
}

function apiErrorMessage(err: unknown, fallback: string) {
  if (err instanceof IamApiError && err.message) return err.message;
  if (err instanceof Error && err.message) return err.message;
  return fallback;
}

export default function Dashboard() {
  const { t } = useTranslation();
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
  const [productSearch, setProductSearch] = useState("");
  const [projectSearch, setProjectSearch] = useState("");
  const [projects, setProjects] = useState<WorkbenchProject[]>([]);
  const [quantity, setQuantity] = useState(1);
  const [selectedOrderNo, setSelectedOrderNo] = useState("");
  const [selectedProductId, setSelectedProductId] = useState("");
  const [selectedProjectId, setSelectedProjectId] = useState("");
  const [serviceMode, setServiceMode] = useState<ServiceMode>("purchase");

  const projectsById = useMemo(() => {
    return new Map(projects.map((project) => [project.id, project]));
  }, [projects]);

  const productsById = useMemo(() => {
    return new Map(
      projects.flatMap((project) =>
        project.products.map((product) => [product.id, product] as const)
      )
    );
  }, [projects]);

  const filteredProjects = useMemo(
    () => filterProjects(projects, projectSearch),
    [projects, projectSearch]
  );

  const selectedProject = projectsById.get(selectedProjectId) ?? filteredProjects[0];

  const filteredProducts = useMemo(
    () =>
      filterProducts(selectedProject?.products ?? [], productSearch, serviceMode),
    [productSearch, selectedProject?.products, serviceMode]
  );

  useEffect(() => {
    void loadWorkbenchProjects();
    void refreshOrders();
  }, []);

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
    if (selectedProject.products.some((product) => product.id === selectedProductId)) {
      return;
    }
    setSelectedProductId(
      filterProducts(selectedProject.products, productSearch, serviceMode)[0]?.id ?? ""
    );
  }, [productSearch, selectedProject, selectedProductId, serviceMode]);

  useEffect(() => {
    if (filteredProducts.some((product) => product.id === selectedProductId)) {
      return;
    }
    setSelectedProductId(filteredProducts[0]?.id ?? selectedProject?.products[0]?.id ?? "");
  }, [filteredProducts, selectedProductId, selectedProject?.products]);

  const selectedProduct =
    productsById.get(selectedProductId) ??
    filteredProducts[0] ??
    selectedProject?.products[0];
  const selectedInventory = getProductInventory(
    selectedProduct,
    serviceMode,
    inventoryScope
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
          projectsById.get(order.projectId)?.name
        )
      );
  }, [orderSearch, orders, projectsById, serviceMode]);

  useEffect(() => {
    if (!selectedOrderNo) return;
    if (visibleOrders.some((order) => order.orderNo === selectedOrderNo)) return;
    setSelectedOrderNo("");
  }, [selectedOrderNo, visibleOrders]);

  const selectedOrder = visibleOrders.find(
    (order) => order.orderNo === selectedOrderNo
  );
  const mailClientOrder = mailClientParams
    ? orders.find(
        (order) =>
          order.orderNo === mailClientParams.orderNo &&
          order.deliveryEmail === mailClientParams.email &&
          order.token === mailClientParams.token
      )
    : undefined;

  async function loadWorkbenchProjects() {
    try {
      const list = await listProjects({ scope: "visible", status: "listed" }, 0, 100);
      const listed = list.items.filter((project) => project.status === "listed");
      setProjects(listed.map((project) => toWorkbenchProject(project)));
    } catch (err) {
      Toast.error(apiErrorMessage(err, t("An unexpected error occurred.")));
    }
  }

  async function loadProjectInventory(projectId: string) {
    const numericProjectId = Number(projectId);
    if (!Number.isInteger(numericProjectId) || numericProjectId <= 0) return;
    try {
      const inventory = await getProjectInventory(numericProjectId);
      setProjects((prev) =>
        prev.map((project) =>
          project.id === projectId ? mergeProjectInventory(project, inventory) : project
        )
      );
    } catch (err) {
      Toast.error(apiErrorMessage(err, t("An unexpected error occurred.")));
    }
  }

  async function refreshOrders() {
    try {
      const list = await listOrders({ limit: 100 });
      setOrders(list.items.map(toWorkbenchOrder));
    } catch (err) {
      Toast.error(apiErrorMessage(err, t("An unexpected error occurred.")));
    }
  }

  async function loadOrderDetail(orderNo: string) {
    const detail = toWorkbenchOrder(await getOrder(orderNo));
    setOrders((prev) =>
      prev.map((item) =>
        item.orderNo === orderNo
          ? {
              ...detail,
              messages: item.messages,
              lastFetchedAt: item.lastFetchedAt,
              verificationCode: item.verificationCode,
            }
          : item
      )
    );
    return detail;
  }

  function handleSelectProject(projectId: string) {
    const project = projectsById.get(projectId);
    setSelectedProjectId(projectId);
    setProductSearch("");
    setSelectedProductId(
      filterProducts(project?.products ?? [], "", serviceMode)[0]?.id ?? ""
    );
  }

  async function handleCreateOrder() {
    if (!selectedProject || !selectedProduct || creating) return;
    const requestedQuantity = clampQuantity(quantity, selectedInventory);
    if (requestedQuantity <= 0) return;
    setCreating(true);
    const createdOrders: OrderResponse[] = [];
    const signature = checkoutBatchSignature({
      inventoryScope,
      productId: selectedProduct.id,
      projectId: selectedProject.id,
      quantity: requestedQuantity,
      serviceMode,
      suffix: selectedProduct.emailSuffix,
    });
    const batch = loadCheckoutBatchState(signature, requestedQuantity);
    const succeededIndexes = new Set(batch.succeededIndexes);
    saveCheckoutBatchState(batch);
    let createdOrdersMerged = false;
    const mergeCreatedOrders = () => {
      if (createdOrdersMerged || createdOrders.length === 0) return;
      createdOrdersMerged = true;
      const nextOrders = createdOrders.map(toWorkbenchOrder);
      const nextOrderNos = new Set(nextOrders.map((order) => order.orderNo));
      setOrders((prev) => [
        ...nextOrders,
        ...prev.filter((item) => !nextOrderNos.has(item.orderNo)),
      ]);
      setSelectedOrderNo(nextOrders[0]?.orderNo ?? "");
    };
    try {
      const missingIndexes = Array.from(
        { length: requestedQuantity },
        (_, index) => index
      ).filter((index) => !succeededIndexes.has(index));
      const failures: unknown[] = [];
      for (
        let start = 0;
        start < missingIndexes.length && failures.length === 0;
        start += checkoutBatchConcurrency
      ) {
        const chunk = missingIndexes.slice(start, start + checkoutBatchConcurrency);
        const settled = await Promise.allSettled(
          chunk.map(async (index) => ({
            index,
            order: await createOrder(
              {
                emailSuffix: selectedProduct.emailSuffix || undefined,
                projectId: Number(selectedProject.id),
                productId: Number(selectedProduct.productId),
              },
              {
                idempotencyKey: `${batch.batchId}:${index}`,
                serviceMode,
                supply: inventoryScope,
              }
            ),
          }))
        );
        for (const item of settled) {
          if (item.status === "fulfilled") {
            createdOrders.push(item.value.order);
            succeededIndexes.add(item.value.index);
            batch.succeededIndexes = [...succeededIndexes].sort((a, b) => a - b);
            saveCheckoutBatchState(batch);
          } else {
            failures.push(item.reason);
          }
        }
      }
      if (succeededIndexes.size >= requestedQuantity) {
        clearCheckoutBatchState(batch.batchId);
        mergeCreatedOrders();
        Toast.success(t("Order created."));
        void refreshOrders();
        void loadProjectInventory(selectedProject.id);
        return;
      }
      mergeCreatedOrders();
      void refreshOrders();
      void loadProjectInventory(selectedProject.id);
      const firstFailure = failures[0];
      Toast.error(
        `${apiErrorMessage(firstFailure, t("An unexpected error occurred."))} (${succeededIndexes.size}/${requestedQuantity})`
      );
      return;
    } catch (err) {
      if (createdOrders.length > 0) {
        mergeCreatedOrders();
        void refreshOrders();
        void loadProjectInventory(selectedProject.id);
      }
      Toast.error(apiErrorMessage(err, t("An unexpected error occurred.")));
    } finally {
      setCreating(false);
    }
  }

  async function handleFetchOrderMail(order: WorkbenchOrder, _source: FetchSource) {
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
      const messages = toWorkbenchMessages(result.items);
      const latestCode = latestVerificationCode(messages);
      const lastFetchedAt =
        result.fetch?.lastReceivedAt ??
        result.fetch?.lastSuccessAt ??
        result.fetch?.lastSubmittedAt ??
        new Date().toISOString();
      let refreshedDetail: WorkbenchOrder | undefined;
      if (latestCode) {
        try {
          refreshedDetail = toWorkbenchOrder(await getOrder(target.orderNo));
        } catch {
          refreshedDetail = undefined;
        }
      }
      setOrders((prev) =>
        prev.map((item) =>
          item.orderNo === target.orderNo
            ? {
                ...(refreshedDetail ?? item),
                messages,
                lastFetchedAt,
                verificationCode: latestCode ?? item.verificationCode,
                serviceState: latestCode
                  ? target.serviceMode === "code"
                    ? "code_received"
                    : "in_warranty"
                  : (refreshedDetail?.serviceState ?? item.serviceState),
              }
            : item
        )
      );
    } catch (err) {
      Toast.error(apiErrorMessage(err, t("An unexpected error occurred.")));
    }
  }

  function handleSelectOrder(orderNo: string) {
    setSelectedOrderNo((current) => {
      if (current === orderNo) return "";
      const order = orders.find((item) => item.orderNo === orderNo);
      if (order && !order.token) {
        void loadOrderDetail(orderNo)
          .then((detail) => {
            if (!detail.verificationCode) {
              void handleFetchOrderMail(detail, "auto");
            }
          })
          .catch((err) =>
            Toast.error(apiErrorMessage(err, t("An unexpected error occurred.")))
          );
      } else if (order && !order.verificationCode) {
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
      if (order) {
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
        void handleFetchOrderMail(order, "auto");
      })
      .catch((err) =>
        Toast.error(apiErrorMessage(err, t("An unexpected error occurred.")))
      );
  }

  return (
    <>
      <div className="workbench-responsive-shell">
        <div className="workbench-layout">
          <ProjectListPanel
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
            inventoryScope={inventoryScope}
            onCreateOrder={handleCreateOrder}
            onFetchOrderMail={handleFetchOrderMail}
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
            selectedOrder={selectedOrder}
            selectedProduct={selectedProduct}
            selectedProject={selectedProject}
            selectedProductInventory={selectedInventory}
            serviceMode={serviceMode}
          />
        </div>
      </div>

      <MailboxClientModal
        email={mailClientParams?.email}
        messages={mailClientOrder?.messages ?? []}
        onClose={() => setMailClientParams(null)}
        onFetch={(source) => {
          if (!mailClientOrder) return;
          handleFetchOrderMail(mailClientOrder, source);
        }}
      />
    </>
  );
}
