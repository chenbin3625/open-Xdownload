import React from "react";
import Notification from "@douyinfe/semi-ui/lib/es/notification";

export interface ToastOptions { message: React.ReactNode; description?: React.ReactNode; duration?: number }
let toastSequence = 0;
const toastIds = new Map<number, string>();
function notify(type: "success" | "error" | "warning" | "info", options: ToastOptions) { const id = ++toastSequence; toastIds.set(id, Notification[type]({ title: options.message, content: options.description, duration: (options.duration ?? 4500) / 1000, showClose: true, onClose: () => toastIds.delete(id), theme: "light" })); return id; }
export const toast = { success: (o: ToastOptions) => notify("success", o), error: (o: ToastOptions) => notify("error", o), warning: (o: ToastOptions) => notify("warning", o), info: (o: ToastOptions) => notify("info", o), dismiss: (id: number) => { const key = toastIds.get(id); if (key) Notification.close(key); toastIds.delete(id); } };
export function Toaster() { return null; }
