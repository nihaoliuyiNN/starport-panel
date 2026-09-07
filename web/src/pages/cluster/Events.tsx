import { useState } from 'react';
import { Alert, Button, Input, Space, Table, Tag } from 'antd';
import { ReloadOutlined } from '@ant-design/icons';
import { useQuery } from '@tanstack/react-query';
import { errMsg, k8sApi, type K8sEvent } from '../../api';
import NamespaceSelect from './NamespaceSelect';
import { fromNow } from '../../util';

export default function Events({ cid }: { cid: number }) {
  const [ns, setNs] = useState('');
  const [object, setObject] = useState('');
  const [warnOnly, setWarnOnly] = useState(false);
  const api = k8sApi(cid);
  const q = useQuery({ queryKey: ['k8s', cid, 'events', ns, object], queryFn: () => api.events(ns, object), refetchInterval: 10000 });
  if (q.isError) return <Alert type="error" message={errMsg(q.error)} />;
  const data = (q.data ?? []).filter((e) => !warnOnly || e.type === 'Warning');
  return (
    <>
      <Space style={{ marginBottom: 12 }} wrap>
        <NamespaceSelect cid={cid} value={ns} onChange={setNs} />
        <Input.Search placeholder="对象过滤，如 Pod/nginx-abc" allowClear style={{ width: 260 }} onSearch={setObject} />
        <Button type={warnOnly ? 'primary' : 'default'} onClick={() => setWarnOnly(!warnOnly)}>只看 Warning</Button>
        <Button icon={<ReloadOutlined />} onClick={() => q.refetch()} loading={q.isFetching}>刷新</Button>
      </Space>
      <Table<K8sEvent>
        rowKey={(e, i) => `${e.object}-${e.reason}-${e.lastSeen}-${i}`}
        size="small"
        loading={q.isLoading}
        dataSource={data}
        scroll={{ x: 'max-content' }}
        pagination={{ pageSize: 50, hideOnSinglePage: true }}
        columns={[
          { title: '类型', width: 90, render: (_, e) => <Tag color={e.type === 'Warning' ? 'warning' : 'default'}>{e.type}</Tag> },
          { title: '原因', dataIndex: 'reason', width: 160 },
          { title: '对象', dataIndex: 'object', width: 260, render: (v: string) => <span style={{ fontFamily: 'monospace', fontSize: 12 }}>{v}</span> },
          { title: '消息', dataIndex: 'message' },
          { title: '次数', dataIndex: 'count', width: 60 },
          { title: '最近', width: 110, render: (_, e) => fromNow(e.lastSeen) },
        ]}
      />
    </>
  );
}
