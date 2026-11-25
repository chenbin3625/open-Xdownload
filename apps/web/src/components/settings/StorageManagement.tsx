import React, { useEffect, useRef, useState } from "react";
import Button from "@douyinfe/semi-ui/lib/es/button";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronDown, ChevronUp, Copy, Eye, FolderOpen, HardDrive, Pencil, Plus, RefreshCw, Save, Server, Trash2 } from "lucide-react";
import { api, type AssetKind, type TweetData } from "../../lib/api";
import { getStorage, getTweets, migrateStorage, saveStorage, storageQueryKey, type AssetRule, type StorageDocument, type StorageTarget } from "../../lib/archive";
import { t, useTranslation } from "../../lib/i18n";
import { Alert, ListSkeleton } from "../semi/feedback";
import { Field, Input, PasswordInput, Select, Switch } from "../semi/controls";
import { Modal, Popconfirm, Tooltip } from "../semi/overlays";
import { Tag } from "../semi/data";
import { toast } from "../semi/toast";
import { LocalDirectoryPicker } from "./LocalDirectoryPicker";
import { FilenameStrategy } from "./FilenameStrategy";
import { copyToClipboard } from "../common/CommonUI";

const kinds: AssetKind[] = ["image", "video"];

function directoryOptions() {
  return [
    { value: "", label: t("全部放在同一目录") },
    { value: "{username}", label: t("按用户") },
    { value: "{username}/{year}/{month}", label: t("按用户 / 年 / 月") },
    { value: "{year}/{month}", label: t("按年 / 月") },
    { value: "{kind}/{username}", label: t("按媒体类型 / 用户") },
    { value: "{author_id}/{tweet_id}", label: t("按用户编号 / 推文编号") },
  ];
}

function StoragePathPreview({ document, kind, tweet: sample }: { document: StorageDocument; kind: AssetKind; tweet?: TweetData }) {
  const [location, setLocation] = useState("");
  const current = useRef(document); current.current = document;
  useEffect(() => setLocation(""), [document]);
  const preview = useMutation({ mutationFn: async (submitted: StorageDocument) => {
    const tweet = sample ?? { id: "2120206875581186293", text: t("归档预览"), createdAt: "2026-11-10T12:00:00Z", author: { id: "44196397", name: "Author", screenName: "author" }, media: [] };
    const video = kind === "video";
    const index = tweet.media.findIndex(media => video ? media.type !== "photo" : media.type === "photo");
    const media = tweet.media[index] ?? { id: video ? "video-1" : "image-1", type: video ? "video" : "photo" };
    const extension = kind === "video" ? "mp4" : "jpg";
    return api<{ location: string }>("/api/storage/preview", { method: "POST", body: JSON.stringify({ document: submitted, kind, tweet, media, index: Math.max(0, index), extension }) });
  }, onSuccess: (value, submitted) => { if (current.current === submitted) setLocation(value.location); } });
  return <div className="storage-preview-row"><Button type="tertiary" icon={<Eye size={16} />} aria-label={`${t(`storage.kind.${kind}`)} ${t("storage.preview")}`} loading={preview.isPending} disabled={preview.isPending} onClick={() => preview.mutate(document)}>{t("storage.preview")}</Button>{location && <><output className="storage-path-preview">{location}</output><Tooltip content={t("复制本地路径")}><Button theme="borderless" icon={<Copy size={16} />} aria-label={t("复制本地路径")} onClick={() => void copyToClipboard(location, t("本地路径"))} /></Tooltip></>}{preview.isError && preview.variables === document && <span className="text-xs text-danger">{preview.error.message}</span>}</div>;
}

export function StorageManagement() {
  useTranslation();
  const client = useQueryClient();
  const query = useQuery({ queryKey: storageQueryKey, queryFn: ({ signal }) => getStorage(signal) });
  const [draft, setDraft] = useState<StorageDocument | null>(null);
  const currentDraft = useRef(draft); currentDraft.current = draft;
  const [dirty, setDirty] = useState(false);
  const [migrationId, setMigrationId] = useState<number | null>(null);
  const [pickingDirectory, setPickingDirectory] = useState(false);
  const pickingDirectoryRef = useRef(pickingDirectory); pickingDirectoryRef.current = pickingDirectory;
  const [defaultRulesCollapsed, setDefaultRulesCollapsed] = useState(true);
  const [targetsModalOpen, setTargetsModalOpen] = useState(false);
  const [editingTarget, setEditingTarget] = useState<StorageTarget | null>(null);
  const [isNewTarget, setIsNewTarget] = useState(false);
  const tweets = useQuery({
    queryKey: ["storage-preview-tweets"],
    queryFn: ({ signal }) => getTweets(new URLSearchParams({ pageSize: "1" }), signal),
    staleTime: 60_000,
  });
  useEffect(() => { if (query.data && !dirty) setDraft(query.data); }, [query.data, dirty]);
  useEffect(() => {
    if (!dirty) return;
    const protectDraft = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = ""; };
    const protectNavigation = (event: Event) => event.preventDefault();
    window.addEventListener("beforeunload", protectDraft);
    window.addEventListener("settings:before-navigate", protectNavigation);
    return () => { window.removeEventListener("beforeunload", protectDraft); window.removeEventListener("settings:before-navigate", protectNavigation); };
  }, [dirty]);
  function update(next: StorageDocument) { setDraft({ ...next, enabled: true }); setDirty(true); }
  const save = useMutation({ mutationFn: saveStorage, onSuccess: (document, submitted) => {
    client.setQueryData(storageQueryKey, document);
    const edited = currentDraft.current !== submitted;
    setDraft(current => edited && current ? { ...current, version: document.version, targets: current.targets.map(target => ({ ...target, version: document.targets.find(saved => saved.id === target.id)?.version ?? target.version })) } : document);
    setDirty(edited); toast.success({ message: t("storage.saved") });
  }, onError: error => toast.error({ message: error.message }) });
  const migration = useMutation({ mutationFn: migrateStorage, onSuccess: job => { setMigrationId(job.id); void client.invalidateQueries({ queryKey: ["jobs"] }); toast.success({ message: t("storage.migrationQueued") }); }, onError: error => toast.error({ message: error.message }) });
  if (!draft) return query.isError ? <Alert type="error" message={query.error.message} action={<Button type="tertiary" onClick={() => void query.refetch()}>{t("重试")}</Button>} /> : <ListSkeleton rows={3} />;
  const target = draft.targets.find(item => item.id === draft.default.targetId);
  function changeRoot(root: string) {
    if (root === target?.root) return;
    update({ ...draft!, targets: draft!.targets.map(item => item.id === target?.id ? { ...item, root } : item) });
  }
  function openDirectoryPicker() {
    void client.invalidateQueries({ queryKey: ["local-directories"] });
    setPickingDirectory(true);
  }
  function ruleFields(kind: AssetKind | "default", rule: AssetRule, inherited = false) {
    const change = (patch: Partial<AssetRule>) => update(kind === "default" ? { ...draft!, default: { ...rule, ...patch } } : { ...draft!, rules: { ...draft!.rules, [kind]: { ...rule, ...patch } } });
    const directories = directoryOptions();
    if (!directories.some(item => item.value === rule.directoryTemplate)) directories.push({ value: rule.directoryTemplate, label: t("保留现有分类方式") });
    const ruleTarget = draft!.targets.find(item => item.id === rule.targetId);
    return <>
      {kind !== "default" && <Field label={t("storage.target")} className="storage-media-field"><Select disabled={inherited} ariaLabel={`${t(`storage.kind.${kind}`)} ${t("storage.target")}`} value={rule.targetId} options={draft!.targets.map(item => ({ value: item.id, label: item.name || item.id }))} onChange={targetId => { if (targetId !== rule.targetId) change({ targetId }); }} /><span className="storage-target-path">{ruleTarget?.type === "s3" ? `s3://${ruleTarget.bucket ?? ""}` : ruleTarget?.root}</span></Field>}
      <Field label={t("分类存储策略")} className={kind !== "default" ? "storage-media-field" : undefined}>
        <Select disabled={inherited} ariaLabel={kind === "default" ? t("分类存储策略") : `${t(`storage.kind.${kind}`)} ${t("分类存储策略")}`} value={rule.directoryTemplate} options={directories} onChange={directoryTemplate => { if (directoryTemplate !== rule.directoryTemplate) change({ directoryTemplate, directoryTemplateSet: true }); }} />
        {rule.prefix && <span className="storage-target-path">{t("storage.prefix")}: {rule.prefix}</span>}
        <div className="storage-folder-toggle"><Switch disabled={inherited} checked={Boolean(rule.separateFolder || rule.directoryTemplate.includes("{kind}"))} ariaLabel={`${kind === "default" ? "" : `${t(`storage.kind.${kind}`)} `}${t("按媒体类型分文件夹")}`} onChange={separateFolder => change({
          separateFolder,
          ...(!separateFolder && rule.directoryTemplate.includes("{kind}") ? { directoryTemplate: rule.directoryTemplate.replaceAll("{kind}", "").split("/").filter(Boolean).join("/"), directoryTemplateSet: true } : {}),
        })} /><span>{t("按媒体类型分文件夹")}</span></div>
      </Field>
      <Field label={t("命名策略")} className={kind !== "default" ? "storage-media-field" : undefined}><FilenameStrategy disabled={inherited} labelPrefix={kind === "default" ? "" : t(`storage.kind.${kind}`)} value={rule.filenameTemplate} onChange={filenameTemplate => { if (filenameTemplate !== rule.filenameTemplate) change({ filenameTemplate }); }} /></Field>
    </>;
  }
  return <section id="settings-storage" className="semi-settings-section storage-settings">
    <div className="settings-section-heading"><h2 className="settings-section-title"><HardDrive size={16} />{t("存储设置")}</h2><div className="flex flex-wrap items-center gap-2">{dirty && <Tag tone="warning">{t("storage.unsaved")}</Tag>}<Button theme="solid" icon={<Save size={16} />} disabled={!dirty || save.isPending} loading={save.isPending} onClick={() => save.mutate(draft)}>{t("保存存储设置")}</Button></div></div>
    <div className="storage-location">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h3 className="settings-subsection-title">{t("storage.location")}</h3>
        <Button type="tertiary" size="small" icon={<Server size={14} />} onClick={() => { setEditingTarget(null); setTargetsModalOpen(true); }}>
          {t("管理存储目标")} ({draft.targets.length})
        </Button>
      </div>
      <div className={`storage-directory-options ${draft.targets.length > 1 ? "storage-has-targets" : ""}`}>
        {draft.targets.length > 1 && <Field label={t("storage.target")}><Select ariaLabel={t("storage.target")} value={draft.default.targetId} options={draft.targets.map(item => ({ value: item.id, label: item.name || item.id }))} onChange={targetId => { if (targetId !== draft.default.targetId) update({ ...draft, default: { ...draft.default, targetId } }); }} /></Field>}
        <Field label={t("保存目录")}><div className="flex min-w-0 gap-2"><Input aria-label={t("保存目录")} value={target?.type === "s3" ? `s3://${target.bucket ?? ""}` : target?.root ?? ""} disabled={target?.type === "s3"} onChange={event => changeRoot(event.target.value)} />{target?.type !== "s3" && <Tooltip content={t("选择目录")}><Button type="tertiary" icon={<FolderOpen size={16} />} aria-label={t("选择目录")} onClick={openDirectoryPicker} /></Tooltip>}</div></Field>
      </div>
    </div>
    <div className="storage-default-rules">
      <div className="flex items-center justify-between gap-3">
        <h3 className="settings-subsection-title">{t("storage.default")}</h3>
        <Button type="tertiary" size="small" icon={defaultRulesCollapsed ? <ChevronDown size={14} /> : <ChevronUp size={14} />} aria-expanded={!defaultRulesCollapsed} onClick={() => setDefaultRulesCollapsed(collapsed => !collapsed)}>{defaultRulesCollapsed ? t("展开默认规则") : t("收起默认规则")}</Button>
      </div>
      {!defaultRulesCollapsed && <><div className="storage-policy-options">{ruleFields("default", draft.default)}</div><div className="storage-filename-limit">{t("文件名上限：240 字节（含扩展名）")}</div></>}
    </div>
    <div className="storage-media-rules">
      <h3 className="settings-subsection-title">{t("按媒体类型单独设置")}</h3>
      {kinds.map(kind => <div className="storage-media-row" role="group" aria-label={t(`storage.kind.${kind}`)} key={kind}>
        <span className="storage-media-name">{t(`storage.kind.${kind}`)}</span>
        <div className="storage-media-mode"><Switch checked={Boolean(draft.rules[kind])} ariaLabel={`${t(`storage.kind.${kind}`)} ${t("单独设置")}`} onChange={custom => { const rules = { ...draft.rules }; if (custom) rules[kind] = { ...draft.default }; else delete rules[kind]; update({ ...draft, rules }); }} /><span>{t(draft.rules[kind] ? "单独设置" : "使用默认")}</span></div>
        {draft.rules[kind] ? ruleFields(kind, { ...draft.rules[kind]!, separateFolder: draft.rules[kind]?.separateFolder ?? draft.default.separateFolder }) : <div className="storage-inherited-summary"><span>{target?.name || target?.id} · {target?.type === "s3" ? `s3://${target.bucket ?? ""}` : target?.root}</span><span>{directoryOptions().find(option => option.value === draft.default.directoryTemplate)?.label || t("保留现有分类方式")}</span><span>{t("命名策略")}: {draft.default.filenameTemplate.replace(/\{(text|tweet_id|media_id|username|index|ext)\}/g, (_token, field: string) => t(({ text: "正文", tweet_id: "推文编号", media_id: "媒体编号", username: "用户名", index: "媒体序号", ext: "扩展名" } as Record<string, string>)[field]))}</span></div>}
        <StoragePathPreview document={draft} kind={kind} tweet={tweets.data?.items[0]} />
      </div>)}
    </div>
    <div className="storage-migration-row"><div><h3 className="settings-subsection-title">{t("历史文件整理")}</h3><p className="text-xs text-fg-muted">{dirty ? t("请先保存存储设置") : t("storage.sourceRetained")}</p>{migrationId && <a className="text-sm text-brand-600" href="/tasks">{t("查看整理任务")} #{migrationId}</a>}</div><Popconfirm title={t("storage.migrateConfirm")} disabled={!draft.enabled || dirty || migration.isPending} onConfirm={() => { if (!draft.enabled || dirty || migration.isPending) return; migration.mutate(); }}><Button type="tertiary" disabled={!draft.enabled || dirty || migration.isPending} loading={migration.isPending} icon={<RefreshCw size={16} />}>{t("storage.migrate")}</Button></Popconfirm></div>
    <Modal open={pickingDirectory} onClose={() => setPickingDirectory(false)} title={t("选择目录")} footer={null}><LocalDirectoryPicker path={target?.root ?? ""} onSelect={root => { if (!pickingDirectoryRef.current) return; changeRoot(root); setPickingDirectory(false); }} /></Modal>
    <Modal
      open={targetsModalOpen}
      onClose={() => { setEditingTarget(null); setTargetsModalOpen(false); }}
      title={t("多存储目标管理 (Local / NAS / S3)")}
      width="min(92vw, 42rem)"
    >
      <div className="space-y-4">
        <div className="flex items-center justify-between gap-2">
          <span className="text-xs text-fg-muted">{t("可配置多个本地目录、NAS 挂载路径或 S3/R2/MinIO 对象存储，配置完成后请点击外层“保存存储设置”。")}</span>
          <Button
            size="small"
            theme="solid"
            icon={<Plus size={14} />}
            onClick={() => {
              setIsNewTarget(true);
              setEditingTarget({
                id: `target-${draft.targets.length + 1}`,
                name: "",
                type: "s3",
                version: 1,
                root: "",
                endpoint: "",
                region: "auto",
                bucket: "",
                accessKey: "",
                secretKey: "",
                accessKeyEnv: "",
                secretKeyEnv: "",
              });
            }}
          >
            {t("新增存储目标")}
          </Button>
        </div>

        <div className="divide-y divide-line rounded-card border border-line">
          {draft.targets.map((item) => {
            const inUse = draft.default.targetId === item.id || Object.values(draft.rules).some((r) => r?.targetId === item.id);
            return (
              <div key={item.id} className="flex flex-wrap items-center justify-between gap-2 p-3 text-xs">
                <div className="min-w-0 flex-1 space-y-0.5">
                  <div className="flex items-center gap-2">
                    <span className="text-sm font-semibold text-fg">{item.name || item.id}</span>
                    <Tag size="sm">{item.type.toUpperCase()}</Tag>
                    <code className="text-fg-subtle">{item.id}</code>
                    {inUse && <Tag tone="brand" size="sm">{t("使用中")}</Tag>}
                  </div>
                  <div className="truncate font-mono text-fg-muted">
                    {item.type === "s3" ? `s3://${item.bucket ?? ""} (${item.endpoint || item.region || "default"})` : item.root || "—"}
                  </div>
                </div>
                <div className="flex items-center gap-1">
                  <Button
                    size="small"
                    theme="borderless"
                    type="tertiary"
                    icon={<Pencil size={14} />}
                    aria-label={`${t("编辑存储目标")} ${item.name || item.id}`}
                    onClick={() => { setIsNewTarget(false); setEditingTarget({ ...item }); }}
                  />
                  {!inUse && draft.targets.length > 1 && (
                    <Button
                      size="small"
                      theme="borderless"
                      type="danger"
                      icon={<Trash2 size={14} />}
                      aria-label={`${t("移除存储目标")} ${item.name || item.id}`}
                      onClick={() => update({ ...draft, targets: draft.targets.filter((tItem) => tItem.id !== item.id) })}
                    />
                  )}
                </div>
              </div>
            );
          })}
        </div>

        {editingTarget && (
          <div className="rounded-card border border-line bg-surface-muted/40 p-3.5 space-y-3">
            <div className="text-sm font-semibold text-fg">{isNewTarget ? t("新增存储目标") : t("编辑存储目标")}</div>
            <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
              <Field label={t("目标 ID")}>
                <Input
                  value={editingTarget.id}
                  disabled={!isNewTarget}
                  onChange={(e) => setEditingTarget({ ...editingTarget, id: e.target.value })}
                  placeholder="s3-archive"
                />
              </Field>
              <Field label={t("显示名称")}>
                <Input
                  value={editingTarget.name}
                  onChange={(e) => setEditingTarget({ ...editingTarget, name: e.target.value })}
                  placeholder={t("例如：Cloudflare R2")}
                />
              </Field>
              <Field label={t("存储类型")}>
                <Select
                  ariaLabel={t("存储类型")}
                  value={editingTarget.type}
                  onChange={(type) => setEditingTarget({ ...editingTarget, type: type as StorageTarget["type"] })}
                  options={[
                    { value: "local", label: t("本地磁盘 (Local)") },
                    { value: "nas", label: t("网络存储 (NAS)") },
                    { value: "s3", label: t("对象存储 (S3 / R2 / MinIO)") },
                  ]}
                />
              </Field>
            </div>
            {editingTarget.type === "s3" ? (
              <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
                <Field label={t("S3 Endpoint")}>
                  <Input
                    value={editingTarget.endpoint ?? ""}
                    onChange={(e) => setEditingTarget({ ...editingTarget, endpoint: e.target.value })}
                    placeholder="https://<account>.r2.cloudflarestorage.com"
                  />
                </Field>
                <Field label={t("Bucket 名称")}>
                  <Input
                    value={editingTarget.bucket ?? ""}
                    onChange={(e) => setEditingTarget({ ...editingTarget, bucket: e.target.value })}
                    placeholder="x-archive"
                  />
                </Field>
                <Field label={t("Region")}>
                  <Input
                    value={editingTarget.region ?? ""}
                    onChange={(e) => setEditingTarget({ ...editingTarget, region: e.target.value })}
                    placeholder="auto / us-east-1"
                  />
                </Field>
                <Field label={t("Access Key")}>
                  <Input
                    value={editingTarget.accessKey ?? ""}
                    onChange={(e) => setEditingTarget({ ...editingTarget, accessKey: e.target.value })}
                    placeholder={t("或通过下方环境变量提供")}
                  />
                </Field>
                <Field label={t("Secret Key")}>
                  <PasswordInput
                    value={editingTarget.secretKey ?? ""}
                    onChange={(e) => setEditingTarget({ ...editingTarget, secretKey: e.target.value })}
                    placeholder={t("或通过下方环境变量提供")}
                  />
                </Field>
                <Field label={t("凭证环境变量名 (可选，Access / Secret)")}>
                  <div className="grid grid-cols-2 gap-2">
                    <Input
                      value={editingTarget.accessKeyEnv ?? ""}
                      onChange={(e) => setEditingTarget({ ...editingTarget, accessKeyEnv: e.target.value })}
                      placeholder="AWS_ACCESS_KEY_ID"
                    />
                    <Input
                      value={editingTarget.secretKeyEnv ?? ""}
                      onChange={(e) => setEditingTarget({ ...editingTarget, secretKeyEnv: e.target.value })}
                      placeholder="AWS_SECRET_ACCESS_KEY"
                    />
                  </div>
                </Field>
              </div>
            ) : (
              <Field label={t("根目录路径")}>
                <Input
                  value={editingTarget.root ?? ""}
                  onChange={(e) => setEditingTarget({ ...editingTarget, root: e.target.value })}
                  placeholder="/mnt/nas/x-archive"
                />
              </Field>
            )}
            <div className="flex justify-end gap-2">
              <Button size="small" type="tertiary" onClick={() => setEditingTarget(null)}>{t("取消")}</Button>
              <Button
                size="small"
                theme="solid"
                disabled={!editingTarget.id.trim() || (editingTarget.type === "s3" ? !editingTarget.bucket?.trim() : !editingTarget.root?.trim())}
                onClick={() => {
                  const id = editingTarget.id.trim();
                  const nextTarget: StorageTarget = { ...editingTarget, id, name: editingTarget.name.trim() || id };
                  const exists = draft.targets.some((item) => item.id === id);
                  if (isNewTarget && exists) {
                    toast.error({ message: t("目标 ID 已存在") });
                    return;
                  }
                  const nextTargets = exists
                    ? draft.targets.map((item) => (item.id === id ? nextTarget : item))
                    : [...draft.targets, nextTarget];
                  update({ ...draft, targets: nextTargets });
                  setEditingTarget(null);
                }}
              >
                {t("应用目标变更")}
              </Button>
            </div>
          </div>
        )}
      </div>
    </Modal>
  </section>;
}
