import { useCallback, useEffect, useMemo, useState } from "react";
import { tablePageSizeOptions } from "./pagination";

export type SectionKey = "overview" | "settings";

export type RouteState = {
  section: SectionKey;
  jobPage: number;
  jobPageSize: number;
  shouldReplace: boolean;
};

export const defaultJobPage = 1;
export const defaultJobPageSize = 20;

export const sectionRoutes: Record<SectionKey, string> = {
  overview: "/overview",
  settings: "/settings",
};

export const routeSections: Record<string, SectionKey> = {
  "/": "overview",
  "/overview": "overview",
  "/jobs": "overview",
  "/settings": "settings",
};

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

export function buildRoutePath(
  section: SectionKey,
  jobPage = defaultJobPage,
  jobPageSize = defaultJobPageSize,
) {
  const params = new URLSearchParams();
  if (section === "overview") {
    if (jobPage > defaultJobPage) {
      params.set("page", String(jobPage));
    }
    if (jobPageSize !== defaultJobPageSize) {
      params.set("pageSize", String(jobPageSize));
    }
  }
  const query = params.toString();
  return `${sectionRoutes[section]}${query ? `?${query}` : ""}`;
}

export function readRouteState(): RouteState {
  if (typeof window === "undefined") {
    return {
      section: "overview",
      jobPage: defaultJobPage,
      jobPageSize: defaultJobPageSize,
      shouldReplace: false,
    };
  }

  const pathname = normalizePathname(window.location.pathname);
  const section = routeSections[pathname] ?? "overview";
  const params = new URLSearchParams(window.location.search);
  const jobPage =
    section === "overview"
      ? parsePositiveInteger(params.get("page"), defaultJobPage)
      : defaultJobPage;
  const jobPageSize =
    section === "overview"
      ? parseJobPageSize(params.get("pageSize"))
      : defaultJobPageSize;
  const canonicalRoute = buildRoutePath(section, jobPage, jobPageSize);
  const currentRoute = `${window.location.pathname}${window.location.search}`;

  return {
    section,
    jobPage,
    jobPageSize,
    shouldReplace: currentRoute !== canonicalRoute,
  };
}

export function updateBrowserRoute(
  section: SectionKey,
  jobPage = defaultJobPage,
  jobPageSize = defaultJobPageSize,
  replace = false,
) {
  if (typeof window === "undefined") return;

  const nextRoute = buildRoutePath(section, jobPage, jobPageSize);
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

  useEffect(() => {
    if (initialRoute.shouldReplace) {
      updateBrowserRoute(initialRoute.section, initialRoute.jobPage, initialRoute.jobPageSize, true);
    }
  }, [initialRoute]);

  useEffect(() => {
    function handlePopState() {
      const route = readRouteState();
      setActiveSection(route.section);
      setJobPage(route.jobPage);
      setJobPageSize(route.jobPageSize);
    }

    window.addEventListener("popstate", handlePopState);
    return () => window.removeEventListener("popstate", handlePopState);
  }, []);

  const handleSectionChange = useCallback((section: SectionKey) => {
    setActiveSection(section);
    updateBrowserRoute(section, jobPage, jobPageSize);
  }, [jobPage, jobPageSize]);

  const handleJobPageChange = useCallback((page: number) => {
    setJobPage(page);
    if (activeSection === "overview") {
      updateBrowserRoute("overview", page, jobPageSize);
    }
  }, [activeSection, jobPageSize]);

  const handleJobPageSizeChange = useCallback((pageSize: number) => {
    setJobPageSize(pageSize);
    setJobPage(1);
    if (activeSection === "overview") {
      updateBrowserRoute("overview", defaultJobPage, pageSize);
    }
  }, [activeSection]);

  const syncServerPage = useCallback((serverPage?: number) => {
    if (serverPage && serverPage !== jobPage) {
      setJobPage(serverPage);
      if (activeSection === "overview") {
        updateBrowserRoute("overview", serverPage, jobPageSize, true);
      }
    }
  }, [activeSection, jobPage, jobPageSize]);

  return {
    activeSection,
    jobPage,
    jobPageSize,
    handleSectionChange,
    handleJobPageChange,
    handleJobPageSizeChange,
    syncServerPage,
  };
}
