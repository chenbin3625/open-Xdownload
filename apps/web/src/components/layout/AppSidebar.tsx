import {
  ClockCircleOutlined,
  ExclamationCircleOutlined,
  FolderOpenOutlined,
  PictureOutlined,
  PlusOutlined,
  SettingOutlined,
  UnorderedListOutlined,
} from "@ant-design/icons";
import {
  Badge,
  Button,
  Divider,
  Flex,
  Menu,
  Space,
  Tag,
  Typography,
} from "antd";
import React from "react";
import type { SectionKey } from "../../lib/useRouteState";

const { Text } = Typography;

export interface AppSidebarProps {
  activeSection: SectionKey;
  onSectionChange: (section: SectionKey) => void;
  onOpenCreateModal: () => void;
  onOpenFailedDrawer: () => void;
  totalJobsCount: number;
  activeJobsCount: number;
  schedulesCount: number;
  failedTweetCount: number;
  storageType?: string;
  storagePath?: string;
}

export function AppSidebar({
  activeSection,
  onSectionChange,
  onOpenCreateModal,
  onOpenFailedDrawer,
  totalJobsCount,
  activeJobsCount,
  schedulesCount,
  failedTweetCount,
  storageType = "local",
  storagePath = "/downloads",
}: AppSidebarProps) {
  const currentKey =
    activeSection === "overview" ||
    activeSection === "workbench" ||
    activeSection === "tasks"
      ? "tasks"
      : activeSection;

  // 计数统一用 Tag 呈现，避免同一位置出现多种自绘徽标样式。
  const countTag = (text: React.ReactNode, highlight = false) => (
    <Tag
      color={highlight ? "processing" : undefined}
      style={{ margin: 0, fontSize: 11 }}
      className="font-mono"
    >
      {text}
    </Tag>
  );

  const menuItems = [
    {
      key: "tasks",
      icon: <UnorderedListOutlined />,
      label: (
        <Flex align="center" justify="space-between" gap={8}>
          <span>任务调度中心</span>
          {activeJobsCount > 0
            ? countTag(`${activeJobsCount} 运行`, true)
            : totalJobsCount > 0
              ? countTag(totalJobsCount)
              : null}
        </Flex>
      ),
    },
    {
      key: "schedules",
      icon: <ClockCircleOutlined />,
      label: (
        <Flex align="center" justify="space-between" gap={8}>
          <span>自动归档计划</span>
          {schedulesCount > 0 ? countTag(schedulesCount) : null}
        </Flex>
      ),
    },
    {
      key: "gallery",
      icon: <PictureOutlined />,
      label: "媒体归档库",
    },
    {
      key: "settings",
      icon: <SettingOutlined />,
      label: "系统与存储配置",
    },
  ];

  return (
    <aside className="app-sidebar w-64 shrink-0 flex flex-col justify-between select-none">
      <div style={{ padding: 12 }}>
        {/* 品牌区 */}
        <Flex align="center" gap={10} style={{ padding: "6px 8px 12px" }}>
          <Flex
            align="center"
            justify="center"
            style={{
              width: 34,
              height: 34,
              borderRadius: 10,
              background: "var(--brand-500)",
              color: "#fff",
              fontSize: 16,
              fontWeight: 700,
              flexShrink: 0,
            }}
          >
            𝕏
          </Flex>
          <div style={{ minWidth: 0 }}>
            <Flex align="center" gap={6}>
              <Text strong style={{ fontSize: 14 }}>
                open-Xdownload
              </Text>
              <Tag
                color="processing"
                style={{ margin: 0, fontSize: 10, lineHeight: "16px" }}
                className="font-mono"
              >
                v{__APP_VERSION__}
              </Tag>
            </Flex>
            <Text type="secondary" style={{ fontSize: 11 }}>
              推文多媒体下载控制台
            </Text>
          </div>
        </Flex>

        <Button
          type="primary"
          block
          icon={<PlusOutlined />}
          onClick={onOpenCreateModal}
        >
          新建下载 / 归档
        </Button>

        <Menu
          mode="inline"
          items={menuItems}
          selectedKeys={[currentKey]}
          onClick={({ key }) => onSectionChange(key as SectionKey)}
          style={{ border: "none", marginTop: 12, background: "transparent" }}
        />

        {/* 失败推文入口 */}
        {failedTweetCount > 0 && (
          <>
            <Divider style={{ margin: "12px 0" }} />
            <Flex vertical gap={8}>
              <Flex align="center" justify="space-between">
                <Space size={6}>
                  <ExclamationCircleOutlined style={{ color: "#ef4444" }} />
                  <Text style={{ fontSize: 12 }}>失败推文队列</Text>
                </Space>
                <Badge count={failedTweetCount} overflowCount={999} />
              </Flex>
              <Button danger size="small" block onClick={onOpenFailedDrawer}>
                查看并批量重试
              </Button>
            </Flex>
          </>
        )}
      </div>

      {/* 底部存储状态 */}
      <div
        style={{
          padding: 12,
          borderTop: "1px solid var(--app-border)",
          background: "var(--app-surface-muted)",
        }}
      >
        <Flex align="center" justify="space-between" style={{ marginBottom: 6 }}>
          <Space size={6}>
            <FolderOpenOutlined style={{ color: "var(--text-subtle)" }} />
            <Text type="secondary" style={{ fontSize: 12 }}>
              存储 ({storageType.toUpperCase()})
            </Text>
          </Space>
        </Flex>
        <Text
          type="secondary"
          ellipsis={{ tooltip: storagePath }}
          className="font-mono"
          style={{ fontSize: 11, display: "block" }}
        >
          {storagePath || "/downloads"}
        </Text>
      </div>
    </aside>
  );
}
