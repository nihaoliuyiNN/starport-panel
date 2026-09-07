import { useState } from 'react';
import { App, Button, Card, Form, Input, InputNumber, Modal, Popover, Progress, Space, Table, Typography, Tooltip } from 'antd';
import { CodeOutlined, PlayCircleOutlined, ReloadOutlined } from '@ant-design/icons';
import { useQuery } from '@tanstack/react-query';
import { errMsg, nodesApi, wsUrl, type Node } from '../api';
import { OnlineTag } from '../components/tags';
import TaskLogDrawer from '../components/TaskLogDrawer';
import Terminal from '../components/Terminal';
import { fmtBytes, fromNow } from '../util';

export default function Nodes() {
  const { message } = App.useApp();
  const nodes = useQuery({ queryKey: ['nodes'], queryFn: nodesApi.list, refetchInterval: 5000 });
  const [taskId, setTaskId] = useState<number | null>(null);
  const [execNode, setExecNode] = useState<Node | null>(null);
  const [termNode, setTermNode] = useState<Node | null>(null);
  const [form] = Form.useForm<{ script: string; timeoutSec: number }>();

  const runExec = async () => {
    if (!execNode) return;
    const v = await form.validateFields();
    try {
      const res = await nodesApi.exec(execNode.id, v.script, v.timeoutSec * 1000);
      setExecNode(null);
      form.resetFields();
      setTaskId(res.taskId);
    } catch (e) {
      message.error(errMsg(e));
    }
  };

  return (
    <>
      <Card
        title="节点"
        extra={
          <Space>
            <Popover
              title="纳管新节点"
              trigger="click"
              content={
                <div style={{ maxWidth: 520 }}>
                  <Typography.Paragraph style={{ marginBottom: 6 }}>在目标 Linux 机器上以 root 执行仓库 <code>scripts/install-starport-agent.sh</code>（引导令牌即面板 <code>--bootstrap-token</code>）：</Typography.Paragraph>
                  <pre style={{ background: '#f6f6f6', padding: 8, borderRadius: 4, fontSize: 12, margin: 0 }}>
{`STARPORT_SERVER_URL=${window.location.origin} \\
STARPORT_BOOTSTRAP_TOKEN=<引导令牌> \\
STARPORT_AGENT_BIN_URL=<starport-agent 二进制下载地址> \\
bash install-starport-agent.sh`}
                  </pre>
                  <Typography.Text type="secondary" style={{ fontSize: 12 }}>agent 注册成功后会自动出现在此列表。详见 docs/build.md。</Typography.Text>
                </div>
              }
            >
              <Button>纳管节点</Button>
            </Popover>
            <Button icon={<ReloadOutlined />} onClick={() => nodes.refetch()} loading={nodes.isFetching}>刷新</Button>
          </Space>
        }
      >
        <Table<Node>
          rowKey="id"
          size="middle"
          loading={nodes.isLoading}
          dataSource={nodes.data ?? []}
          scroll={{ x: 'max-content' }}
          pagination={false}
          columns={[
            { title: 'ID', dataIndex: 'id', width: 60 },
            { title: '主机名', render: (_, n) => <Space direction="vertical" size={0}><b>{n.facts.hostname}</b><Typography.Text type="secondary" style={{ fontSize: 12 }}>{n.facts.internalIp}</Typography.Text></Space> },
            { title: '状态', width: 90, render: (_, n) => <OnlineTag online={n.online} /> },
            { title: '系统', render: (_, n) => <Typography.Text style={{ fontSize: 12 }}>{n.facts.os}/{n.facts.arch}{n.facts.kernel ? ` · ${n.facts.kernel}` : ''}</Typography.Text> },
            { title: 'CPU', width: 150, render: (_, n) => <Tooltip title={`${n.facts.cpuCores} 核`}><Progress percent={Math.round(n.facts.cpuUsedPercent)} size="small" format={(p) => `${p}% / ${n.facts.cpuCores}C`} /></Tooltip> },
            { title: '内存', width: 170, render: (_, n) => <Progress percent={Math.round(n.facts.memUsedPercent)} size="small" format={(p) => `${p}% / ${fmtBytes(n.facts.memBytes)}`} /> },
            { title: 'agent', dataIndex: 'agentVersion', width: 90 },
            { title: '最近心跳', width: 110, render: (_, n) => fromNow(n.lastSeenAt) },
            {
              title: '操作', width: 180,
              render: (_, n) => (
                <Space>
                  <Button size="small" icon={<PlayCircleOutlined />} disabled={!n.online} onClick={() => setExecNode(n)}>执行脚本</Button>
                  <Button size="small" icon={<CodeOutlined />} disabled={!n.online} onClick={() => setTermNode(n)}>终端</Button>
                </Space>
              ),
            },
          ]}
        />
      </Card>

      <Modal title={`在 ${execNode?.facts.hostname ?? ''} 上执行脚本`} open={!!execNode} onCancel={() => setExecNode(null)} onOk={runExec} okText="执行" destroyOnClose>
        <Form form={form} layout="vertical" initialValues={{ timeoutSec: 600 }}>
          <Form.Item name="script" label="脚本（bash）" rules={[{ required: true, message: '请输入脚本' }]}>
            <Input.TextArea rows={8} placeholder="uname -a&#10;df -h" style={{ fontFamily: 'monospace' }} />
          </Form.Item>
          <Form.Item name="timeoutSec" label="超时（秒）"><InputNumber min={1} max={86400} /></Form.Item>
        </Form>
      </Modal>

      <Modal title={`终端 · ${termNode?.facts.hostname ?? ''}`} open={!!termNode} onCancel={() => setTermNode(null)} footer={null} width={960} destroyOnClose>
        {termNode && <Terminal url={wsUrl(`/nodes/${termNode.id}/terminal`)} height={520} />}
      </Modal>

      <TaskLogDrawer taskId={taskId} onClose={() => setTaskId(null)} />
    </>
  );
}
