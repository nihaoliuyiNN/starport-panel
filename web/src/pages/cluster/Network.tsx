import { useState } from 'react';
import { App, Alert, Button, Popconfirm, Space, Table, Tabs, Tag, Typography } from 'antd';
import { DeleteOutlined, ReloadOutlined } from '@ant-design/icons';
import { useQuery } from '@tanstack/react-query';
import { errMsg, k8sApi, type Ingress, type Service } from '../../api';
import NamespaceSelect from './NamespaceSelect';
import { fromNow } from '../../util';

export default function Network({ cid }: { cid: number }) {
  const [ns, setNs] = useState('');
  return (
    <Tabs
      tabBarExtraContent={<NamespaceSelect cid={cid} value={ns} onChange={setNs} />}
      items={[
        { key: 'services', label: 'Services', children: <Services cid={cid} ns={ns} /> },
        { key: 'ingresses', label: 'Ingresses', children: <Ingresses cid={cid} ns={ns} /> },
      ]}
    />
  );
}

function Services({ cid, ns }: { cid: number; ns: string }) {
  const { message } = App.useApp();
  const api = k8sApi(cid);
  const q = useQuery({ queryKey: ['k8s', cid, 'services', ns], queryFn: () => api.services(ns), refetchInterval: 10000 });
  const del = async (s: Service) => {
    try {
      await api.deleteService(s.namespace, s.name);
      message.success(`已删除 ${s.name}`);
      q.refetch();
    } catch (e) {
      message.error(errMsg(e));
    }
  };
  if (q.isError) return <Alert type="error" message={errMsg(q.error)} />;
  return (
    <>
      <Space style={{ marginBottom: 12 }}><Button icon={<ReloadOutlined />} onClick={() => q.refetch()} loading={q.isFetching}>刷新</Button></Space>
      <Table<Service>
        rowKey={(s) => `${s.namespace}/${s.name}`}
        size="small"
        loading={q.isLoading}
        dataSource={q.data ?? []}
        scroll={{ x: 'max-content' }}
        pagination={{ pageSize: 50, hideOnSinglePage: true }}
        columns={[
          { title: '命名空间', dataIndex: 'namespace', width: 140 },
          { title: '名称', dataIndex: 'name', render: (v: string) => <b>{v}</b> },
          { title: '类型', dataIndex: 'type', width: 120, render: (v: string) => <Tag>{v}</Tag> },
          { title: 'ClusterIP', dataIndex: 'clusterIp', width: 130 },
          { title: '外部地址', render: (_, s) => (s.externalIps.length ? s.externalIps.join(', ') : '-') },
          { title: '端口', render: (_, s) => s.ports.map((p) => <div key={`${p.port}/${p.protocol}`} style={{ fontSize: 12, fontFamily: 'monospace' }}>{p.port}{p.nodePort ? `:${p.nodePort}` : ''}→{p.targetPort}/{p.protocol}</div>) },
          { title: '选择器', render: (_, s) => Object.entries(s.selector).map(([k, v]) => <Tag key={k} style={{ fontSize: 11 }}>{k}={v}</Tag>) },
          { title: '存活', width: 100, render: (_, s) => fromNow(s.createdAt) },
          {
            title: '', width: 50,
            render: (_, s) => (
              <Popconfirm title="删除 Service？" onConfirm={() => del(s)} okButtonProps={{ danger: true }} disabled={s.name === 'kubernetes' && s.namespace === 'default'}>
                <Button size="small" danger icon={<DeleteOutlined />} disabled={s.name === 'kubernetes' && s.namespace === 'default'} />
              </Popconfirm>
            ),
          },
        ]}
      />
    </>
  );
}

function Ingresses({ cid, ns }: { cid: number; ns: string }) {
  const api = k8sApi(cid);
  const q = useQuery({ queryKey: ['k8s', cid, 'ingresses', ns], queryFn: () => api.ingresses(ns), refetchInterval: 10000 });
  if (q.isError) return <Alert type="error" message={errMsg(q.error)} />;
  return (
    <>
      <Space style={{ marginBottom: 12 }}><Button icon={<ReloadOutlined />} onClick={() => q.refetch()} loading={q.isFetching}>刷新</Button></Space>
      <Table<Ingress>
        rowKey={(i) => `${i.namespace}/${i.name}`}
        size="small"
        loading={q.isLoading}
        dataSource={q.data ?? []}
        scroll={{ x: 'max-content' }}
        pagination={{ pageSize: 50, hideOnSinglePage: true }}
        columns={[
          { title: '命名空间', dataIndex: 'namespace', width: 140 },
          { title: '名称', dataIndex: 'name', render: (v: string) => <b>{v}</b> },
          { title: 'Class', dataIndex: 'class', width: 110 },
          { title: '规则', render: (_, i) => i.rules.map((r, idx) => <div key={idx} style={{ fontSize: 12, fontFamily: 'monospace' }}>{r.host || '*'}{r.path} → {r.service}:{r.port}</div>) },
          { title: 'TLS', render: (_, i) => (i.tlsHosts.length ? i.tlsHosts.map((h) => <Tag key={h} color="green">{h}</Tag>) : <Typography.Text type="secondary">-</Typography.Text>) },
          { title: '地址', render: (_, i) => (i.addresses.length ? i.addresses.join(', ') : '-') },
          { title: '存活', width: 100, render: (_, i) => fromNow(i.createdAt) },
        ]}
      />
    </>
  );
}
