import React from "react";

export function SettingsField({
  children,
  hint,
  label,
}: {
  children: React.ReactNode;
  hint?: string;
  label: string;
}) {
  return (
    <label className="settings-field">
      <span className="settings-field-label">
        {label}
        {hint ? (
          <abbr className="settings-field-tip" title={hint}>
            ?
          </abbr>
        ) : null}
      </span>
      {children}
    </label>
  );
}
export function SettingsSwitch({
  checked,
  hint,
  label,
  onChange,
}: {
  checked: boolean;
  hint?: string;
  label: string;
  onChange: (checked: boolean) => void;
}) {
  return (
    <label className="settings-field settings-switch-field">
      <span className="settings-field-label">
        {label}
        {hint ? (
          <abbr className="settings-field-tip" title={hint}>
            ?
          </abbr>
        ) : null}
      </span>
      <input type="checkbox" checked={checked} onChange={(event) => onChange(event.target.checked)} />
    </label>
  );
}
