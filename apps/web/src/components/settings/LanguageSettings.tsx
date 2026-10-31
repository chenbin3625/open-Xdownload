import { Languages } from "lucide-react";
import { useState } from "react";
import { readLanguagePreference, setLanguagePreference, useTranslation, type LanguagePreference } from "../../lib/i18n";
import { Select } from "../ui/Controls";

export function LanguageSettings() {
  const { t } = useTranslation();
  const [preference, setPreference] = useState(readLanguagePreference);
  return (
    <div className="flex flex-wrap items-center justify-between gap-3 border-b border-line pb-4">
      <label htmlFor="interface-language" className="flex items-center gap-2 text-sm font-medium text-fg">
        <Languages className="size-4 text-fg-muted" />
        {t("language.label")}
      </label>
      <Select id="interface-language" value={preference}
        onChange={(value) => {
          const next = value as LanguagePreference;
          setPreference(next);
          void setLanguagePreference(next);
        }}
        options={[
          { value: "auto", label: t("language.auto") },
          { value: "zh-CN", label: "中文" },
          { value: "en", label: "English" },
        ]}
        className="min-w-40 max-w-full" />
    </div>
  );
}
