import React from "react";
import type { AppConfig } from "../lib/api";
import { ConfigForm } from "../components/settings/ConfigForm";

export interface SettingsPageProps {
  config: AppConfig;
  onRefresh?: () => void;
  refreshPending?: boolean;
}

export function SettingsPage({
  config,
  onRefresh,
  refreshPending = false,
}: SettingsPageProps) {
  return (
    <div className="space-y-6">
      <ConfigForm
        config={config}
        onRefresh={onRefresh}
        refreshPending={refreshPending}
      />
    </div>
  );
}
