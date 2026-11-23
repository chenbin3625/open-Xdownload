import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { t } from "./i18n";
import { tablePageSizeOptions } from "./pagination";

export type SectionKey =
  | "overview"
  | "workbench"
  | "tasks"
  | "schedules"
  | "gallery"
  | "tweets"
  | "settings";

/** 任务中心的服务端筛选（全部进 URL，查询会返回全量计数）。 */
export type StatusFilterType = "all" | "active" | "completed" | "failed";

export const statusFilterValues: StatusFilterType[] = ["all", "active", "completed", "failed"];

/** 媒体库筛选（服务端分页，全部进 URL）。 */
export interface GalleryRouteState {
  kind: string;
  user: string;
  search: string;
  page: number;
  sort: GallerySort;
  from: string;
  to: string;
  minBytes: string;
  maxBytes: string;
}

export const gallerySortValues = ["date_desc", "date_asc", "size_desc", "size_asc"] as const;
export type GallerySort = typeof gallerySortValues[number];
export const galleryKindValues = ["all", "images", "videos", "gifs"];
export const galleryPageSize = 60;

export const defaultGalleryFilter: GalleryRouteState = {
  kind: "all",
  user: "all",
  search: "",
  page: 1,
  sort: "date_desc",
  from: "",
  to: "",
  minBytes: "",
  maxBytes: "",
};

export type RouteState = {
  section: SectionKey;
  jobPage: number;
  jobPageSize: number;
  jobStatus: StatusFilterType;
  jobKind: string;
  jobSearch: string;
  gallery: GalleryRouteState;
  shouldReplace: boolean;
};

export const defaultJobPage = 1;
export const defaultJobPageSize = 10;
export const defaultJobStatus: StatusFilterType = "all";
export const defaultJobKind = "all";
export const defaultJobSearch = "";
const jobKindValues = ["all", "tweet_link", "media_url", "user", "list", "following", "failed_retry", "asset_migration"];

export const sectionRoutes: Record<SectionKey, string> = {
  overview: "/overview",
  workbench: "/workbench",
  tasks: "/tasks",
  schedules: "/schedules",
  gallery: "/gallery",
  tweets: "/tweets",
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
  "/tweets": "tweets",
  "/settings": "settings",
};

/** 提交给 buildRoutePath / updateBrowserRoute 的完整路由状态（未给出的字段按默认值处理）。 */
export interface RouteQueryState {
  section: SectionKey;
  jobPage?: number;
  jobPageSize?: number;
  jobStatus?: StatusFilterType;
  jobKind?: string;
  jobSearch?: string;
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

export function parseJobKind(value: string | null) {
  return jobKindValues.includes(value ?? "") ? value! : defaultJobKind;
}

export function parseGalleryKind(value: string | null) {
  return galleryKindValues.includes(value ?? "") ? (value as string) : "all";
}

function parseGalleryDate(value: string | null) {
  if (!value || !/^\d{4}-\d{2}-\d{2}$/.test(value)) return "";
  const date = new Date(`${value}T00:00:00Z`);
  return Number.isFinite(date.getTime()) && date.toISOString().slice(0, 10) === value ? value : "";
}

function parseGalleryBytes(value: string | null) {
  return value && /^\d+$/.test(value) && Number.isSafeInteger(Number(value)) ? String(Number(value)) : "";
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
    const kind = state.jobKind ?? defaultJobKind;
    if (kind !== defaultJobKind) params.set("kind", kind);
    const search = state.jobSearch ?? defaultJobSearch;
    if (search) params.set("q", search);
  } else if (section === "gallery") {
    const gallery = { ...defaultGalleryFilter, ...state.gallery };
    if (gallery.page > 1) params.set("page", String(gallery.page));
    if (gallery.kind !== "all") params.set("kind", gallery.kind);
    if (gallery.user && gallery.user !== "all") params.set("user", gallery.user);
    if (gallery.search) params.set("q", gallery.search);
    if (gallery.sort !== defaultGalleryFilter.sort) params.set("sort", gallery.sort);
    for (const key of ["from", "to", "minBytes", "maxBytes"] as const) {
      if (gallery[key] !== "") params.set(key, gallery[key]);
    }
  } else if (section === "tweets" && typeof window !== "undefined" && normalizePathname(window.location.pathname) === "/tweets") {
    new URLSearchParams(window.location.search).forEach((value, key) => params.set(key, value));
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
      jobKind: defaultJobKind,
      jobSearch: defaultJobSearch,
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
  const jobKind = carriesPagination ? parseJobKind(params.get("kind")) : defaultJobKind;
  const jobSearch = carriesPagination ? parseSearchKeyword(params.get("q") ?? params.get("search")) : defaultJobSearch;
  const gallery: GalleryRouteState =
    section === "gallery"
      ? {
          kind: parseGalleryKind(params.get("kind")),
          user: params.get("user") || "all",
          search: parseSearchKeyword(params.get("q")),
          page: parsePositiveInteger(params.get("page"), 1),
          sort: gallerySortValues.includes(params.get("sort") as GallerySort) ? params.get("sort") as GallerySort : "date_desc",
          from: parseGalleryDate(params.get("from")),
          to: parseGalleryDate(params.get("to")),
          minBytes: parseGalleryBytes(params.get("minBytes")),
          maxBytes: parseGalleryBytes(params.get("maxBytes")),
        }
      : { ...defaultGalleryFilter };

  const canonicalRoute = buildRoutePath({ section, jobPage, jobPageSize, jobStatus, jobKind, jobSearch, gallery });
  const currentRoute = `${window.location.pathname}${window.location.search}`;

  return {
    section,
    jobPage,
    jobPageSize,
    jobStatus,
    jobKind,
    jobSearch,
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
  const [jobKind, setJobKind] = useState(initialRoute.jobKind);
  const [jobSearch, setJobSearch] = useState(initialRoute.jobSearch);
  const [gallery, setGallery] = useState<GalleryRouteState>(initialRoute.gallery);

  const stateRef = useRef({
    section: initialRoute.section,
    jobPage: initialRoute.jobPage,
    jobPageSize: initialRoute.jobPageSize,
    jobStatus: initialRoute.jobStatus,
    jobKind: initialRoute.jobKind,
    jobSearch: initialRoute.jobSearch,
    gallery: initialRoute.gallery,
  });
  stateRef.current = { section: activeSection, jobPage, jobPageSize, jobStatus, jobKind, jobSearch, gallery };

  const pushRoute = useCallback(
    (
      patch: {
        section?: SectionKey;
        jobPage?: number;
        jobPageSize?: number;
        jobStatus?: StatusFilterType;
        jobKind?: string;
        jobSearch?: string;
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
          jobKind: initialRoute.jobKind,
          jobSearch: initialRoute.jobSearch,
          gallery: initialRoute.gallery,
        },
        true,
      );
    }
  }, [initialRoute]);

  useEffect(() => {
    function handlePopState() {
      const route = readRouteState();
      if (route.section !== stateRef.current.section) {
        const guard = new Event("settings:before-navigate", { cancelable: true });
        if (!window.dispatchEvent(guard) && !window.confirm(t("放弃未保存的修改？"))) {
          // 取消离开时用 pushState 恢复当前受保护页面，避免 replaceState 覆写掉上一条历史记录。
          updateBrowserRoute(stateRef.current, false);
          return;
        }
      }
      setActiveSection(route.section);
      setJobPage(route.jobPage);
      setJobPageSize(route.jobPageSize);
      setJobStatus(route.jobStatus);
      setJobKind(route.jobKind);
      setJobSearch(route.jobSearch);
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

  const handleJobKindChange = useCallback(
    (kind: string) => {
      setJobKind(kind);
      setJobPage(defaultJobPage);
      if (isTaskCenterSection(stateRef.current.section)) {
        pushRoute({ jobKind: kind, jobPage: defaultJobPage });
      }
    },
    [pushRoute],
  );

  const handleJobSearchChange = useCallback(
    (search: string) => {
      const value = search.slice(0, 120);
      setJobSearch(value);
      setJobPage(defaultJobPage);
      if (isTaskCenterSection(stateRef.current.section)) {
        pushRoute({ jobSearch: value, jobPage: defaultJobPage }, true);
      }
    },
    [pushRoute],
  );

  /**
   * 媒体库筛选/页码统一入口：一次更新同时改 state 与 URL，避免"新筛选 + 旧页码"多发一次请求。
   * 搜索框逐字符输入用 replace 模式，避免把每个字符都压进浏览器历史。
   */
  const clearJobFilters = useCallback(() => {
    setJobStatus(defaultJobStatus);
    setJobKind(defaultJobKind);
    setJobSearch(defaultJobSearch);
    setJobPage(defaultJobPage);
    if (isTaskCenterSection(stateRef.current.section)) {
      pushRoute({
        jobStatus: defaultJobStatus,
        jobKind: defaultJobKind,
        jobSearch: defaultJobSearch,
        jobPage: defaultJobPage,
      });
    }
  }, [pushRoute]);

  const handleGalleryChange = useCallback(
    (patch: Partial<GalleryRouteState>, options?: { replace?: boolean }) => {
      // 先算结果再 setState：history 写入是副作用，不能放进 updater（StrictMode 会双调用）。
      const sanitizedPatch =
        "search" in patch
          ? { ...patch, search: parseSearchKeyword(patch.search ?? null) }
          : patch;
      const next = { ...stateRef.current.gallery, ...sanitizedPatch };
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
    jobKind,
    jobSearch,
    gallery,
    handleSectionChange,
    handleJobPageChange,
    handleJobPageSizeChange,
    handleJobStatusChange,
    handleJobKindChange,
    handleJobSearchChange,
    clearJobFilters,
    handleGalleryChange,
    syncServerPage,
  };
}
