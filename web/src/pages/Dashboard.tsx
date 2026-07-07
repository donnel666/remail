import { useEffect, useMemo, useState } from "react";
import { Toast } from "@douyinfe/semi-ui";
import { useTranslation } from "react-i18next";

import { MailboxClientModal } from "./workbench/mailbox-client";
import { createMockOrder, mockOrders, mockProjects } from "./workbench/mock-data";
import { OrderPanel } from "./workbench/order-panel";
import { ProductPickerPanel } from "./workbench/product-picker-panel";
import { ProjectListPanel } from "./workbench/project-list-panel";
import type {
  FetchSource,
  InventoryScope,
  ServiceMode,
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

function filterProducts(products: WorkbenchProduct[], search: string) {
  const q = search.trim().toLowerCase();
  if (!q) return products;
  return products.filter((product) =>
    [product.label, product.suffix, product.productType]
      .join(" ")
      .toLowerCase()
      .includes(q)
  );
}

export default function Dashboard() {
  const { t } = useTranslation();
  const [inventoryScope, setInventoryScope] =
    useState<InventoryScope>("private_first");
  const [mailClientParams, setMailClientParams] = useState<{
    email: string;
    orderNo: string;
    token: string;
  } | null>(null);
  const [orderSearch, setOrderSearch] = useState("");
  const [orders, setOrders] = useState<WorkbenchOrder[]>(mockOrders);
  const [productSearch, setProductSearch] = useState("");
  const [projectSearch, setProjectSearch] = useState("");
  const [quantity, setQuantity] = useState(1);
  const [selectedOrderNo, setSelectedOrderNo] = useState("");
  const [selectedProductId, setSelectedProductId] = useState(
    mockProjects[0]?.products[0]?.id ?? ""
  );
  const [selectedProjectId, setSelectedProjectId] = useState(mockProjects[0]?.id ?? "");
  const [serviceMode, setServiceMode] = useState<ServiceMode>("purchase");

  const projectsById = useMemo(() => {
    return new Map(mockProjects.map((project) => [project.id, project]));
  }, []);

  const productsById = useMemo(() => {
    return new Map(
      mockProjects.flatMap((project) =>
        project.products.map((product) => [product.id, product] as const)
      )
    );
  }, []);

  const filteredProjects = useMemo(
    () => filterProjects(mockProjects, projectSearch),
    [projectSearch]
  );

  const selectedProject = projectsById.get(selectedProjectId) ?? mockProjects[0];

  const filteredProducts = useMemo(
    () => filterProducts(selectedProject?.products ?? [], productSearch),
    [productSearch, selectedProject?.products]
  );

  useEffect(() => {
    if (!selectedProject) return;
    if (selectedProject.products.some((product) => product.id === selectedProductId)) {
      return;
    }
    setSelectedProductId(selectedProject.products[0]?.id ?? "");
  }, [selectedProductId, selectedProject]);

  useEffect(() => {
    if (filteredProducts.some((product) => product.id === selectedProductId)) {
      return;
    }
    setSelectedProductId(filteredProducts[0]?.id ?? selectedProject?.products[0]?.id ?? "");
  }, [filteredProducts, selectedProductId, selectedProject?.products]);

  const selectedProduct =
    productsById.get(selectedProductId) ??
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

  function handleSelectProject(projectId: string) {
    const project = projectsById.get(projectId);
    setSelectedProjectId(projectId);
    setProductSearch("");
    setSelectedProductId(project?.products[0]?.id ?? "");
  }

  function handleCreateOrder() {
    if (!selectedProject || !selectedProduct) return;
    const nextOrder = createMockOrder({
      inventoryScope,
      product: selectedProduct,
      project: selectedProject,
      quantity,
      serviceMode,
    });
    setOrders((prev) => [nextOrder, ...prev]);
    setSelectedOrderNo(nextOrder.orderNo);
    Toast.success(t("Order created."));
  }

  function handleFetchOrderMail(order: WorkbenchOrder, _source: FetchSource) {
    setOrders((prev) =>
      prev.map((item) =>
        item.orderNo === order.orderNo
          ? { ...item, lastFetchedAt: new Date().toISOString() }
          : item
      )
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
              setQuantity(1);
            }}
            productSearch={productSearch}
            products={filteredProducts}
            selectedProductId={selectedProductId}
            selectedProject={selectedProject}
            serviceMode={serviceMode}
          />

          <OrderPanel
            inventoryScope={inventoryScope}
            onCreateOrder={handleCreateOrder}
            onFetchOrderMail={handleFetchOrderMail}
            onOpenMailbox={setMailClientParams}
            onQuantityChange={setQuantity}
            onSearchChange={setOrderSearch}
            onSelectOrder={(orderNo) =>
              setSelectedOrderNo((current) => (current === orderNo ? "" : orderNo))
            }
            orderSearch={orderSearch}
            orders={visibleOrders}
            productsById={productsById}
            projectsById={projectsById}
            quantity={quantity}
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
