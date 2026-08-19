import { Col, Form, Input, Row } from "antd";
import React from "react";
import type { AppConfig } from "../../lib/api";

export const webdavTips = {
  webdavUrl: "WebDAV 服务根地址，例如 https://example.com/dav。",
  webdavPath: "WebDAV 根地址下的保存目录，留空表示保存到根目录。",
  remoteUsername: "远程存储账号用户名，留空时按匿名或服务端默认权限尝试。",
  savedSecret: "敏感字段读取时可能显示为 ********；保持不变或留空不会覆盖已有值。",
};

export function WebDAVStorageFields({
  draft,
  onChange,
}: {
  draft: AppConfig;
  onChange: React.Dispatch<React.SetStateAction<AppConfig>>;
}) {
  return (
    <Row gutter={[16, 0]}>
      <Col xs={24}>
        <Form.Item label="地址" tooltip={webdavTips.webdavUrl}>
          <Input
            value={draft.webdavUrl}
            onChange={(event) => onChange((current) => ({ ...current, webdavUrl: event.target.value }))}
            placeholder="https://example.com/dav"
          />
        </Form.Item>
      </Col>
      <Col xs={24}>
        <Form.Item label="目录" tooltip={webdavTips.webdavPath}>
          <Input
            value={draft.webdavPath}
            onChange={(event) => onChange((current) => ({ ...current, webdavPath: event.target.value }))}
            placeholder="x-media"
          />
        </Form.Item>
      </Col>
      <Col xs={24} md={12}>
        <Form.Item label="用户名" tooltip={webdavTips.remoteUsername}>
          <Input
            value={draft.webdavUsername}
            onChange={(event) => onChange((current) => ({ ...current, webdavUsername: event.target.value }))}
          />
        </Form.Item>
      </Col>
      <Col xs={24} md={12}>
        <Form.Item label="密码" tooltip={webdavTips.savedSecret}>
          <Input.Password
            value={draft.webdavPassword ?? ""}
            onChange={(event) => onChange((current) => ({ ...current, webdavPassword: event.target.value }))}
          />
        </Form.Item>
      </Col>
    </Row>
  );
}
