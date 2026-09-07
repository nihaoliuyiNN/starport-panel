import { useEffect, useState } from 'react';
import { App, Button, Drawer, Input, Space, Typography } from 'antd';
import { CopyOutlined, ReloadOutlined, SaveOutlined } from '@ant-design/icons';
import { errMsg, k8sApi } from '../api';
import { copyText } from '../util';

export interface YamlTarget {
  group: string; // core 组传 ''
  version: string;
  resource: string; // 复数资源名，如 pods / deployments
  kind?: string;
  namespace?: string;
  name: string;
}

interface Props {
  cid: number;
  target: YamlTarget | null;
  onClose: () => void;
  onApplied?: () => void;
}

/** 通用资源 YAML 抽屉：读取 → 编辑 → server-side apply。同一份编辑器复用于 Pod / Deployment / 任意资源。 */
export default function YamlDrawer({ cid, target, onClose, onApplied }: Props) {
  const { message } = App.useApp();
  const api = k8sApi(cid);
  const [orig, setOrig] = useState('');
  const [text, setText] = useState('');
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);

  const load = async () => {
    if (!target) return;
    setLoading(true);
    try {
      const y = await api.getResource(target.group, target.version, target.resource, target.name, target.namespace);
      setOrig(y);
      setText(y);
    } catch (e) {
      message.error(errMsg(e));
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    setOrig('');
    setText('');
    void load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [target?.group, target?.version, target?.resource, target?.namespace, target?.name]);

  const apply = async () => {
    if (!target) return;
    setSaving(true);
    try {
      const res = await api.apply(text, target.namespace);
      message.success(`已应用 ${res.applied.map((a) => `${a.kind}/${a.name}`).join(', ')}`);
      onApplied?.();
      await load();
    } catch (e) {
      message.error(errMsg(e));
    } finally {
      setSaving(false);
    }
  };

  const dirty = text !== orig;
  const title = target ? `${target.kind ?? target.resource} ${target.namespace ? `${target.namespace}/` : ''}${target.name}` : '';

  return (
    <Drawer
      open={!!target}
      onClose={onClose}
      width={860}
      title={title}
      extra={
        <Space>
          <Button size="small" icon={<ReloadOutlined />} onClick={load} loading={loading}>重新读取</Button>
          <Button size="small" icon={<CopyOutlined />} onClick={async () => { if (await copyText(text)) message.success('已复制'); }}>复制</Button>
          <Button size="small" type="primary" icon={<SaveOutlined />} onClick={apply} loading={saving} disabled={!dirty}>应用</Button>
        </Space>
      }
    >
      <Typography.Text type="secondary" style={{ fontSize: 12 }}>
        直接编辑后点「应用」= server-side apply（字段管理者 starport-panel）。status / managedFields 等只读字段会被 apiserver 忽略；改 Pod 的大部分 spec 字段会被拒绝，属正常。
      </Typography.Text>
      <Input.TextArea
        value={text}
        onChange={(e) => setText(e.target.value)}
        autoSize={{ minRows: 24, maxRows: 40 }}
        spellCheck={false}
        style={{ fontFamily: 'monospace', fontSize: 12, lineHeight: 1.5, marginTop: 8 }}
        disabled={loading}
      />
    </Drawer>
  );
}
