import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { tablePageSizeOptions } from "./pagination";

export type SectionKey =
  | "overview"
  | "workbench"
  | "tasks"
  | "schedules"
  | "gallery"
  | "settings";

/** 任务中心的状态筛选（客户端过滤当前页，进 URL 只为刷新/分享还原）。 */
export type StatusFilterType = "all" | "active" | "completed" | "failed";

export const statusFilterValues: StatusFilterType[] = ["all", "active", "completed", "failed"];

/** 媒体库筛选（服务端分页，全部进 URL）。 */
export interface GalleryRouteState {
  kind: string;
  user: string;
  search: string;
  page: number;
}

export const galleryKindValues = ["all", "images", "videos", "gifs"];
export const galleryPageSize = 60;

export const defaultGalleryFilter: GalleryRouteState = {
  kind: "all",
  user: "all",
  search: "",
  page: 1,
};

export type RouteState = {
  section: SectionKey;
  jobPage: number;
  jobPageSize: number;
  jobStatus: StatusFilterType;
  gallery: GalleryRouteState;
  shouldReplace: boolean;
};

export const defaultJobPage = 1;
export const defaultJobPageSize = 10;
export const defaultJobStatus: StatusFilterType = "all";

export const sectionRoutes: Record<SectionKey, string> = {
  overview: "/overview",
  workbench: "/workbench",
  tasks: "/tasks",
  schedules: "/schedules",
  gallery: "/gallery",
  settings: "/settings",
};

export const routeSections: Record<string, SectionKey> = {
  "/": "overview",
  "/overview": "overview",
  "/workbench": "overview",
  "/jobs": "tasks",
  "/tasks": "tasks",
  "/schedules": "schedules",
  "/gallery": "gallery",
  "/settings": "settings",
};

/** 提交给 buildRoutePath / updateBrowserRoute 的完整路由状态（未给出的字段按默认值处理）。 */
export interface RouteQueryState {
  section: SectionKey;
  jobPage?: number;
  jobPageSize?: number;
  jobStatus?: StatusFilterType;
  gallery?: Partial<GalleryRouteState>;
}

export function normalizePathname(pathname: string) {
  const normalized = pathname.replace(/\/+$/, "");
  return normalized === "" ? "/" : normalized;
}

export function parsePositiveInteger(value: string | null, fallback: number) {
  const parsed = Number(value);
  return Number.isInteger(parsed) && parsed > 0 ? parsed : fallback;
}

export function parseJobPageSize(value: string | null) {
  const parsed = parsePositiveInteger(value, defaultJobPageSize);
  return tablePageSizeOptions.includes(parsed) ? parsed : defaultJobPageSize;
}

export function parseStatusFilter(value: string | null): StatusFilterType {
  return statusFilterValues.includes(value as StatusFilterType)
    ? (value as StatusFilterType)
    : defaultJobStatus;
}

export function parseGalleryKind(value: string | null) {
  return galleryKindValues.includes(value ?? "") ? (value as string) : "all";
}

/** 搜索词只保留可打印内容并限制长度，避免超长 query 被写进历史记录。 */
export function parseSearchKeyword(value: string | null) {
  if (!value) return "";
  return value.slice(0, 120);
}

export function isTaskCenterSection(section: SectionKey) {
  return section === "overview" || section === "workbench" || section === "tasks";
}

export function buildRoutePath(state: RouteQueryState) {
  const { section } = state;
  const params = new URLSearchParams();

  if (isTaskCenterSection(section)) {
    const jobPage = state.jobPage ?? defaultJobPage;
    const jobPageSize = state.jobPageSize ?? defaultJobPageSize;
    if (jobPage > defaultJobPage) params.set("page", String(jobPage));
    if (jobPageSize !== defaultJobPageSize) params.set("pageSize", String(jobPageSize));
    const status = state.jobStatus ?? defaultJobStatus;
    if (status !== defaultJobStatus) params.set("status", status);
  } else if (section === "gallery") {
    const gallery = { ...defaultGalleryFilter, ...state.gallery };
    if (gallery.page > 1) params.set("page", String(gallery.page));
    if (gallery.kind !== "all") params.set("kind", gallery.kind);
    if (gallery.user && gallery.user !== "all") params.set("user", gallery.user);
    if (gallery.search) params.set("q", gallery.search);
  }

  const query = params.toString();
  const route = sectionRoutes[section] ?? `/${section}`;
  return `${route}${query ? `?${query}` : ""}`;
}

export function readRouteState(): RouteState {
  if (typeof window === "undefined") {
    return {
      section: "overview",
      jobPage: defaultJobPage,
      jobPageSize: defaultJobPageSize,
      jobStatus: defaultJobStatus,
      gallery: { ...defaultGalleryFilter },
      shouldReplace: false,
    };
  }

  const pathname = normalizePathname(window.location.pathname);
  const section = routeSections[pathname] ?? "overview";
  const params = new URLSearchParams(window.location.search);
  const carriesPagination = isTaskCenterSection(section);
  const jobPage = carriesPagination
    ? parsePositiveInteger(params.get("page"), defaultJobPage)
    : defaultJobPage;
  const jobPageSize = carriesPagination
    ? parseJobPageSize(params.get("pageSize"))
    : defaultJobPageSize;
  const jobStatus = carriesPagination ? parseStatusFilter(params.get("status")) : defaultJobStatus;
  const gallery: GalleryRouteState =
    section === "gallery"
      ? {
          kind: parseGalleryKind(params.get("kind")),
          user: params.get("user") || "all",
          search: parseSearchKeyword(params.get("q")),
          page: parsePositiveInteger(params.get("page"), 1),
        }
      : { ...defaultGalleryFilter };

  const canonicalRoute = buildRoutePath({ section, jobPage, jobPageSize, jobStatus, gallery });
  const currentRoute = `${window.location.pathname}${window.location.search}`;

  return {
    section,
    jobPage,
    jobPageSize,
    jobStatus,
    gallery,
    shouldReplace: currentRoute !== canonicalRoute,
  };
}

export function updateBrowserRoute(state: RouteQueryState, replace = false) {
  if (typeof window === "undefined") return;

  const nextRoute = buildRoutePath(state);
  const currentRoute = `${window.location.pathname}${window.location.search}`;
  if (currentRoute === nextRoute) return;

  if (replace) {
    window.history.replaceState(null, "", nextRoute);
    return;
  }
  window.history.pushState(null, "", nextRoute);
}

export function useRouteState() {
  const initialRoute = useMemo(() => readRouteState(), []);
  const [activeSection, setActiveSection] = useState<SectionKey>(initialRoute.section);
  const [jobPage, setJobPage] = useState(initialRoute.jobPage);
  const [jobPageSize, setJobPageSize] = useState(initialRoute.jobPageSize);
  const [jobStatus, setJobStatus] = useState<StatusFilterType>(initialRoute.jobStatus);
  const [gallery, setGallery] = useState<GalleryRouteState>(initialRoute.gallery);

  const stateRef = useRef({
    section: initialRoute.section,
    jobPage: initialRoute.jobPage,
    jobPageSize: initialRoute.jobPageSize,
    jobStatus: initialRoute.jobStatus,
    gallery: initialRoute.gallery,
  });
  stateRef.current = { section: activeSection, jobPage, jobPageSize, jobStatus, gallery };

  const pushRoute = useCallback(
    (
      patch: {
        section?: SectionKey;
        jobPage?: number;
        jobPageSize?: number;
        jobStatus?: StatusFilterType;
        gallery?: GalleryRouteState;
      },
      replace = false,
    ) => {
      const next = { ...stateRef.current, ...patch };
      stateRef.current = next;
      updateBrowserRoute(next, replace);
    },
    [],
  );

  useEffect(() => {
    if (initialRoute.shouldReplace) {
      updateBrowserRoute(
        {
          section: initialRoute.section,
          jobPage: initialRoute.jobPage,
          jobPageSize: initialRoute.jobPageSize,
          jobStatus: initialRoute.jobStatus,
          gallery: initialRoute.gallery,
        },
        true,
      );
    }
  }, [initialRoute]);

  useEffect(() => {
    function handlePopState() {
      const route = readRouteState();
      setActiveSection(route.section);
      setJobPage(route.jobPage);
      setJobPageSize(route.jobPageSize);
      setJobStatus(route.jobStatus);
      setGallery(route.gallery);
    }

    window.addEventListener("popstate", handlePopState);
    return () => window.removeEventListener("popstate", handlePopState);
  }, []);

  const handleSectionChange = useCallback(
    (section: SectionKey) => {
      setActiveSection(section);
      pushRoute({ section });
    },
    [pushRoute],
  );

  const handleJobPageChange = useCallback(
    (page: number) => {
      setJobPage(page);
      if (isTaskCenterSection(stateRef.current.section)) {
        pushRoute({ jobPage: page });
      }
    },
    [pushRoute],
  );

  const handleJobPageSizeChange = useCallback(
    (pageSize: number) => {
      setJobPageSize(pageSize);
      setJobPage(defaultJobPage);
      if (isTaskCenterSection(stateRef.current.section)) {
        pushRoute({ jobPage: defaultJobPage, jobPageSize: pageSize });
      }
    },
    [pushRoute],
  );

  const handleJobStatusChange = useCallback(
    (status: StatusFilterType) => {
      setJobStatus(status);
      setJobPage(defaultJobPage);
      if (isTaskCenterSection(stateRef.current.section)) {
        pushRoute({ jobStatus: status, jobPage: defaultJobPage });
      }
    },
    [pushRoute],
  );

  /**
   * 媒体库筛选/页码统一入口：一次更新同时改 state 与 URL，避免"新筛选 + 旧页码"多发一次请求。
   * 搜索框逐字符输入用 replace 模式，避免把每个字符都压进浏览器历史。
   */
  const handleGalleryChange = useCallback(
    (patch: Partial<GalleryRouteState>, options?: { replace?: boolean }) => {
      // 先算结果再 setState：history 写入是副作用，不能放进 updater（StrictMode 会双调用）。
      const next = { ...stateRef.current.gallery, ...patch };
      const nextState = { ...stateRef.current, gallery: next };
      stateRef.current = nextState;
      setGallery(next);
      if (nextState.section === "gallery") {
        updateBrowserRoute(nextState, options?.replace ?? false);
      }
    },
    [],
  );

  const syncServerPage = useCallback(
    (serverPage?: number) => {
      if (serverPage && serverPage !== stateRef.current.jobPage) {
        setJobPage(serverPage);
        if (isTaskCenterSection(stateRef.current.section)) {
          pushRoute({ jobPage: serverPage }, true);
        }
      }
    },
    [pushRoute],
  );

  return {
    activeSection,
    jobPage,
    jobPageSize,
    jobStatus,
    gallery,
    handleSectionChange,
    handleJobPageChange,
    handleJobPageSizeChange,
    handleJobStatusChange,
    handleGalleryChange,
    syncServerPage,
  };
}
