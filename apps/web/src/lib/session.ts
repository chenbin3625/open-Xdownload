export const sessionTokenKey = "open-xdownload-session";
export const sessionInvalidatedEvent = "open-xdownload-session-invalidated";

export function readSessionToken(): string | null {
  try { return localStorage.getItem(sessionTokenKey); } catch { return null; }
}

export function saveSessionToken(token: string | null) {
  try {
    if (token) localStorage.setItem(sessionTokenKey, token);
    else localStorage.removeItem(sessionTokenKey);
  } catch { /* HttpOnly cookie authentication remains available. */ }
}

export function invalidateSession() {
  saveSessionToken(null);
  if (typeof window !== "undefined") window.dispatchEvent(new Event(sessionInvalidatedEvent));
}
