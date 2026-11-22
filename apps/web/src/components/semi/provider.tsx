import "@douyinfe/semi-ui/react19-adapter";
import ConfigProvider from "@douyinfe/semi-ui/lib/es/configProvider";
import zhCN from "@douyinfe/semi-ui/lib/es/locale/source/zh_CN";
import enUS from "@douyinfe/semi-ui/lib/es/locale/source/en_US";
import React from "react";
import { t, useTranslation } from "../../lib/i18n";

export function TooltipProvider({ children }: { children: React.ReactNode }) {
  const { i18n } = useTranslation();
  return <ConfigProvider locale={i18n.language.startsWith("en") ? enUS : zhCN}>{children}</ConfigProvider>;
}

export { t };
