import { useEffect, useMemo, useState } from "react";
import { Toast } from "@douyinfe/semi-ui";
import { useTranslation } from "react-i18next";

import { IamApiError } from "@/lib/api-client";
import {
  createOrder,
  getOrder,
  listOrders,
  type OrderResponse,
} from "@/lib/orders-api";
import {
  listProjects,
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
        ? [product.label, product.suffix, product.productType]
            .join(" ")
            .toLowerCase()
            .includes(q)
        : true
    );
}

function toWorkbenchProject(project: ProjectItem): WorkbenchProject {
  return {
    description: project.description ?? "",
    id: String(project.id),
    logoUrl: project.logoUrl,
    name: project.name,
    products: (project.products ?? []).map((product) =>
      toWorkbenchProduct(project.id, product, product.totalAvailable ?? 0)
    ),
    projectUrl: project.targetPlatform,
    visibility: project.accessType,
  };
}

function toWorkbenchProduct(
  projectId: number,
  product: ProjectProductSummary,
  inventory: number
): WorkbenchProduct {
  const label = product.type === "microsoft" ? "Microsoft" : "Domain";
  return {
    activationWindowMinutes: product.activationWindowMinutes,
    codeEnabled: product.codeEnabled,
    codeInventory: inventory,
    codePrice: moneyToNumber(product.codePrice),
    codeWindowMinutes: product.codeWindowMinutes,
    id: String(product.id),
    label,
    productType: product.type,
    projectId: String(projectId),
    purchaseEnabled: product.purchaseEnabled,
    purchaseInventory: inventory,
    purchasePrice: moneyToNumber(product.purchasePrice),
    suffix: label,
    warrantyHours: Math.max(1, Math.ceil(product.warrantyMinutes / 60)),
  };
}

function toWorkbenchOrder(order: OrderResponse): WorkbenchOrder {
  return {
    activationUntil:
      order.serviceMode === "purchase"
        ? order.receiveUntil ?? order.afterSaleUntil ?? undefined
        : undefined,
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

function orderServiceState(order: OrderResponse): ServiceState {
  if (order.status === "completed") {
    return order.serviceMode === "code" ? "code_received" : "read_expired";
  }
  if (order.status === "active") {
    return order.serviceMode === "purchase" ? "in_warranty" : "waiting_mail";
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

function nextIdempotencyKey() {
  if (typeof crypto !== "undefined" && "randomUUID" in crypto) {
    return crypto.randomUUID();
  }
  return `ui_${Date.now()}_${Math.random().toString(36).slice(2)}`;
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
          ? { ...detail, messages: item.messages, lastFetchedAt: item.lastFetchedAt }
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
    setCreating(true);
    try {
      const order = await createOrder(
        {
          projectId: Number(selectedProject.id),
          productId: Number(selectedProduct.id),
        },
        {
          idempotencyKey: nextIdempotencyKey(),
          serviceMode,
          supply: inventoryScope,
        }
      );
      const nextOrder = toWorkbenchOrder(order);
      setOrders((prev) => [
        nextOrder,
        ...prev.filter((item) => item.orderNo !== nextOrder.orderNo),
      ]);
      setSelectedOrderNo(nextOrder.orderNo);
      Toast.success(t("Order created."));
      void loadWorkbenchProjects();
    } catch (err) {
      Toast.error(apiErrorMessage(err, t("An unexpected error occurred.")));
    } finally {
      setCreating(false);
    }
  }

  function handleFetchOrderMail(_order: WorkbenchOrder, _source: FetchSource) {
    Toast.info(t("Feature is not implemented yet."));
  }

  function handleSelectOrder(orderNo: string) {
    setSelectedOrderNo((current) => {
      if (current === orderNo) return "";
      const order = orders.find((item) => item.orderNo === orderNo);
      if (order && !order.token) {
        void loadOrderDetail(orderNo).catch((err) =>
          Toast.error(apiErrorMessage(err, t("An unexpected error occurred.")))
        );
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
      return;
    }
    void loadOrderDetail(params.orderNo)
      .then((order) => {
        setMailClientParams({
          email: order.deliveryEmail,
          orderNo: order.orderNo,
          token: order.token,
        });
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
            onSearchChange={setOrderSearch}
            onSelectOrder={handleSelectOrder}
            orderSearch={orderSearch}
            orders={visibleOrders}
            productsById={productsById}
            projectsById={projectsById}
            selectedOrder={selectedOrder}
            selectedProduct={selectedProduct}
            selectedProject={selectedProject}
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
