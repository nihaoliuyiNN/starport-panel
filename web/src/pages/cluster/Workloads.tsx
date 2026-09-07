import { useState } from 'react';
import { App, Alert, Button, Form, Input, InputNumber, Modal, Popconfirm, Select, Space, Table, Tabs, Tag, Tooltip, Typography } from 'antd';
import { CodeOutlined, DeleteOutlined, FileSearchOutlined, FileTextOutlined, ReloadOutlined } from '@ant-design/icons';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { ApiError, errMsg, k8sApi, wsUrl, type Deployment, type Pod } from '../../api';
import { PodPhaseTag } from '../../components/tags';
import PodLogs from '../../components/PodLogs';
import Terminal from '../../components/Terminal';
import YamlDrawer, { type YamlTarget } from '../../components/YamlDrawer';
import NamespaceSelect from './NamespaceSelect';
import { fmtBytes, fromNow } from '../../util';

export default function Workloads({ cid }: { cid: number }) {
  const [ns, setNs] = useState('default');
  return (
    <Tabs
      tabBarExtraContent={<NamespaceSelect cid={cid} value={ns} onChange={setNs} />}
      items={[
        { key: 'pods', label: 'Pods', children: <Pods cid={cid} ns={ns} /> },
        { key: 'deployments', label: 'Deployments', children: <Deployments cid={cid} ns={ns} /> },
      ]}
    />
  );
}

function Pods({ cid, ns }: { cid: number; ns: string }) {
  const { message } = App.useApp();
  const api = k8sApi(cid);
  const q = useQuery({ queryKey: ['k8s', cid, 'pods', ns], queryFn: () => api.pods(ns), refetchInterval: 5000 });
  const usage = useQuery({
    queryKey: ['k8s', cid, 'pod-metrics', ns],
    queryFn: () => api.podMetrics(ns),
    refetchInterval: 15000,
    retry: (n, e) => !(e instanceof ApiError && e.code === 'NO_METRICS_SERVER') && n < 2,
  });
  const usageOf = (p: Pod) => usage.data?.find((u) => u.namespace === p.namespace && u.name === p.name);
  const [logPod, setLogPod] = useState<Pod | null>(null);
  const [execPod, setExecPod] = useState<Pod | null>(null);
  const [execContainer, setExecContainer] = useState('');
  const [yamlTarget, setYamlTarget] = useState<YamlTarget | null>(null);

  const del = async (p: Pod) => {
    try {
      await api.deletePod(p.namespace, p.name);
      message.success(`已删除 ${p.name}`);
      q.refetch();
    } catch (e) {
      message.error(errMsg(e));
    }
  };

  if (q.isError) return <Alert type="error" message={errMsg(q.error)} />;
  return (
    <>
      <Space style={{ marginBottom: 12 }}><Button icon={<ReloadOutlined />} onClick={() => q.refetch()} loading={q.isFetching}>刷新</Button><Typography.Text type="secondary">{q.data?.length ?? 0} 个 Pod</Typography.Text></Space>
      <Table<Pod>
        rowKey={(p) => `${p.namespace}/${p.name}`}
        size="small"
        loading={q.isLoading}
        dataSource={q.data ?? []}
        scroll={{ x: 'max-content' }}
        pagination={{ pageSize: 50, hideOnSinglePage: true }}
        columns={[
          ...(ns ? [] : [{ title: '命名空间', dataIndex: 'namespace', width: 140 }]),
          { title: '名称', dataIndex: 'name', render: (v: string, p: Pod) => <Tooltip title={p.containers.map((c) => `${c.name}: ${c.image} (${c.state || 'unknown'})`).join('\n')}><span>{v}</span></Tooltip> },
          { title: '状态', width: 110, render: (_, p) => <PodPhaseTag phase={p.phase} /> },
          { title: '就绪', dataIndex: 'ready', width: 70 },
          { title: '重启', dataIndex: 'restarts', width: 60 },
          { title: '节点', dataIndex: 'node', width: 140 },
          { title: 'Pod IP', dataIndex: 'podIp', width: 130 },
          ...(usage.data
            ? [
                { title: 'CPU', width: 80, render: (_: unknown, p: Pod) => { const u = usageOf(p); return u ? `${u.cpuMilli}m` : '-'; } },
                { title: '内存', width: 90, render: (_: unknown, p: Pod) => { const u = usageOf(p); return u ? fmtBytes(u.memBytes) : '-'; } },
              ]
            : []),
          { title: '存活', width: 100, render: (_, p) => fromNow(p.createdAt) },
          {
            title: '操作', width: 200,
            render: (_, p) => (
              <Space size={4}>
                <Tooltip title="YAML"><Button size="small" icon={<FileSearchOutlined />} onClick={() => setYamlTarget({ group: '', version: 'v1', resource: 'pods', kind: 'Pod', namespace: p.namespace, name: p.name })} /></Tooltip>
                <Tooltip title="日志"><Button size="small" icon={<FileTextOutlined />} onClick={() => setLogPod(p)} /></Tooltip>
                <Tooltip title="终端"><Button size="small" icon={<CodeOutlined />} onClick={() => { setExecPod(p); setExecContainer(p.containers[0]?.name ?? ''); }} disabled={p.phase !== 'Running'} /></Tooltip>
                <Popconfirm title="删除 Pod？" description="由控制器管理的 Pod 会被重建" onConfirm={() => del(p)} okButtonProps={{ danger: true }}>
                  <Tooltip title="删除"><Button size="small" danger icon={<DeleteOutlined />} /></Tooltip>
                </Popconfirm>
              </Space>
            ),
          },
        ]}
      />

      <Modal title={`日志 · ${logPod?.name ?? ''}`} open={!!logPod} onCancel={() => setLogPod(null)} footer={null} width={1000} destroyOnClose>
        {logPod && <PodLogs path={api.logsPath(logPod.namespace, logPod.name)} containers={logPod.containers.map((c) => c.name)} />}
      </Modal>

      <Modal
        title={<Space>终端 · {execPod?.name}<Select size="small" value={execContainer} onChange={setExecContainer} options={(execPod?.containers ?? []).map((c) => ({ value: c.name, label: c.name }))} style={{ width: 180 }} /></Space>}
        open={!!execPod} onCancel={() => setExecPod(null)} footer={null} width={960} destroyOnClose
      >
        {execPod && execContainer && <Terminal url={wsUrl(api.execPath(execPod.namespace, execPod.name), { container: execContainer })} height={520} />}
      </Modal>

      <YamlDrawer cid={cid} target={yamlTarget} onClose={() => setYamlTarget(null)} onApplied={() => q.refetch()} />
    </>
  );
}

function Deployments({ cid, ns }: { cid: number; ns: string }) {
  const { message } = App.useApp();
  const qc = useQueryClient();
  const api = k8sApi(cid);
  const q = useQuery({ queryKey: ['k8s', cid, 'deployments', ns], queryFn: () => api.deployments(ns), refetchInterval: 5000 });
  const [scaleTarget, setScaleTarget] = useState<Deployment | null>(null);
  const [exposeTarget, setExposeTarget] = useState<Deployment | null>(null);
  const [scaleForm] = Form.useForm<{ replicas: number }>();
  const [exposeForm] = Form.useForm<{ name?: string; type: string; port: number; targetPort?: number; nodePort?: number; protocol: string }>();
  const exposeType = Form.useWatch('type', exposeForm);
  const [yamlTarget, setYamlTarget] = useState<YamlTarget | null>(null);

  const restart = async (d: Deployment) => {
    try {
      await api.restart(d.namespace, d.name);
      message.success(`已触发 ${d.name} 滚动重启`);
    } catch (e) {
      message.error(errMsg(e));
    }
  };
  const scale = async () => {
    if (!scaleTarget) return;
    const v = await scaleForm.validateFields();
    try {
      await api.scale(scaleTarget.namespace, scaleTarget.name, v.replicas);
      message.success(`${scaleTarget.name} 副本数 → ${v.replicas}`);
      setScaleTarget(null);
      q.refetch();
    } catch (e) {
      message.error(errMsg(e));
    }
  };
  const expose = async () => {
    if (!exposeTarget) return;
    const v = await exposeForm.validateFields();
    try {
      const svc = await api.expose(exposeTarget.namespace, exposeTarget.name, v);
      const np = svc.ports[0]?.nodePort;
      message.success(`Service ${svc.name} 已创建（${svc.type}${np ? `，NodePort ${np}` : ''}）`);
      setExposeTarget(null);
      qc.invalidateQueries({ queryKey: ['k8s', cid, 'services'] });
    } catch (e) {
      message.error(errMsg(e));
    }
  };

  if (q.isError) return <Alert type="error" message={errMsg(q.error)} />;
  return (
    <>
      <Space style={{ marginBottom: 12 }}><Button icon={<ReloadOutlined />} onClick={() => q.refetch()} loading={q.isFetching}>刷新</Button></Space>
      <Table<Deployment>
        rowKey={(d) => `${d.namespace}/${d.name}`}
        size="small"
        loading={q.isLoading}
        dataSource={q.data ?? []}
        scroll={{ x: 'max-content' }}
        pagination={{ pageSize: 50, hideOnSinglePage: true }}
        columns={[
          ...(ns ? [] : [{ title: '命名空间', dataIndex: 'namespace', width: 140 }]),
          { title: '名称', dataIndex: 'name', render: (v: string) => <b>{v}</b> },
          { title: '副本', width: 160, render: (_, d) => <Space size={4}><Tag color={d.ready === d.replicas ? 'success' : 'warning'}>{d.ready}/{d.replicas}</Tag><Typography.Text type="secondary" style={{ fontSize: 12 }}>更新 {d.updated} · 可用 {d.available}</Typography.Text></Space> },
          { title: '镜像', render: (_, d) => d.images.map((i) => <div key={i} style={{ fontSize: 12, fontFamily: 'monospace' }}>{i}</div>) },
          { title: '存活', width: 100, render: (_, d) => fromNow(d.createdAt) },
          {
            title: '操作', width: 290,
            render: (_, d) => (
              <Space size={4}>
                <Button size="small" onClick={() => setYamlTarget({ group: 'apps', version: 'v1', resource: 'deployments', kind: 'Deployment', namespace: d.namespace, name: d.name })}>YAML</Button>
                <Button size="small" onClick={() => { setScaleTarget(d); scaleForm.setFieldsValue({ replicas: d.replicas }); }}>扩缩</Button>
                <Popconfirm title="滚动重启？" onConfirm={() => restart(d)}><Button size="small">重启</Button></Popconfirm>
                <Button size="small" onClick={() => { setExposeTarget(d); exposeForm.setFieldsValue({ name: d.name, type: 'ClusterIP', port: 80, protocol: 'TCP' }); }}>暴露</Button>
              </Space>
            ),
          },
        ]}
      />

      <Modal title={`扩缩 · ${scaleTarget?.name ?? ''}`} open={!!scaleTarget} onCancel={() => setScaleTarget(null)} onOk={scale} okText="应用" destroyOnClose>
        <Form form={scaleForm} layout="vertical"><Form.Item name="replicas" label="副本数" rules={[{ required: true }]}><InputNumber min={0} max={1000} style={{ width: '100%' }} /></Form.Item></Form>
      </Modal>

      <Modal title={`暴露 · ${exposeTarget?.name ?? ''}`} open={!!exposeTarget} onCancel={() => setExposeTarget(null)} onOk={expose} okText="创建 Service" destroyOnClose>
        <Form form={exposeForm} layout="vertical">
          <Form.Item name="name" label="Service 名称"><Input /></Form.Item>
          <Form.Item name="type" label="类型" rules={[{ required: true }]}>
            <Select options={[{ value: 'ClusterIP', label: 'ClusterIP（集群内）' }, { value: 'NodePort', label: 'NodePort（节点端口）' }, { value: 'LoadBalancer', label: 'LoadBalancer' }]} />
          </Form.Item>
          <Space size="large" style={{ display: 'flex' }}>
            <Form.Item name="port" label="Service 端口" rules={[{ required: true }]}><InputNumber min={1} max={65535} /></Form.Item>
            <Form.Item name="targetPort" label="容器端口" tooltip="留空同 Service 端口"><InputNumber min={1} max={65535} /></Form.Item>
            {exposeType !== 'ClusterIP' && <Form.Item name="nodePort" label="NodePort" tooltip="留空由集群分配（30000-32767）"><InputNumber min={30000} max={32767} /></Form.Item>}
            <Form.Item name="protocol" label="协议"><Select options={[{ value: 'TCP' }, { value: 'UDP' }]} style={{ width: 90 }} /></Form.Item>
          </Space>
        </Form>
      </Modal>

      <YamlDrawer cid={cid} target={yamlTarget} onClose={() => setYamlTarget(null)} onApplied={() => q.refetch()} />
    </>
  );
}
