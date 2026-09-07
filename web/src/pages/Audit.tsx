import { useState } from 'react';
import { Button, Card, Space, Table, Tag, Typography } from 'antd';
import { ReloadOutlined } from '@ant-design/icons';
import { useQuery } from '@tanstack/react-query';
import { auditApi, type AuditEntry } from '../api';
import { fmtTime } from '../util';

const methodColor: Record<string, string> = { POST: 'blue', PUT: 'geekblue', PATCH: 'purple', DELETE: 'red' };

// 写操作审计：谁（令牌）在何时对哪个接口做了什么、结果如何。按 ID 倒序，"更早"按游标翻页。
export default function Audit() {
  const [before, setBefore] = useState<number | undefined>();
  const [stack, setStack] = useState<number[]>([]);
  const list = useQuery({ queryKey: ['audit', before], queryFn: () => auditApi.list(before, 100), refetchInterval: before ? false : 10_000 });
  const rows = list.data ?? [];
  const last = rows[rows.length - 1];

  return (
    <Card
      title="审计日志"
      extra={
        <Space>
          <Typography.Text type="secondary">只记录 /api 下的写操作（POST / PUT / PATCH / DELETE），不含请求体</Typography.Text>
          <Button icon={<ReloadOutlined />} onClick={() => list.refetch()} loading={list.isFetching}>刷新</Button>
        </Space>
      }
    >
      <Table<AuditEntry>
        rowKey="id"
        size="small"
        loading={list.isLoading}
        dataSource={rows}
        scroll={{ x: 'max-content' }}
        pagination={false}
        columns={[
          { title: 'ID', dataIndex: 'id', width: 80 },
          { title: '时间', width: 170, render: (_, e) => fmtTime(e.at) },
          { title: '令牌', width: 140, render: (_, e) => (e.tokenId ? `${e.tokenName} (#${e.tokenId})` : <Tag>{e.tokenName}</Tag>) },
          { title: '方法', width: 90, render: (_, e) => <Tag color={methodColor[e.method] ?? 'default'}>{e.method}</Tag> },
          { title: '路径', render: (_, e) => <Typography.Text code>{e.path}</Typography.Text> },
          { title: '状态', width: 80, render: (_, e) => <Tag color={e.status < 300 ? 'success' : e.status < 500 ? 'warning' : 'error'}>{e.status}</Tag> },
          { title: '来源 IP', dataIndex: 'remoteIp', width: 140 },
          { title: '耗时', width: 90, render: (_, e) => `${e.durationMs} ms` },
        ]}
      />
      <Space style={{ marginTop: 12 }}>
        <Button
          disabled={stack.length === 0}
          onClick={() => {
            const s = [...stack];
            const prev = s.pop();
            setStack(s);
            setBefore(prev === 0 ? undefined : prev);
          }}
        >
          更新
        </Button>
        <Button
          disabled={rows.length < 100 || !last}
          onClick={() => {
            setStack([...stack, before ?? 0]);
            setBefore(last!.id);
          }}
        >
          更早
        </Button>
      </Space>
    </Card>
  );
}
