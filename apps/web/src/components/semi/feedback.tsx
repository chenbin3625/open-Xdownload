import React from "react";
import Banner from "@douyinfe/semi-ui/lib/es/banner";
import SEmpty from "@douyinfe/semi-ui/lib/es/empty";
import SProgress from "@douyinfe/semi-ui/lib/es/progress";
import SSkeleton from "@douyinfe/semi-ui/lib/es/skeleton";
import Spin from "@douyinfe/semi-ui/lib/es/spin";
import { cn } from "../../lib/cn";
import { t } from "../../lib/i18n";

export function Spinner({ className }: { className?: string }) { return <Spin size="small" wrapperClassName={className} />; }
export function LoadingOverlay({ loading, children, tip }: { loading: boolean; children: React.ReactNode; tip?: string }) { return <Spin spinning={loading} tip={tip} wrapperClassName="w-full min-w-0">{children}</Spin>; }
export function Alert({ type = "info", message, description, action, showIcon = true, className }: { type?: "info" | "success" | "warning" | "error"; message: React.ReactNode; description?: React.ReactNode; action?: React.ReactNode; showIcon?: boolean; className?: string }) { return <div role={type === "error" ? "alert" : "status"}><Banner type={type === "error" ? "danger" : type} title={message} description={description} closeIcon={null} fullMode={false} icon={showIcon ? undefined : null} className={className}>{action}</Banner></div>; }
export function Skeleton({ className }: { className?: string }) { return <SSkeleton loading active placeholder={<SSkeleton.Paragraph rows={1} />} className={className} />; }
export function ListSkeleton({ rows = 4, avatar = true }: { rows?: number; avatar?: boolean }) { return <div role="status" aria-label={t("加载中")} className="space-y-4">{Array.from({ length: rows }, (_, i) => <SSkeleton key={i} loading active placeholder={<div className="flex gap-3">{avatar && <SSkeleton.Avatar />}<SSkeleton.Paragraph rows={2} style={{ width: "100%" }} /></div>} />)}</div>; }
export function Empty({ description, children, className }: { description: React.ReactNode; children?: React.ReactNode; className?: string }) { return <SEmpty description={description} className={cn("py-8", className)}>{children}</SEmpty>; }
export function Progress({ percent, status, className }: { percent: number; status?: "normal" | "active" | "success" | "exception"; className?: string }) { return <SProgress percent={Math.max(0, Math.min(100, percent))} showInfo={false} stroke={status === "exception" ? "var(--semi-color-danger)" : status === "success" ? "var(--semi-color-success)" : undefined} className={className} />; }
