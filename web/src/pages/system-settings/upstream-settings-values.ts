import { listProjects, type ProjectItem, type ProjectListResponse } from "@/lib/projects-api";

type ProjectPageLoader = (offset: number, limit: number) => Promise<ProjectListResponse>;

export async function loadAllProjects(
  fetchPage: ProjectPageLoader = (offset, limit) =>
    listProjects({ scope: "all" }, offset, limit),
): Promise<ProjectItem[]> {
  const items: ProjectItem[] = [];
  const limit = 100;
  while (true) {
    const page = await fetchPage(items.length, limit);
    items.push(...page.items);
    if (items.length >= page.total || page.items.length === 0) return items;
  }
}
