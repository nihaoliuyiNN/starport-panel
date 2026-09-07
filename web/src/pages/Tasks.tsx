import { useState } from 'react';
import { Button, Card, Space, Table, Tag, Typography } from 'antd';
import { ReloadOutlined } from '@ant-design/icons';
import { useQuery } from '@tanstack/react-query';
import { Link } from 'react-router-dom';
import { nodesApi, tasksApi, type Task } from '../api';
import { TaskStatusTag } from '../components/tags';
import TaskLogDrawer from '../components/TaskLogDrawer';
import { fmtTime, fromNow } from '../util';

const kindText: Record<string, string> = { install: '装机', exec: '执行脚本', remove: '移除节点', destroy: '删集群重置', upgrade: '升级 agent' };

export default function Tasks() {
  const tasks = useQuery({ queryKey: ['tasks'], queryFn: () => tasksApi.list(undefined, 200), refetchInterval: 5000 });
  const nodes = useQuery({ queryKey: ['nodes'], queryFn: nodesApi.list, staleTime: 30_000 });
  const nodeName = (id: number) => nodes.data?.find((n) => n.id === id)?.facts.hostname ?? `#${id}`;
  const [taskId, setTaskId] = useState<number | null>(null);

  return (
    <>
      <Card title="任务" extra={<Button icon={<ReloadOutlined />} onClick={() => tasks.refetch()} loading={tasks.isFetching}>刷新</Button>}>
        <Table<Task>
          rowKey="id"
          size="middle"
          loading={tasks.isLoading}
          dataSource={tasks.data ?? []}
          scroll={{ x: 'max-content' }}
          pagination={{ pageSize: 30, hideOnSinglePage: true }}
          onRow={(t) => ({ onClick: () => setTaskId(t.id), style: { cursor: 'pointer' } })}
          columns={[
            { title: 'ID', dataIndex: 'id', width: 70 },
            { title: '类型', width: 120, render: (_, t) => <Tag>{kindText[t.kind] ?? t.kind}</Tag> },
            { title: '状态', width: 100, render: (_, t) => <TaskStatusTag status={t.status} /> },
            { title: '节点', render: (_, t) => nodeName(t.nodeId) },
            { title: '集群', width: 100, render: (_, t) => (t.clusterId ? <Link to={`/clusters/${t.clusterId}`} onClick={(e) => e.stopPropagation()}>#{t.clusterId}</Link> : '-') },
            { title: '结果', render: (_, t) => (t.errorCode ? <Space><Tag color="error">{t.errorCode}</Tag><Typography.Text type="secondary" ellipsis style={{ maxWidth: 360 }}>{t.errorMessage}</Typography.Text></Space> : t.status === 'succeeded' ? `exit ${t.exitCode}` : '-') },
            { title: '开始', width: 170, render: (_, t) => fmtTime(t.startedAt) },
            { title: '耗时/结束', width: 120, render: (_, t) => (t.status === 'running' ? fromNow(t.startedAt) : fmtTime(t.finishedAt, 'HH:mm:ss')) },
          ]}
        />
      </Card>
      <TaskLogDrawer taskId={taskId} onClose={() => setTaskId(null)} />
    </>
  );
}
