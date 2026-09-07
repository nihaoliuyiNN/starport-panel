import { useState } from 'react';
import { App, Alert, Button, Input, Popconfirm, Space, Tag, Typography } from 'antd';
import { ApiError, errMsg, k8sApi, type Applied } from '../../api';
import NamespaceSelect from './NamespaceSelect';

const SAMPLE = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: nginx
spec:
  replicas: 2
  selector:
    matchLabels: { app: nginx }
  template:
    metadata:
      labels: { app: nginx }
    spec:
      containers:
        - name: nginx
          image: nginx:1.27
          ports: [{ containerPort: 80 }]
`;

/** YAML 编辑区 → server-side apply / 删除。 */
export default function ApplyYaml({ cid }: { cid: number }) {
  const { message } = App.useApp();
  const api = k8sApi(cid);
  const [ns, setNs] = useState('default');
  const [yaml, setYaml] = useState(SAMPLE);
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<{ ok: boolean; verb: string; items: Applied[]; error?: string } | null>(null);

  const run = async (verb: 'apply' | 'delete') => {
    setBusy(true);
    setResult(null);
    try {
      if (verb === 'apply') {
        const r = await api.apply(yaml, ns);
        setResult({ ok: true, verb: '已应用', items: r.applied });
      } else {
        const r = await api.deleteManifest(yaml, ns);
        setResult({ ok: true, verb: '已删除', items: r.deleted });
      }
    } catch (e) {
      // 部分成功：响应体里带 applied/deleted + error
      const body = e instanceof ApiError ? (e.body as { applied?: Applied[]; deleted?: Applied[] } | null) : null;
      setResult({ ok: false, verb: verb === 'apply' ? '已应用' : '已删除', items: body?.applied ?? body?.deleted ?? [], error: errMsg(e) });
      message.error(errMsg(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div>
      <Space style={{ marginBottom: 12 }} wrap>
        <span>默认命名空间</span>
        <NamespaceSelect cid={cid} value={ns} onChange={setNs} allowAll={false} />
        <Button type="primary" loading={busy} onClick={() => run('apply')}>Apply（server-side）</Button>
        <Popconfirm title="按此 YAML 删除对象？" okButtonProps={{ danger: true }} onConfirm={() => run('delete')}>
          <Button danger loading={busy}>Delete</Button>
        </Popconfirm>
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>支持多文档（---）；未写 metadata.namespace 的资源落到默认命名空间</Typography.Text>
      </Space>
      <Input.TextArea
        value={yaml}
        onChange={(e) => setYaml(e.target.value)}
        rows={22}
        spellCheck={false}
        style={{ fontFamily: 'Menlo, Consolas, monospace', fontSize: 13, background: '#fafafa' }}
      />
      {result && (
        <Alert
          style={{ marginTop: 12 }}
          type={result.ok ? 'success' : 'error'}
          showIcon
          message={result.ok ? `${result.verb} ${result.items.length} 个对象` : result.error}
          description={
            result.items.length > 0 && (
              <Space wrap>
                {result.items.map((a) => <Tag key={`${a.kind}/${a.namespace}/${a.name}`}>{a.kind} {a.namespace ? `${a.namespace}/` : ''}{a.name}</Tag>)}
                {!result.ok && <Typography.Text type="secondary">（以上为失败前已成功的对象）</Typography.Text>}
              </Space>
            )
          }
        />
      )}
    </div>
  );
}
