import { Col, Form, Input, InputNumber, Row } from "antd";
import React from "react";
import type { AppConfig } from "../../lib/api";
import { fullWidthStyle } from "../common/CommonUI";

export const smbTips = {
  smbHost: "SMB 服务器地址，可填写 IP 或主机名，不要包含 smb:// 前缀。",
  smbPort: "SMB 默认端口通常为 445。",
  smbShare: "共享名是服务器上暴露的共享根名称，不是完整路径。",
  smbPath: "共享名下的保存目录，留空表示保存到共享根目录。",
  smbDomain: "多数家庭 NAS 可留空；企业域或工作组环境按实际要求填写。",
  remoteUsername: "远程存储账号用户名，留空时按匿名或服务端默认权限尝试。",
  savedSecret: "敏感字段读取时可能显示为 ********；保持不变或留空不会覆盖已有值。",
};

export function SMBStorageFields({
  draft,
  onChange,
}: {
  draft: AppConfig;
  onChange: React.Dispatch<React.SetStateAction<AppConfig>>;
}) {
  return (
    <Row gutter={[16, 0]}>
      <Col xs={24} md={12}>
        <Form.Item label="主机" tooltip={smbTips.smbHost}>
          <Input
            value={draft.smbHost}
            onChange={(event) => onChange((current) => ({ ...current, smbHost: event.target.value }))}
            placeholder="192.168.1.10"
          />
        </Form.Item>
      </Col>
      <Col xs={24} md={12}>
        <Form.Item label="端口" tooltip={smbTips.smbPort}>
          <InputNumber
            min={1}
            max={65535}
            value={draft.smbPort}
            onChange={(value) => onChange((current) => ({ ...current, smbPort: value ?? 445 }))}
            style={fullWidthStyle}
          />
        </Form.Item>
      </Col>
      <Col xs={24} md={12}>
        <Form.Item label="共享名" tooltip={smbTips.smbShare}>
          <Input
            value={draft.smbShare}
            onChange={(event) => onChange((current) => ({ ...current, smbShare: event.target.value }))}
            placeholder="downloads"
          />
        </Form.Item>
      </Col>
      <Col xs={24} md={12}>
        <Form.Item label="目录" tooltip={smbTips.smbPath}>
          <Input
            value={draft.smbPath}
            onChange={(event) => onChange((current) => ({ ...current, smbPath: event.target.value }))}
            placeholder="x-media"
          />
        </Form.Item>
      </Col>
      <Col xs={24} md={12}>
        <Form.Item label="域" tooltip={smbTips.smbDomain}>
          <Input
            value={draft.smbDomain}
            onChange={(event) => onChange((current) => ({ ...current, smbDomain: event.target.value }))}
          />
        </Form.Item>
      </Col>
      <Col xs={24} md={12}>
        <Form.Item label="用户名" tooltip={smbTips.remoteUsername}>
          <Input
            value={draft.smbUsername}
            onChange={(event) => onChange((current) => ({ ...current, smbUsername: event.target.value }))}
          />
        </Form.Item>
      </Col>
      <Col xs={24}>
        <Form.Item label="密码" tooltip={smbTips.savedSecret}>
          <Input.Password
            value={draft.smbPassword ?? ""}
            onChange={(event) => onChange((current) => ({ ...current, smbPassword: event.target.value }))}
          />
        </Form.Item>
      </Col>
    </Row>
  );
}
