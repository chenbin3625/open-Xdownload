export const sessionTokenKey = "open-xdownload-session";
export const sessionInvalidatedEvent = "open-xdownload-session-invalidated";
export const sessionChangedKey = "open-xdownload-session-changed";
let revision = 0;

try { localStorage.removeItem(sessionTokenKey); } catch { /* Storage may be unavailable. */ }

export function sessionRevision() { return revision; }

export function noteSessionChange(broadcast = true) {
  revision += 1;
  try {
    localStorage.removeItem(sessionTokenKey);
    if (broadcast) localStorage.setItem(sessionChangedKey, `${Date.now()}:${revision}`);
  } catch { /* Cookie authentication does not require localStorage. */ }
}

export function invalidateSession() {
  noteSessionChange();
  if (typeof window !== "undefined") window.dispatchEvent(new Event(sessionInvalidatedEvent));
}
