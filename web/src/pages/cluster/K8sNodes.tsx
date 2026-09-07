import { Alert, App, Button, Popconfirm, Progress, Space, Table, Tag, Tooltip, Typography } from 'antd';
import { ReloadOutlined } from '@ant-design/icons';
import { useQuery } from '@tanstack/react-query';
import { ApiError, errMsg, k8sApi, type K8sNode } from '../../api';
import { fmtBytes, fmtTime } from '../../util';

export default function K8sNodes({ cid }: { cid: number }) {
  const { message } = App.useApp();
  const api = k8sApi(cid);
  const q = useQuery({ queryKey: ['k8s', cid, 'nodes'], queryFn: api.nodes, refetchInterval: 10000 });
  // 用量：没装 metrics-server 时后端 404 NO_METRICS_SERVER，列显示为 "-"，不再重试
  const usage = useQuery({
    queryKey: ['k8s', cid, 'node-metrics'],
    queryFn: api.nodeMetrics,
    refetchInterval: 15000,
    retry: (n, e) => !(e instanceof ApiError && e.code === 'NO_METRICS_SERVER') && n < 2,
  });
  const noMetrics = usage.error instanceof ApiError && usage.error.code === 'NO_METRICS_SERVER';
  const usageOf = (name: string) => usage.data?.find((u) => u.name === name);

  const toggle = async (n: K8sNode) => {
    try {
      if (n.unschedulable) await api.uncordon(n.name);
      else await api.cordon(n.name);
      message.success(`${n.name} 已${n.unschedulable ? '恢复调度' : '停止调度'}`);
      q.refetch();
    } catch (e) {
      message.error(errMsg(e));
    }
  };

  if (q.isError) return <Alert type="error" message={errMsg(q.error)} />;
  return (
    <>
      <Space style={{ marginBottom: 12 }}>
        <Button icon={<ReloadOutlined />} onClick={() => { q.refetch(); usage.refetch(); }} loading={q.isFetching}>刷新</Button>
        {noMetrics && <Typography.Text type="secondary">未安装 metrics-server，无实时用量（建集群时勾选 metrics-server 附加组件，或在「应用」里安装）</Typography.Text>}
      </Space>
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
          {
            title: 'CPU 用量', width: 160,
            render: (_, n) => {
              const u = usageOf(n.name);
              if (!u) return <Typography.Text type="secondary">{n.cpu} 核</Typography.Text>;
              return <Tooltip title={`${u.cpuMilli}m / ${u.cpuCapMilli}m`}><Progress percent={u.cpuPercent} size="small" format={(p) => `${p}% / ${n.cpu}C`} /></Tooltip>;
            },
          },
          {
            title: '内存用量', width: 180,
            render: (_, n) => {
              const u = usageOf(n.name);
              if (!u) return <Typography.Text type="secondary">{n.memory}</Typography.Text>;
              return <Tooltip title={`${fmtBytes(u.memBytes)} / ${fmtBytes(u.memCapBytes)}`}><Progress percent={u.memPercent} size="small" format={(p) => `${p}% / ${fmtBytes(u.memCapBytes)}`} /></Tooltip>;
            },
          },
          { title: 'kubelet', dataIndex: 'kubeletVersion', width: 100 },
          { title: '运行时', dataIndex: 'containerRuntime' },
          { title: '系统', dataIndex: 'osImage' },
          { title: 'Pods', dataIndex: 'pods', width: 70 },
          { title: '创建', width: 170, render: (_, n) => fmtTime(n.createdAt) },
          {
            title: '操作', width: 120,
            render: (_, n) => (
              <Popconfirm
                title={n.unschedulable ? '恢复调度（uncordon）？' : '停止调度（cordon）？'}
                description={n.unschedulable ? '新 Pod 可以再调度到该节点。' : '新 Pod 不再调度到该节点；已有 Pod 不受影响（不会驱逐）。'}
                onConfirm={() => toggle(n)}
              >
                <Button size="small">{n.unschedulable ? '恢复调度' : '停止调度'}</Button>
              </Popconfirm>
            ),
          },
        ]}
      />
    </>
  );
}
