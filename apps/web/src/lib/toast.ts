export function toast(
  title: string,
  options?: { description?: string; tone?: "ok" | "err" },
) {
  if (typeof document === "undefined") return;
  let host = document.getElementById("app-toasts");
  if (!host) {
    host = document.createElement("div");
    host.id = "app-toasts";
    host.className = "app-toasts";
    host.setAttribute("aria-live", "polite");
    document.body.appendChild(host);
  }
  const item = document.createElement("div");
  item.className = `app-toast app-toast-${options?.tone ?? "ok"}`;
  const heading = document.createElement("strong");
  heading.textContent = title;
  item.appendChild(heading);
  if (options?.description) {
    const detail = document.createElement("span");
    detail.textContent = options.description;
    item.appendChild(detail);
  }
  host.appendChild(item);
  window.setTimeout(() => item.remove(), 2800);
}
