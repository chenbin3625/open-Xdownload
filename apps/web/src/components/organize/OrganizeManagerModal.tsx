import React, { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { FolderPlus, Plus, Tag as TagIcon, Trash2 } from "lucide-react";
import Button from "@douyinfe/semi-ui/lib/es/button";
import {
  collectionsQueryRoot,
  createCollection,
  createTag,
  deleteCollection,
  deleteTag,
  getCollections,
  getTags,
  tagsQueryRoot,
  type CollectionRecord,
  type TagRecord,
} from "../../lib/api";
import { t, useTranslation } from "../../lib/i18n";
import { notifyError } from "../common/CommonUI";
import { Field, Input, Select, Tabs } from "../semi/controls";
import { Tag } from "../semi/data";
import { Modal, Popconfirm } from "../semi/overlays";
import { toast } from "../semi/toast";

const TAG_COLORS = [
  "blue",
  "cyan",
  "green",
  "amber",
  "orange",
  "red",
  "pink",
  "purple",
  "indigo",
  "teal",
] as const;

export function safeTagList(data: unknown): TagRecord[] {
  if (!Array.isArray(data)) return [];
  return data.filter((item): item is TagRecord => Boolean(item && typeof item === "object" && typeof (item as TagRecord).name === "string"));
}

export function safeCollectionList(data: unknown): CollectionRecord[] {
  if (!Array.isArray(data)) return [];
  return data.filter((item): item is CollectionRecord => Boolean(item && typeof item === "object" && typeof (item as CollectionRecord).name === "string"));
}

export function OrganizeManagerModal({
  open,
  onClose,
}: {
  open: boolean;
  onClose: () => void;
}) {
  useTranslation();
  const queryClient = useQueryClient();
  const [activeTab, setActiveTab] = useState("tags");
  const [tagName, setTagName] = useState("");
  const [tagColor, setTagColor] = useState("blue");
  const [colName, setColName] = useState("");
  const [colDesc, setColDesc] = useState("");

  const tagsQuery = useQuery({
    queryKey: tagsQueryRoot,
    queryFn: ({ signal }) => getTags(signal),
    enabled: open,
  });
  const collectionsQuery = useQuery({
    queryKey: collectionsQueryRoot,
    queryFn: ({ signal }) => getCollections(signal),
    enabled: open,
  });

  const tags = safeTagList(tagsQuery.data);
  const collections = safeCollectionList(collectionsQuery.data);

  const addTag = useMutation({
    mutationFn: () => createTag(tagName.trim(), tagColor),
    onSuccess: () => {
      setTagName("");
      void queryClient.invalidateQueries({ queryKey: tagsQueryRoot });
      toast.success({ message: t("标签已创建") });
    },
    onError: notifyError(t("创建失败")),
  });

  const removeTag = useMutation({
    mutationFn: deleteTag,
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: tagsQueryRoot });
      toast.success({ message: t("标签已删除") });
    },
    onError: notifyError(t("删除失败")),
  });

  const addCollection = useMutation({
    mutationFn: () => createCollection(colName.trim(), colDesc.trim()),
    onSuccess: () => {
      setColName("");
      setColDesc("");
      void queryClient.invalidateQueries({ queryKey: collectionsQueryRoot });
      toast.success({ message: t("合集已创建") });
    },
    onError: notifyError(t("创建失败")),
  });

  const removeCollection = useMutation({
    mutationFn: deleteCollection,
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: collectionsQueryRoot });
      toast.success({ message: t("合集已删除") });
    },
    onError: notifyError(t("删除失败")),
  });

  return (
    <Modal open={open} onClose={onClose} title={t("标签与合集管理")} width="min(92vw, 38rem)">
      <Tabs
        value={activeTab}
        onChange={setActiveTab}
        items={[
          {
            key: "tags",
            label: (
              <span className="flex items-center gap-1.5">
                <TagIcon className="size-3.5" />
                <span>{t("标签")} ({tags.length})</span>
              </span>
            ),
            children: (
              <div className="space-y-4 pt-2">
                <div className="flex flex-wrap items-end gap-2">
                  <Field label={t("新标签名称")} className="min-w-0 flex-1">
                    <Input
                      value={tagName}
                      onChange={(e) => setTagName(e.target.value)}
                      placeholder={t("例如：插画、待阅读、灵感")}
                      onPressEnter={() => { if (tagName.trim() && !addTag.isPending) addTag.mutate(); }}
                    />
                  </Field>
                  <Field label={t("颜色")}>
                    <Select
                      ariaLabel={t("标签颜色")}
                      value={tagColor}
                      onChange={setTagColor}
                      options={TAG_COLORS.map((c) => ({ value: c, label: c }))}
                    />
                  </Field>
                  <Button
                    theme="solid"
                    icon={<Plus className="size-4" />}
                    disabled={!tagName.trim() || addTag.isPending}
                    loading={addTag.isPending}
                    onClick={() => addTag.mutate()}
                  >
                    {t("新建标签")}
                  </Button>
                </div>

                {tags.length === 0 ? (
                  <p className="py-6 text-center text-xs text-fg-muted">{t("暂无自定义标签")}</p>
                ) : (
                  <div className="divide-y divide-line rounded-card border border-line">
                    {tags.map((item) => (
                      <div key={item.id} className="flex items-center justify-between gap-2 px-3 py-2">
                        <Tag>{item.name}</Tag>
                        <Popconfirm
                          title={t("删除标签")}
                          description={t("确定删除标签 “{{value0}}”？", { value0: item.name })}
                          danger
                          onConfirm={() => removeTag.mutate(item.id)}
                        >
                          <Button
                            size="small"
                            theme="borderless"
                            type="danger"
                            icon={<Trash2 className="size-4" />}
                            aria-label={`${t("删除标签")} ${item.name}`}
                          />
                        </Popconfirm>
                      </div>
                    ))}
                  </div>
                )}
              </div>
            ),
          },
          {
            key: "collections",
            label: (
              <span className="flex items-center gap-1.5">
                <FolderPlus className="size-3.5" />
                <span>{t("合集")} ({collections.length})</span>
              </span>
            ),
            children: (
              <div className="space-y-4 pt-2">
                <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
                  <Field label={t("新合集名称")}>
                    <Input
                      value={colName}
                      onChange={(e) => setColName(e.target.value)}
                      placeholder={t("例如：年度精选、壁纸素材")}
                    />
                  </Field>
                  <Field label={t("合集说明 (可选)")}>
                    <Input
                      value={colDesc}
                      onChange={(e) => setColDesc(e.target.value)}
                      placeholder={t("简短备注")}
                    />
                  </Field>
                </div>
                <div className="flex justify-end">
                  <Button
                    theme="solid"
                    icon={<Plus className="size-4" />}
                    disabled={!colName.trim() || addCollection.isPending}
                    loading={addCollection.isPending}
                    onClick={() => addCollection.mutate()}
                  >
                    {t("新建合集")}
                  </Button>
                </div>

                {collections.length === 0 ? (
                  <p className="py-6 text-center text-xs text-fg-muted">{t("暂无自定义合集")}</p>
                ) : (
                  <div className="divide-y divide-line rounded-card border border-line">
                    {collections.map((col) => (
                      <div key={col.id} className="flex items-center justify-between gap-2 px-3 py-2.5">
                        <div className="min-w-0">
                          <div className="truncate text-sm font-semibold text-fg">{col.name}</div>
                          <div className="text-xs text-fg-muted">
                            {col.description ? `${col.description} · ` : ""}
                            {t("{{value0}} 条推文 · {{value1}} 个媒体", { value0: col.tweetCount, value1: col.downloadCount })}
                          </div>
                        </div>
                        <Popconfirm
                          title={t("删除合集")}
                          description={t("确定删除合集 “{{value0}}”？原推文与文件不受影响。", { value0: col.name })}
                          danger
                          onConfirm={() => removeCollection.mutate(col.id)}
                        >
                          <Button
                            size="small"
                            theme="borderless"
                            type="danger"
                            icon={<Trash2 className="size-4" />}
                            aria-label={`${t("删除合集")} ${col.name}`}
                          />
                        </Popconfirm>
                      </div>
                    ))}
                  </div>
                )}
              </div>
            ),
          },
        ]}
      />
    </Modal>
  );
}
