import { useState } from 'react';
import { App, Button, Card, Dropdown, Form, Input, InputNumber, Modal, Popover, Progress, Space, Table, Typography, Tooltip } from 'antd';
import { CloudUploadOutlined, CodeOutlined, CopyOutlined, DeleteOutlined, MoreOutlined, PlayCircleOutlined, PlusOutlined, ReloadOutlined } from '@ant-design/icons';
import { useQuery } from '@tanstack/react-query';
import { agentsApi, errMsg, nodesApi, wsUrl, type Node } from '../api';
import { OnlineTag } from '../components/tags';
import TaskLogDrawer from '../components/TaskLogDrawer';
import Terminal from '../components/Terminal';
import { copyText, fmtBytes, fromNow } from '../util';

type UpgradeTarget = { node: Node } | { all: true } | null;

const RELEASES = 'https://github.com/nihaoliuyiNN/starport-panel/releases';

/**
 * 纳管节点的一行命令：脚本与二进制都从 GitHub Release 取；面板是发行版（v*）就钉同版本 agent，dev 构建用 latest。
 * ghProxy 非空时拼在 GitHub 地址前并传给脚本（国内机器拉 Release 慢）。
 */
function enrollCommand(token: string, version: string, ghProxy: string) {
  const pinned = /^v\d/.test(version);
  const script = ghProxy + (pinned ? `${RELEASES}/download/${version}/install-starport-agent.sh` : `${RELEASES}/latest/download/install-starport-agent.sh`);
  const env = [
    `STARPORT_SERVER_URL=${window.location.origin}`,
    `STARPORT_BOOTSTRAP_TOKEN=${token}`,
    ...(pinned ? [`STARPORT_VERSION=${version}`] : []),
    ...(ghProxy ? [`STARPORT_GH_PROXY=${ghProxy}`] : []),
  ];
  return `curl -fsSL ${script} | \\\n  ${env.join(' ')} bash`;
}

export default function Nodes() {
  const { message, modal } = App.useApp();
  const nodes = useQuery({ queryKey: ['nodes'], queryFn: nodesApi.list, refetchInterval: 5000 });
  const enroll = useQuery({ queryKey: ['agents', 'enroll'], queryFn: agentsApi.enroll, staleTime: Infinity });
  const [ghProxy, setGhProxy] = useState('');
  const proxyPrefix = ghProxy.trim() ? ghProxy.trim().replace(/\/?$/, '/') : '';
  const cmd = enroll.data ? enrollCommand(enroll.data.bootstrapToken, enroll.data.version, proxyPrefix) : '';
  const [taskId, setTaskId] = useState<number | null>(null);
  const [execNode, setExecNode] = useState<Node | null>(null);
  const [termNode, setTermNode] = useState<Node | null>(null);
  const [upgrade, setUpgrade] = useState<UpgradeTarget>(null);
  const [form] = Form.useForm<{ script: string; timeoutSec: number }>();
  const [upForm] = Form.useForm<{ binUrl: string; sha256?: string }>();

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

  const runUpgrade = async () => {
    if (!upgrade) return;
    const v = await upForm.validateFields();
    try {
      if ('node' in upgrade) {
        const res = await nodesApi.upgrade(upgrade.node.id, v.binUrl, v.sha256 || undefined);
        setTaskId(res.taskId);
      } else {
        const res = await nodesApi.upgradeAll(v.binUrl, v.sha256 || undefined);
        const n = Object.keys(res.tasks).length;
        message.success(`已向 ${n} 个在线节点下发升级任务${res.skipped.length ? `，跳过 ${res.skipped.length} 个` : ''}；进度见「任务」页`);
      }
      setUpgrade(null);
    } catch (e) {
      message.error(errMsg(e));
    }
  };

  const removeNode = async (n: Node) => {
    try {
      await nodesApi.remove(n.id);
      message.success(`已删除节点 ${n.facts.hostname}`);
      nodes.refetch();
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
                <div style={{ maxWidth: 640 }}>
                  <Typography.Paragraph style={{ marginBottom: 6 }}>在目标 Linux 机器（amd64 / arm64）上以 root 执行，几秒后它会出现在列表里：</Typography.Paragraph>
                  <Input
                    size="small"
                    allowClear
                    value={ghProxy}
                    onChange={(e) => setGhProxy(e.target.value)}
                    placeholder="GitHub 加速前缀（可选，如 https://ghfast.top/；节点在国内、拉 Release 慢时填）"
                    style={{ marginBottom: 6 }}
                  />
                  <pre style={{ background: '#f6f6f6', padding: 8, borderRadius: 4, fontSize: 12, margin: 0, whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}>{cmd || (enroll.isError ? errMsg(enroll.error) : '加载中…')}</pre>
                  <Space style={{ marginTop: 8 }}>
                    <Button size="small" icon={<CopyOutlined />} disabled={!cmd} onClick={async () => { if (await copyText(cmd)) message.success('已复制'); }}>复制命令</Button>
                    <Typography.Text type="secondary" style={{ fontSize: 12 }}>节点需能访问本面板的 {window.location.host} 与 gRPC 端口（默认 9192）。</Typography.Text>
                  </Space>
                </div>
              }
            >
              <Button type="primary" icon={<PlusOutlined />}>纳管节点</Button>
            </Popover>
            <Button icon={<CloudUploadOutlined />} onClick={() => setUpgrade({ all: true })} disabled={!nodes.data?.some((n) => n.online)}>升级全部 agent</Button>
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
            {
              title: '主机名',
              render: (_, n) => (
                <Tooltip title={n.facts.machineId ? `machine-id ${n.facts.machineId}` : undefined}>
                  <Space direction="vertical" size={0}><b>{n.facts.hostname}</b><Typography.Text type="secondary" style={{ fontSize: 12 }}>{n.facts.internalIp}</Typography.Text></Space>
                </Tooltip>
              ),
            },
            { title: '状态', width: 90, render: (_, n) => <OnlineTag online={n.online} /> },
            { title: '系统', render: (_, n) => <Typography.Text style={{ fontSize: 12 }}>{n.facts.os}/{n.facts.arch}{n.facts.kernel ? ` · ${n.facts.kernel}` : ''}</Typography.Text> },
            { title: 'CPU', width: 150, render: (_, n) => <Tooltip title={`${n.facts.cpuCores} 核`}><Progress percent={Math.round(n.facts.cpuUsedPercent)} size="small" format={(p) => `${p}% / ${n.facts.cpuCores}C`} /></Tooltip> },
            { title: '内存', width: 170, render: (_, n) => <Progress percent={Math.round(n.facts.memUsedPercent)} size="small" format={(p) => `${p}% / ${fmtBytes(n.facts.memBytes)}`} /> },
            { title: 'agent', dataIndex: 'agentVersion', width: 90 },
            { title: '最近心跳', width: 110, render: (_, n) => fromNow(n.lastSeenAt) },
            {
              title: '操作', width: 220,
              render: (_, n) => (
                <Space>
                  <Button size="small" icon={<PlayCircleOutlined />} disabled={!n.online} onClick={() => setExecNode(n)}>执行脚本</Button>
                  <Button size="small" icon={<CodeOutlined />} disabled={!n.online} onClick={() => setTermNode(n)}>终端</Button>
                  <Dropdown
                    menu={{
                      items: [
                        { key: 'upgrade', icon: <CloudUploadOutlined />, label: '升级 agent', disabled: !n.online, onClick: () => setUpgrade({ node: n }) },
                        { type: 'divider' },
                        {
                          key: 'delete', icon: <DeleteOutlined />, label: '删除节点记录', danger: true,
                          onClick: () =>
                            modal.confirm({
                              title: `删除节点 ${n.facts.hostname}？`,
                              content: '只删除面板里的记录并断开连接；节点若仍属于某个集群会被拒绝。机器上的 agent 服务需自行停掉（否则会重新注册成新节点）。',
                              okText: '删除', okButtonProps: { danger: true },
                              onOk: () => removeNode(n),
                            }),
                        },
                      ],
                    }}
                  >
                    <Button size="small" icon={<MoreOutlined />} />
                  </Dropdown>
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

      <Modal
        title={upgrade && 'node' in upgrade ? `升级 ${upgrade.node.facts.hostname} 的 agent` : '升级全部在线节点的 agent'}
        open={!!upgrade}
        onCancel={() => setUpgrade(null)}
        onOk={runUpgrade}
        okText="开始升级"
        destroyOnClose
      >
        <Typography.Paragraph type="secondary" style={{ fontSize: 12 }}>
          节点会下载新二进制、校验 sha256（可选）、替换正在运行的 starport-agent 并在 3 秒后重启服务；重启后以新版本重连，列表里的 agent 版本随之更新。
        </Typography.Paragraph>
        <Form form={upForm} layout="vertical">
          <Form.Item name="binUrl" label="starport-agent 二进制地址（http/https，需与节点架构一致）" rules={[{ required: true, type: 'url', message: '请输入合法 URL' }]}>
            <Input placeholder="https://github.com/nihaoliuyiNN/starport-panel/releases/download/v0.1.0/starport-agent-linux-amd64" />
          </Form.Item>
          <Form.Item name="sha256" label="sha256（可选）" rules={[{ pattern: /^[0-9a-fA-F]{64}$/, message: '64 位十六进制' }]}>
            <Input placeholder="留空则不校验" style={{ fontFamily: 'monospace' }} />
          </Form.Item>
        </Form>
      </Modal>

      <Modal title={`终端 · ${termNode?.facts.hostname ?? ''}`} open={!!termNode} onCancel={() => setTermNode(null)} footer={null} width={960} destroyOnClose>
        {termNode && <Terminal url={wsUrl(`/nodes/${termNode.id}/terminal`)} height={520} />}
      </Modal>

      <TaskLogDrawer taskId={taskId} onClose={() => setTaskId(null)} />
    </>
  );
}
