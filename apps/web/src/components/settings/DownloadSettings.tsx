import { Col, Form, Input, InputNumber, Row, Select, Space, Switch, Typography } from "antd";
import React from "react";
import type { AppConfig, FileNamingMode } from "../../lib/api";
import { fullWidthStyle } from "../common/CommonUI";

export const fileNamingOptions: Array<{ value: FileNamingMode; label: string }> = [
  { value: "user_tweet", label: "用户名 + 用户 ID + 推文" },
  { value: "tweet_text", label: "仅推文" },
];

export const downloadSettingsTips = {
  proxy: (
    <Space orientation="vertical" size={2}>
      <Typography.Text>支持 http、https、socks5、socks5h，例如 http://127.0.0.1:7890。</Typography.Text>
      <Typography.Text>如需账号密码，可写成 socks5://user:password@127.0.0.1:1080。</Typography.Text>
      <Typography.Text>用户名或密码里的 @、:、/、% 需要 URL 编码；包含账号密码时会随配置保存在本地。</Typography.Text>
    </Space>
  ),
  concurrency: "后台同时运行的下载任务数，过高可能触发站点限流或增加远程存储压力。",
  maxFilenameLength: "限制保存到磁盘或远程存储的文件名长度，长推文文件名会自动截断。",
  fileNaming: "影响新下载文件的命名方式，已下载文件不会被重命名。",
  autoRetryFailed: "批量归档结束后，自动再次处理失败推文队列。",
  autoFollowProtected: "遇到未关注的保护账号时，使用已配置 Cookie 尝试发起关注后再归档。",
  includeNestedTweetMedia: "开启后会把引用或转推中的媒体也纳入单条下载和批量归档；关闭时只处理当前推文本体媒体。",
  incrementalArchive: (
    <Space orientation="vertical" size={2}>
      <Typography.Text>开启后重复归档从上次成功位置继续，节省 X API 配额。</Typography.Text>
      <Typography.Text>默认关闭：每次全量扫描时间线，已存在媒体自动跳过，并顺带补齐历史视频缺失的封面。</Typography.Text>
    </Space>
  ),
};

export function DownloadSettingsFields({
  draft,
  onChange,
  onAuthChange,
}: {
  draft: AppConfig;
  onChange: React.Dispatch<React.SetStateAction<AppConfig>>;
  onAuthChange: React.Dispatch<React.SetStateAction<AppConfig>>;
}) {
  return (
    <Row gutter={[16, 0]}>
      <Col xs={24} lg={12}>
        <Form.Item label="代理" tooltip={downloadSettingsTips.proxy}>
          <Input
            value={draft.proxyUrl}
            onChange={(event) => onAuthChange((current) => ({ ...current, proxyUrl: event.target.value }))}
            placeholder="http://127.0.0.1:7890"
          />
        </Form.Item>
      </Col>
      <Col xs={24} sm={12} lg={6}>
        <Form.Item label="并发" tooltip={downloadSettingsTips.concurrency}>
          <InputNumber
            min={1}
            max={64}
            value={draft.maxConcurrency}
            onChange={(value) => onChange((current) => ({ ...current, maxConcurrency: value ?? 1 }))}
            style={fullWidthStyle}
          />
        </Form.Item>
      </Col>
      <Col xs={24} sm={12} lg={6}>
        <Form.Item label="最大文件名长度" tooltip={downloadSettingsTips.maxFilenameLength}>
          <InputNumber
            min={16}
            max={240}
            value={draft.maxFilenameLength}
            onChange={(value) => onChange((current) => ({ ...current, maxFilenameLength: value ?? 120 }))}
            style={fullWidthStyle}
          />
        </Form.Item>
      </Col>
      <Col xs={24} lg={12}>
        <Form.Item label="文件名命名" tooltip={downloadSettingsTips.fileNaming}>
          <Select
            value={draft.fileNamingMode}
            options={fileNamingOptions}
            onChange={(value) => onChange((current) => ({ ...current, fileNamingMode: value }))}
          />
        </Form.Item>
      </Col>
      <Col xs={24} sm={12} lg={6}>
        <Form.Item label="失败重试" tooltip={downloadSettingsTips.autoRetryFailed}>
          <Switch
            checked={draft.autoRetryFailed}
            onChange={(checked) => onChange((current) => ({ ...current, autoRetryFailed: checked }))}
          />
        </Form.Item>
      </Col>
      <Col xs={24} sm={12} lg={6}>
        <Form.Item label="保护账号自动关注" tooltip={downloadSettingsTips.autoFollowProtected}>
          <Switch
            checked={draft.autoFollowProtected}
            onChange={(checked) => onChange((current) => ({ ...current, autoFollowProtected: checked }))}
          />
        </Form.Item>
      </Col>
      <Col xs={24} sm={12} lg={6}>
        <Form.Item label="引用/转推媒体" tooltip={downloadSettingsTips.includeNestedTweetMedia}>
          <Switch
            checked={draft.includeNestedTweetMedia}
            onChange={(checked) => onChange((current) => ({ ...current, includeNestedTweetMedia: checked }))}
          />
        </Form.Item>
      </Col>
      <Col xs={24} sm={12} lg={6}>
        <Form.Item label="增量归档" tooltip={downloadSettingsTips.incrementalArchive}>
          <Switch
            checked={draft.incrementalArchive}
            onChange={(checked) => onChange((current) => ({ ...current, incrementalArchive: checked }))}
          />
        </Form.Item>
      </Col>
    </Row>
  );
}
