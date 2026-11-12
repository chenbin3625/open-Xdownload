import { Languages } from "lucide-react";
import { useState } from "react";
import { readLanguagePreference, setLanguagePreference, useTranslation, type LanguagePreference } from "../../lib/i18n";
import { Select } from "@douyinfe/semi-ui";

export function LanguageSettings() {
  const { t } = useTranslation();
  const [preference, setPreference] = useState(readLanguagePreference);
  return (
    <div className="flex flex-wrap items-center justify-between gap-3">
      <label id="interface-language-label" htmlFor="interface-language" className="flex items-center gap-2 text-sm font-medium text-fg">
        <Languages className="size-4 text-fg-muted" />
        {t("language.label")}
      </label>
      <Select id="interface-language" value={preference} aria-labelledby="interface-language-label"
        onChange={(value) => {
          const next = value as LanguagePreference;
          setPreference(next);
          void setLanguagePreference(next);
        }}
        optionList={[
          { value: "auto", label: t("language.auto"), showTick: false },
          { value: "zh-CN", label: "中文", showTick: false },
          { value: "en", label: "English", showTick: false },
        ]}
        className="min-w-40 max-w-full" />
    </div>
  );
}
