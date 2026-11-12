import React, { useEffect, useRef, useState } from "react";
import { Button } from "@douyinfe/semi-ui";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Eye, HardDrive, Pencil, Plus, RefreshCw, Save, Trash2, X } from "lucide-react";
import { api, type AssetKind } from "../../lib/api";
import { getDerivatives, getStorage, getTweets, migrateStorage, regenerateDerivative, saveStorage, storageQueryKey, type AssetRule, type StorageDocument, type StorageTarget } from "../../lib/archive";
import { t, useTranslation } from "../../lib/i18n";
import { Alert, Field, Input, ListSkeleton, Modal, NumberInput, PasswordInput, Popconfirm, Select, Switch, Table, Tag, Tooltip, toast } from "../semi";

const kinds: AssetKind[] = ["image", "video", "thumbnail", "video_cover"];
const emptyTarget: StorageTarget = { id: "", name: "", type: "local", version: 0, root: "" };

export function StorageManagement() {
  useTranslation(); const client = useQueryClient();
  const query = useQuery({ queryKey: storageQueryKey, queryFn: ({ signal }) => getStorage(signal) });
  const [draft, setDraft] = useState<StorageDocument | null>(null);
  const currentDraft = useRef(draft); currentDraft.current = draft;
  const [dirty, setDirty] = useState(false);
  const [target, setTarget] = useState<StorageTarget | null>(null);
  const [editingId, setEditingId] = useState<string | null>(null);
  const [locations, setLocations] = useState<Record<string, string>>({});
  const derivatives = useQuery({ queryKey: ["derivative-status"], queryFn: ({ signal }) => getDerivatives(signal), refetchInterval: 10_000 });
  const tweets = useQuery({ queryKey: ["storage-preview-tweets"], queryFn: ({ signal }) => getTweets(new URLSearchParams({ pageSize: "1" }), signal) });
  useEffect(() => { if (query.data && !dirty) setDraft(query.data); }, [query.data, dirty]);
  function update(next: StorageDocument) { setDraft(next); setDirty(true); setLocations({}); }
  const save = useMutation({ mutationFn: saveStorage, onSuccess: (document, submitted) => {
    client.setQueryData(storageQueryKey, document);
    const edited = currentDraft.current !== submitted;
    setDraft(current => edited && current ? { ...current, version: document.version, targets: current.targets.map(target => ({ ...target, version: document.targets.find(saved => saved.id === target.id)?.version ?? target.version })) } : document);
    setDirty(edited); toast.success({ message: t("storage.saved") });
  }, onError: error => toast.error({ message: error.message }) });
  const regenerate = useMutation({ mutationFn: regenerateDerivative, onSuccess: () => toast.success({ message: t("archive.retryQueued") }), onError: error => toast.error({ message: error.message }) });
  const migration = useMutation({ mutationFn: migrateStorage, onSuccess: () => { void client.invalidateQueries({ queryKey: ["jobs"] }); toast.success({ message: t("storage.migrationQueued") }); }, onError: error => toast.error({ message: error.message }) });
  const preview = useMutation({ mutationFn: async (kind: AssetKind) => {
    if (!draft) throw new Error(t("暂无数据"));
    const tweet = tweets.data?.items[0] ?? { id: "2120206875581186293", text: "Archive preview", createdAt: "2026-11-10T12:00:00Z", author: { id: "44196397", name: "Author", screenName: "author" }, media: [] };
    return { kind, ...await api<{ location: string }>("/api/storage/preview", { method: "POST", body: JSON.stringify({ document: draft, kind, tweet, media: tweet.media[0] ?? { id: "media-1", type: "photo" }, index: 0, extension: kind === "video" ? "mp4" : kind === "thumbnail" ? draft.thumbnail.format === "jpeg" ? "jpg" : draft.thumbnail.format : "jpg" }) }) };
  }, onSuccess: value => setLocations(current => ({ ...current, [value.kind]: value.location })), onError: error => toast.error({ message: error.message }) });
  if (!draft) return query.isError ? <Alert type="error" message={query.error.message} action={<Button onClick={() => void query.refetch()}>{t("重试")}</Button>} /> : <ListSkeleton rows={3} />;
  function ruleFields(kind: AssetKind | "default", rule: AssetRule) {
    const current = draft!;
    const change = (patch: Partial<AssetRule>) => update(kind === "default" ? { ...current, default: { ...rule, ...patch } } : { ...current, rules: { ...current.rules, [kind]: { ...rule, ...patch } } });
    return <div className="storage-rule-fields">
      <Field label={t("storage.target")}><Select value={rule.targetId} onChange={value => change({ targetId: value })} options={current.targets.map(item => ({ value: item.id, label: item.name || item.id }))} /></Field>
      <Field label={t("storage.prefix")}><Input value={rule.prefix} onChange={e => change({ prefix: e.target.value })} /></Field>
      <Field label={t("storage.directoryTemplate")}><Input value={rule.directoryTemplate} onChange={e => change({ directoryTemplate: e.target.value })} /></Field>
      <Field label={t("storage.filenameTemplate")}><Input value={rule.filenameTemplate} onChange={e => change({ filenameTemplate: e.target.value })} /></Field>
    </div>;
  }
  function commitTarget() {
    if (!target || !draft) return;
    if (!/^[a-zA-Z0-9_-]{1,64}$/.test(target.id) || (!editingId && draft.targets.some(item => item.id === target.id))) { toast.error({ message: t("storage.invalidId") }); return; }
    update({ ...draft, targets: editingId ? draft.targets.map(item => item.id === editingId ? target : item) : [...draft.targets, target] }); setTarget(null);
  }
  return <section className="semi-settings-section">
    <h2 className="settings-section-title"><HardDrive size={16} />{t("storage.title")}</h2>
    <div className="flex flex-wrap items-center justify-between gap-3 mb-5"><div className="flex flex-wrap items-center gap-3"><Switch checked={draft.enabled} onChange={value => update({ ...draft, enabled: value })} ariaLabel={t("storage.enabled")} /><span className="text-sm">{t("storage.enabled")}</span><Tag>v{draft.version}</Tag>{dirty && <Tag tone="warning">{t("storage.unsaved")}</Tag>}</div><Button theme="solid" icon={<Save size={16} />} disabled={!dirty || save.isPending} loading={save.isPending} aria-busy={save.isPending} onClick={() => save.mutate(draft)}>{t("保存配置")}</Button></div>
    <div className="flex flex-wrap items-center justify-between gap-3 mb-3"><h4 className="text-sm font-semibold">{t("storage.targets")}</h4><Button type="tertiary" icon={<Plus size={16} />} onClick={() => { setEditingId(null); setTarget({ ...emptyTarget }); }}>{t("storage.addTarget")}</Button></div>
    <Table data={draft.targets} rowKey={item => item.id} minWidth={640} columns={[
      { key: "name", title: t("名称"), render: item => <div><strong className="text-sm">{item.name || item.id}</strong><div className="text-xs text-fg-muted">{item.id}</div></div> },
      { key: "type", title: t("类型"), width: 90, render: item => <Tag>{item.type.toUpperCase()}</Tag> },
      { key: "root", title: t("storage.location"), render: item => <span className="break-all text-xs font-mono">{item.type === "s3" ? `${item.endpoint}/${item.bucket}` : item.root}</span> },
      { key: "actions", title: "", width: 96, render: item => { const inUse = draft.default.targetId === item.id || Object.values(draft.rules).some(rule => rule?.targetId === item.id); return <div className="flex gap-1"><Tooltip title={t("编辑")}><Button theme="borderless" type="tertiary" icon={<Pencil size={16} />} aria-label={t("编辑")} onClick={() => { setEditingId(item.id); setTarget({ ...item }); }} /></Tooltip><Tooltip title={t(inUse ? "storage.targetInUse" : "删除")}><span><Popconfirm title={t("storage.removeTarget")} danger disabled={inUse || draft.targets.length === 1} onConfirm={() => update({ ...draft, targets: draft.targets.filter(t => t.id !== item.id) })}><Button theme="borderless" type="danger" icon={<Trash2 size={16} />} aria-label={t("删除")} disabled={inUse || draft.targets.length === 1} /></Popconfirm></span></Tooltip></div>; } },
    ]} />
    <div className="storage-default"><h4 className="text-sm font-semibold mb-3">{t("storage.default")}</h4>{ruleFields("default", draft.default)}</div>
    <div className="flex flex-wrap items-center justify-between gap-3 py-4"><span className="text-xs text-fg-muted">{t("storage.sourceRetained")}</span><Popconfirm title={t("storage.migrateConfirm")} onConfirm={() => migration.mutate()}><Button disabled={!draft.enabled || dirty || migration.isPending} loading={migration.isPending} aria-busy={migration.isPending} icon={<RefreshCw size={16} />}>{t("storage.migrate")}</Button></Popconfirm></div>
    {kinds.map(kind => <section className="storage-policy" key={kind}>
      <div className="flex flex-wrap items-center justify-between gap-2 mb-3"><h4 className="text-sm font-semibold">{t(`storage.kind.${kind}`)}</h4><div className="flex items-center gap-2 text-xs"><span>{t("storage.inherit")}</span><Switch checked={!draft.rules[kind]} ariaLabel={`${t(`storage.kind.${kind}`)} ${t("storage.inherit")}`} onChange={inherit => { const rules = { ...draft.rules }; if (inherit) delete rules[kind]; else rules[kind] = { ...draft.default }; update({ ...draft, rules }); }} /><Tooltip title={t("storage.preview")}><Button icon={<Eye size={16} />} theme="borderless" type="tertiary" aria-label={`${t("storage.preview")} ${t(`storage.kind.${kind}`)}`} loading={preview.isPending && preview.variables === kind} disabled={preview.isPending && preview.variables === kind} aria-busy={preview.isPending && preview.variables === kind} onClick={() => preview.mutate(kind)} /></Tooltip></div></div>
      {draft.rules[kind] ? ruleFields(kind, draft.rules[kind]!) : <div className="text-xs text-fg-muted break-all">{draft.targets.find(item => item.id === draft.default.targetId)?.name} / {draft.default.prefix}{draft.default.directoryTemplate} / {draft.default.filenameTemplate}</div>}
      {locations[kind] && <output className="storage-path-preview">{locations[kind]}</output>}
    </section>)}
    <section className="storage-policy"><h4 className="text-sm font-semibold mb-3">{t("storage.thumbnail")}</h4><div className="storage-rule-fields">
      <Field label={t("storage.width")}><NumberInput min={16} max={4096} value={draft.thumbnail.width} onChange={value => update({ ...draft, thumbnail: { ...draft.thumbnail, width: value ?? 640 } })} /></Field>
      <Field label={t("storage.height")}><NumberInput min={16} max={4096} value={draft.thumbnail.height} onChange={value => update({ ...draft, thumbnail: { ...draft.thumbnail, height: value ?? 640 } })} /></Field>
      <Field label={t("storage.format")}><Select value={draft.thumbnail.format} options={[{ value: "jpeg", label: "JPEG" }, { value: "png", label: "PNG" }, { value: "webp", label: "WebP" }]} onChange={value => update({ ...draft, thumbnail: { ...draft.thumbnail, format: value } })} /></Field>
      <Field label={t("storage.quality")}><NumberInput min={1} max={100} value={draft.thumbnail.quality} onChange={value => update({ ...draft, thumbnail: { ...draft.thumbnail, quality: value ?? 82 } })} /></Field>
    </div></section>
    {derivatives.data && derivatives.data.items.length > 0 && <section className="storage-policy"><h4 className="text-sm font-semibold mb-3">{t("storage.derivatives")}</h4><Table data={derivatives.data.items} rowKey={item => `${item.mediaKey}-${item.kind}`} minWidth={520} columns={[
      { key: "kind", title: t("类型"), render: item => t(`storage.kind.${item.kind}`) },
      { key: "status", title: t("状态"), render: item => <div><Tag tone={item.status === "failed" ? "danger" : "success"}>{t(`storage.status.${item.status}`, { defaultValue: item.status })}</Tag>{item.error && <p className="text-xs text-danger break-words">{item.error}</p>}</div> },
      { key: "action", title: "", width: 64, render: item => <Tooltip title={t("storage.regenerate")}><Button type="tertiary" icon={<RefreshCw size={16} />} aria-label={t("storage.regenerate")} loading={regenerate.isPending && regenerate.variables === item} disabled={regenerate.isPending && regenerate.variables === item} aria-busy={regenerate.isPending && regenerate.variables === item} onClick={() => regenerate.mutate(item)} /></Tooltip> },
    ]} /></section>}
    <Modal open={target !== null} onClose={() => setTarget(null)} title={t(editingId ? "storage.editTarget" : "storage.addTarget")} footer={<><Button type="tertiary" onClick={() => setTarget(null)}>{t("取消")}</Button><Button theme="solid" onClick={commitTarget}>{t("确定")}</Button></>}>
      {target && <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
        <Field label={t("storage.targetId")}><Input value={target.id} disabled={editingId !== null} onChange={e => setTarget({ ...target, id: e.target.value })} /></Field>
        <Field label={t("名称")}><Input value={target.name} onChange={e => setTarget({ ...target, name: e.target.value })} /></Field>
        <Field label={t("类型")}><Select value={target.type} onChange={value => setTarget({ ...target, type: value as StorageTarget["type"] })} options={[{ value: "local", label: t("本地存储") }, { value: "nas", label: t("storage.nas") }, { value: "s3", label: "S3" }]} /></Field>
        {target.type !== "s3" ? <Field label={t("storage.root")}><Input value={target.root ?? ""} onChange={e => setTarget({ ...target, root: e.target.value })} /></Field> : <>
          {(["endpoint", "bucket", "region", "accessKeyEnv", "secretKeyEnv"] as const).map(key => <Field key={key} label={t(`storage.${key}`)}><Input value={target[key] ?? ""} onChange={e => setTarget({ ...target, [key]: e.target.value })} /></Field>)}
          {(["accessKey", "secretKey", "sessionToken"] as const).map(key => <Field key={key} label={t(`storage.${key}`)}><div className="flex gap-2"><PasswordInput value={target[key] === "__CLEAR__" ? "" : target[key] ?? ""} onChange={e => setTarget({ ...target, [key]: e.target.value })} /><Tooltip title={t("清除")}><Button icon={<X size={14} />} aria-label={`${t("清除")} ${t(`storage.${key}`)}`} onClick={() => setTarget({ ...target, [key]: "__CLEAR__" })} /></Tooltip></div></Field>)}
        </>}
      </div>}
    </Modal>
  </section>;
}
