import { t, useTranslation } from "../../lib/i18n";
import { AlertCircle } from "lucide-react";
import React from "react";
import { Drawer } from "../ui/Overlay";
import { Tag } from "../ui/Tag";
import { FailedTweetQueue } from "../jobs/FailedTweetQueue";

export interface FailedTweetDrawerProps {
  open: boolean;
  onClose: () => void;
  total: number;
}

export function FailedTweetDrawer({
  open,
  onClose,
  total,
}: FailedTweetDrawerProps) {
  useTranslation();
  return (
    <Drawer
      open={open}
      onClose={onClose}
      width="44rem"
      title={
        <div className="flex items-center gap-2">
          <AlertCircle className="size-4 text-danger shrink-0" />
          <span className="font-semibold text-fg">{t("失败推文队列")}</span>
          <Tag tone="danger" className="font-mono">
            {total}
          </Tag>
        </div>
      }
    >
      <div className="p-4">
        <FailedTweetQueue total={total} />
      </div>
    </Drawer>
  );
}
