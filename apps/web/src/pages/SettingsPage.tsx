import React from "react";
import type { AppConfig } from "../lib/api";
import { ConfigForm } from "../components/settings/ConfigForm";
import { AccountSecurity } from "../components/settings/AccountSecurity";
import { StorageManagement } from "../components/settings/StorageManagement";

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
    <div className="workspace-page">
      <ConfigForm
        config={config}
        onRefresh={onRefresh}
        refreshPending={refreshPending}
      />
      <StorageManagement />
      <AccountSecurity />
    </div>
  );
}
