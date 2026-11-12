import React, { useEffect, useRef, useState } from "react";
import { Button } from "@douyinfe/semi-ui";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Eye, FolderOpen, HardDrive, RefreshCw, Save } from "lucide-react";
import { api, type AssetKind, type TweetData } from "../../lib/api";
import { getStorage, getTweets, migrateStorage, saveStorage, storageQueryKey, type AssetRule, type StorageDocument } from "../../lib/archive";
import { t, useTranslation } from "../../lib/i18n";
import { Alert, Field, Input, ListSkeleton, Modal, Popconfirm, Select, Switch, Tag, Tooltip, toast } from "../semi";
import { LocalDirectoryPicker } from "./LocalDirectoryPicker";
import { FilenameStrategy } from "./FilenameStrategy";

const kinds: AssetKind[] = ["image", "video", "thumbnail", "video_cover"];

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
    const video = kind === "video" || kind === "video_cover";
    const index = tweet.media.findIndex(media => video ? media.type !== "photo" : media.type === "photo");
    const media = tweet.media[index] ?? { id: video ? "video-1" : "image-1", type: video ? "video" : "photo" };
    const extension = kind === "video" ? "mp4" : kind === "thumbnail" && submitted.thumbnail.format !== "jpeg" ? submitted.thumbnail.format : "jpg";
    return api<{ location: string }>("/api/storage/preview", { method: "POST", body: JSON.stringify({ document: submitted, kind, tweet, media, index: Math.max(0, index), extension }) });
  }, onSuccess: (value, submitted) => { if (current.current === submitted) setLocation(value.location); }, onError: error => toast.error({ message: error.message }) });
  return <div className="storage-preview-row"><Button type="tertiary" icon={<Eye size={16} />} aria-label={`${t(`storage.kind.${kind}`)} ${t("storage.preview")}`} loading={preview.isPending} disabled={preview.isPending} onClick={() => preview.mutate(document)}>{t("storage.preview")}</Button>{location && <output className="storage-path-preview">{location}</output>}</div>;
}

export function StorageManagement() {
  useTranslation();
  const client = useQueryClient();
  const query = useQuery({ queryKey: storageQueryKey, queryFn: ({ signal }) => getStorage(signal) });
  const [draft, setDraft] = useState<StorageDocument | null>(null);
  const currentDraft = useRef(draft); currentDraft.current = draft;
  const [dirty, setDirty] = useState(false);
  const [pickingDirectory, setPickingDirectory] = useState(false);
  const tweets = useQuery({ queryKey: ["storage-preview-tweets"], queryFn: ({ signal }) => getTweets(new URLSearchParams({ pageSize: "1" }), signal) });
  useEffect(() => { if (query.data && !dirty) setDraft(query.data); }, [query.data, dirty]);
  function update(next: StorageDocument) { setDraft({ ...next, enabled: true }); setDirty(true); }
  const save = useMutation({ mutationFn: saveStorage, onSuccess: (document, submitted) => {
    client.setQueryData(storageQueryKey, document);
    const edited = currentDraft.current !== submitted;
    setDraft(current => edited && current ? { ...current, version: document.version, targets: current.targets.map(target => ({ ...target, version: document.targets.find(saved => saved.id === target.id)?.version ?? target.version })) } : document);
    setDirty(edited); toast.success({ message: t("storage.saved") });
  }, onError: error => toast.error({ message: error.message }) });
  const migration = useMutation({ mutationFn: migrateStorage, onSuccess: () => { void client.invalidateQueries({ queryKey: ["jobs"] }); toast.success({ message: t("storage.migrationQueued") }); }, onError: error => toast.error({ message: error.message }) });
  if (!draft) return query.isError ? <Alert type="error" message={query.error.message} action={<Button onClick={() => void query.refetch()}>{t("重试")}</Button>} /> : <ListSkeleton rows={3} />;
  const target = draft.targets.find(item => item.id === draft.default.targetId);
  function changeRoot(root: string) {
    update({ ...draft!, targets: draft!.targets.map(item => item.id === target?.id ? { ...item, root } : item) });
  }
  function ruleFields(kind: AssetKind | "default", rule: AssetRule, inherited = false) {
    const change = (patch: Partial<AssetRule>) => update(kind === "default" ? { ...draft!, default: { ...rule, ...patch } } : { ...draft!, rules: { ...draft!.rules, [kind]: { ...rule, ...patch } } });
    const directories = directoryOptions();
    if (!directories.some(item => item.value === rule.directoryTemplate)) directories.push({ value: rule.directoryTemplate, label: t("保留现有分类方式") });
    const ruleTarget = draft!.targets.find(item => item.id === rule.targetId);
    return <>
      {kind !== "default" && <Field label={t("storage.target")} className="storage-media-field"><Select disabled={inherited} ariaLabel={`${t(`storage.kind.${kind}`)} ${t("storage.target")}`} value={rule.targetId} options={draft!.targets.map(item => ({ value: item.id, label: item.name || item.id }))} onChange={targetId => change({ targetId })} /><span className="storage-target-path">{ruleTarget?.type === "s3" ? `s3://${ruleTarget.bucket ?? ""}` : ruleTarget?.root}</span></Field>}
      <Field label={t("分类存储策略")} className={kind !== "default" ? "storage-media-field" : undefined}>
        <Select disabled={inherited} ariaLabel={kind === "default" ? t("分类存储策略") : `${t(`storage.kind.${kind}`)} ${t("分类存储策略")}`} value={rule.directoryTemplate} options={directories} onChange={directoryTemplate => change({ directoryTemplate, directoryTemplateSet: true })} />
        {rule.prefix && <span className="storage-target-path">{t("storage.prefix")}: {rule.prefix}</span>}
        <div className="storage-folder-toggle"><Switch disabled={inherited} checked={Boolean(rule.separateFolder || rule.directoryTemplate.includes("{kind}"))} ariaLabel={`${kind === "default" ? "" : `${t(`storage.kind.${kind}`)} `}${t("按媒体类型分文件夹")}`} onChange={separateFolder => change({
          separateFolder,
          ...(!separateFolder && rule.directoryTemplate.includes("{kind}") ? { directoryTemplate: rule.directoryTemplate.replaceAll("{kind}", "").split("/").filter(Boolean).join("/"), directoryTemplateSet: true } : {}),
        })} /><span>{t("按媒体类型分文件夹")}</span></div>
      </Field>
      <Field label={t("命名策略")} className={kind !== "default" ? "storage-media-field" : undefined}><FilenameStrategy disabled={inherited} labelPrefix={kind === "default" ? "" : t(`storage.kind.${kind}`)} value={rule.filenameTemplate} onChange={filenameTemplate => change({ filenameTemplate })} /></Field>
    </>;
  }
  return <section className="semi-settings-section storage-settings">
    <div className="settings-section-heading"><h2 className="settings-section-title"><HardDrive size={16} />{t("存储设置")}</h2><div className="flex flex-wrap items-center gap-2">{dirty && <Tag tone="warning">{t("storage.unsaved")}</Tag>}<Button theme="solid" icon={<Save size={16} />} disabled={!dirty || save.isPending} loading={save.isPending} onClick={() => save.mutate(draft)}>{t("保存存储设置")}</Button></div></div>
    <div className="storage-location">
      <h3 className="settings-subsection-title">{t("storage.location")}</h3>
      <div className={`storage-directory-options ${draft.targets.length > 1 ? "storage-has-targets" : ""}`}>
        {draft.targets.length > 1 && <Field label={t("storage.target")}><Select ariaLabel={t("storage.target")} value={draft.default.targetId} options={draft.targets.map(item => ({ value: item.id, label: item.name || item.id }))} onChange={targetId => update({ ...draft, default: { ...draft.default, targetId } })} /></Field>}
        <Field label={t("保存目录")}><div className="flex min-w-0 gap-2"><Input aria-label={t("保存目录")} value={target?.type === "s3" ? `s3://${target.bucket ?? ""}` : target?.root ?? ""} disabled={target?.type === "s3"} onChange={event => changeRoot(event.target.value)} />{target?.type !== "s3" && <Tooltip content={t("选择目录")}><Button icon={<FolderOpen size={16} />} aria-label={t("选择目录")} onClick={() => setPickingDirectory(true)} /></Tooltip>}</div></Field>
      </div>
    </div>
    <div className="storage-default-rules">
      <h3 className="settings-subsection-title">{t("storage.default")}</h3>
      <div className="storage-policy-options">{ruleFields("default", draft.default)}</div>
      <div className="storage-filename-limit">{t("文件名上限：240 字节（含扩展名）")}</div>
    </div>
    <div className="storage-media-rules">
      <h3 className="settings-subsection-title">{t("按媒体类型单独设置")}</h3>
      <div className="storage-media-heading" aria-hidden="true"><span>{t("媒体类型")}</span><span>{t("单独设置")}</span><span>{t("storage.target")}</span><span>{t("分类存储策略")}</span><span>{t("命名策略")}</span></div>
      {kinds.map(kind => <div className="storage-media-row" role="group" aria-label={t(`storage.kind.${kind}`)} key={kind}>
        <span className="storage-media-name">{t(`storage.kind.${kind}`)}</span>
        <div className="storage-media-mode"><Switch checked={Boolean(draft.rules[kind])} ariaLabel={`${t(`storage.kind.${kind}`)} ${t("单独设置")}`} onChange={custom => { const rules = { ...draft.rules }; if (custom) rules[kind] = { ...draft.default }; else delete rules[kind]; update({ ...draft, rules }); }} /><span>{t(draft.rules[kind] ? "单独设置" : "使用默认")}</span></div>
        {ruleFields(kind, { ...(draft.rules[kind] ?? draft.default), separateFolder: draft.rules[kind]?.separateFolder ?? draft.default.separateFolder }, !draft.rules[kind])}
        <StoragePathPreview document={draft} kind={kind} tweet={tweets.data?.items[0]} />
      </div>)}
    </div>
    <div className="storage-migration-row"><div><h3 className="settings-subsection-title">{t("历史文件整理")}</h3><p className="text-xs text-fg-muted">{t("storage.sourceRetained")}</p></div><Popconfirm title={t("storage.migrateConfirm")} onConfirm={() => migration.mutate()}><Button disabled={!draft.enabled || dirty || migration.isPending} loading={migration.isPending} icon={<RefreshCw size={16} />}>{t("storage.migrate")}</Button></Popconfirm></div>
    <Modal open={pickingDirectory} onClose={() => setPickingDirectory(false)} title={t("选择目录")} footer={null}><LocalDirectoryPicker path={target?.root ?? ""} onSelect={root => { changeRoot(root); setPickingDirectory(false); }} /></Modal>
  </section>;
}
