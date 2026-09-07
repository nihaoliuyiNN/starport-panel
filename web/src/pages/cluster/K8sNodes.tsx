import { Alert, Button, Space, Table, Tag } from 'antd';
import { ReloadOutlined } from '@ant-design/icons';
import { useQuery } from '@tanstack/react-query';
import { errMsg, k8sApi, type K8sNode } from '../../api';
import { fmtTime } from '../../util';

export default function K8sNodes({ cid }: { cid: number }) {
  const api = k8sApi(cid);
  const q = useQuery({ queryKey: ['k8s', cid, 'nodes'], queryFn: api.nodes, refetchInterval: 10000 });
  if (q.isError) return <Alert type="error" message={errMsg(q.error)} />;
  return (
    <>
      <Space style={{ marginBottom: 12 }}><Button icon={<ReloadOutlined />} onClick={() => q.refetch()} loading={q.isFetching}>刷新</Button></Space>
      <Table<K8sNode>
        rowKey="name"
        size="middle"
        loading={q.isLoading}
        dataSource={q.data ?? []}
        scroll={{ x: 'max-content' }}
        pagination={false}
        columns={[
          { title: '名称', dataIndex: 'name', render: (v: string) => <b>{v}</b> },
          { title: '状态', width: 140, render: (_, n) => <Space size={4}>{n.ready ? <Tag color="success">Ready</Tag> : <Tag color="error">NotReady</Tag>}{n.unschedulable && <Tag color="warning">不可调度</Tag>}</Space> },
          { title: '角色', render: (_, n) => n.roles.map((r) => <Tag key={r}>{r}</Tag>) },
          { title: '内网 IP', dataIndex: 'internalIp', width: 130 },
          { title: 'kubelet', dataIndex: 'kubeletVersion', width: 100 },
          { title: '运行时', dataIndex: 'containerRuntime' },
          { title: '系统', dataIndex: 'osImage' },
          { title: 'CPU', dataIndex: 'cpu', width: 70 },
          { title: '内存', dataIndex: 'memory', width: 100 },
          { title: 'Pods', dataIndex: 'pods', width: 70 },
          { title: '创建', width: 170, render: (_, n) => fmtTime(n.createdAt) },
        ]}
      />
    </>
  );
}
