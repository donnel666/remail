import { Card, Empty, Input, Tag } from "@douyinfe/semi-ui";
import { IconSearch } from "@douyinfe/semi-icons";
import { useTranslation } from "react-i18next";

import { OverflowTooltip } from "@/components/semi/overflow-tooltip";
import { cn } from "@/lib/utils";

import { ProjectIcon } from "./project-icon";
import type { ServiceMode, WorkbenchProject } from "./types";

export function ProjectListPanel({
  onSearchChange,
  onSelectProject,
  projects,
  search,
  selectedProjectId,
  serviceMode,
}: {
  onSearchChange: (value: string) => void;
  onSelectProject: (projectId: string) => void;
  projects: WorkbenchProject[];
  search: string;
  selectedProjectId: string;
  serviceMode: ServiceMode;
}) {
  const { t } = useTranslation();

  return (
    <Card className="workbench-column workbench-project-panel" shadows="hover">
      <div className="workbench-column-header">
        <div>
          <div className="workbench-column-title">{t("Projects")}</div>
          <div className="workbench-column-subtitle">{t("Select project to order")}</div>
        </div>
        <Tag color="orange" shape="circle">
          {projects.length}
        </Tag>
      </div>

      <Input
        className="resources-search-input workbench-panel-search"
        onChange={(value) => onSearchChange(String(value))}
        placeholder={t("Search project")}
        prefix={<IconSearch />}
        showClear
        value={search}
      />

      <div className="workbench-project-list">
        {projects.length === 0 ? (
          <Empty description={t("No projects")} />
        ) : (
          projects.map((project) => {
            const selected = selectedProjectId === project.id;
            const inventory = project.products.reduce((sum, product) => {
              return (
                sum +
                (serviceMode === "code"
                  ? product.codeInventory
                  : product.purchaseInventory)
              );
            }, 0);

            return (
              <button
                className={cn("workbench-project-row", selected && "is-selected")}
                key={project.id}
                onClick={() => onSelectProject(project.id)}
                type="button"
              >
                <ProjectIcon name={project.name} logoUrl={project.logoUrl} />
                <span className="workbench-project-row-main">
                  <OverflowTooltip
                    className="workbench-project-row-name"
                    content={project.name}
                  >
                    {project.name}
                  </OverflowTooltip>
                  <OverflowTooltip
                    className="workbench-project-row-desc"
                    content={project.description}
                  >
                    {project.description}
                  </OverflowTooltip>
                </span>
                <span className="workbench-project-row-side">
                  <Tag
                    color={project.visibility === "private" ? "amber" : "grey"}
                    shape="circle"
                    size="small"
                  >
                    {project.visibility === "private" ? t("Private") : t("Public")}
                  </Tag>
                  <span className="font-mono-data text-[12px] text-[var(--semi-color-text-2)]">
                    {inventory}
                  </span>
                </span>
              </button>
            );
          })
        )}
      </div>
    </Card>
  );
}
