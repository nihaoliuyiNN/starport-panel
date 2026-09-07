import { useState } from 'react';
import { Alert, App, Button, Card, Descriptions, Form, Input, Modal, Select, Space, Switch, Table, Tag, Typography } from 'antd';
import { ImportOutlined, PlusOutlined, ReloadOutlined } from '@ant-design/icons';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Link, useNavigate } from 'react-router-dom';
import { clustersApi, errMsg, type Cluster, type ClusterProbe, type CreateClusterRequest } from '../api';
import { ClusterStatusTag } from '../components/tags';
import { fmtTime } from '../util';

const ADDONS = ['ingress-nginx', 'metrics-server', 'cert-manager'];

export default function Clusters() {
  const { message } = App.useApp();
  const qc = useQueryClient();
  const nav = useNavigate();
  const clusters = useQuery({ queryKey: ['clusters'], queryFn: clustersApi.list, refetchInterval: 5000 });
  const [open, setOpen] = useState(false);
  const [form] = Form.useForm<CreateClusterRequest>();
  const mode = Form.useWatch('artifactMode', form);
  const [saving, setSaving] = useState(false);

  // 导入已有集群
  const [importOpen, setImportOpen] = useState(false);
  const [importForm] = Form.useForm<{ name: string; kubeconfig: string }>();
  const [probe, setProbe] = useState<ClusterProbe | null>(null);
  const [probing, setProbing] = useState(false);

  const doProbe = async () => {
    const kc = importForm.getFieldValue('kubeconfig') as string;
    if (!kc?.trim()) return message.warning('请先粘贴 kubeconfig');
    setProbing(true);
    try {
      setProbe(await clustersApi.probe(kc));
    } catch (e) {
      setProbe(null);
      message.error(errMsg(e));
    } finally {
      setProbing(false);
    }
  };

  const doImport = async () => {
    const v = await importForm.validateFields();
    setSaving(true);
    try {
      const res = await clustersApi.import(v.name, v.kubeconfig);
      message.success(`已接管集群 ${res.cluster.name}（${res.probe.version}，${res.probe.nodeCount} 节点）`);
      setImportOpen(false);
      importForm.resetFields();
      setProbe(null);
      await qc.invalidateQueries({ queryKey: ['clusters'] });
      nav(`/clusters/${res.cluster.id}`);
    } catch (e) {
      message.error(errMsg(e));
    } finally {
      setSaving(false);
    }
  };

  const submit = async () => {
    const v = await form.validateFields();
    setSaving(true);
    try {
      const c = await clustersApi.create(v);
      message.success(`集群 ${c.name} 已创建，请加入第一台控制面节点开始安装`);
      setOpen(false);
      form.resetFields();
      await qc.invalidateQueries({ queryKey: ['clusters'] });
      nav(`/clusters/${c.id}`);
    } catch (e) {
      message.error(errMsg(e));
    } finally {
      setSaving(false);
    }
  };

  return (
    <>
      <Card
        title="集群"
        extra={
          <Space>
            <Button type="primary" icon={<PlusOutlined />} onClick={() => setOpen(true)}>新建集群</Button>
            <Button icon={<ImportOutlined />} onClick={() => setImportOpen(true)}>导入已有集群</Button>
            <Button icon={<ReloadOutlined />} onClick={() => clusters.refetch()} loading={clusters.isFetching}>刷新</Button>
          </Space>
        }
      >
        <Table<Cluster>
          rowKey="id"
          size="middle"
          loading={clusters.isLoading}
          dataSource={clusters.data ?? []}
          scroll={{ x: 'max-content' }}
          pagination={false}
          columns={[
            { title: 'ID', dataIndex: 'id', width: 60 },
            { title: '名称', render: (_, c) => <Space><Link to={`/clusters/${c.id}`}><b>{c.name}</b></Link>{c.source === 'imported' && <Tag>接管</Tag>}</Space> },
            { title: '状态', width: 100, render: (_, c) => <ClusterStatusTag status={c.status} /> },
            { title: 'Kubernetes', dataIndex: 'k8sVersion', width: 110 },
            { title: 'CNI', width: 130, render: (_, c) => (c.source === 'imported' ? '-' : `${c.cni} ${c.cniVersion}`) },
            { title: '控制面入口', dataIndex: 'controlPlaneEndpoint', render: (v: string) => v || <Typography.Text type="secondary">首 master 安装时回填</Typography.Text> },
            { title: '制品', width: 90, render: (_, c) => (c.source === 'imported' ? '-' : c.artifactMode === 'bundle' ? '离线包' : '在线') },
            { title: '创建时间', width: 170, render: (_, c) => fmtTime(c.createdAt) },
          ]}
        />
      </Card>

      <Modal title="新建集群" open={open} onCancel={() => setOpen(false)} onOk={submit} okText="创建" confirmLoading={saving} width={640} destroyOnClose>
        <Form form={form} layout="vertical" initialValues={{ artifactMode: 'online', cni: 'calico', addons: ['metrics-server'], useCnMirror: true }}>
          <Form.Item name="name" label="名称" rules={[{ required: true, message: '请输入集群名' }, { pattern: /^[a-z0-9]([a-z0-9-]*[a-z0-9])?$/, message: '小写字母、数字、连字符' }]}>
            <Input placeholder="prod" />
          </Form.Item>
          <Space size="large" style={{ display: 'flex' }} align="start">
            <Form.Item name="k8sVersion" label="Kubernetes 版本" tooltip="留空用面板默认版本" style={{ flex: 1 }}><Input placeholder="默认" /></Form.Item>
            <Form.Item name="artifactMode" label="制品来源" style={{ width: 200 }}>
              <Select options={[{ value: 'online', label: '在线（apt/yum + 镜像源）' }, { value: 'bundle', label: '离线包（bundle）' }]} />
            </Form.Item>
          </Space>
          {mode === 'bundle' && (
            <Form.Item name="bundleUrl" label="离线包 URL" rules={[{ required: true, message: '离线模式必填' }]}>
              <Input placeholder="https://.../k8s-bundle-v1.35.7-amd64-cilium.tar.gz" />
            </Form.Item>
          )}
          {mode === 'online' && (
            <Form.Item name="useCnMirror" label="使用国内镜像源" valuePropName="checked"><Switch /></Form.Item>
          )}
          <Space size="large" style={{ display: 'flex' }} align="start">
            <Form.Item name="cni" label="网络插件" style={{ width: 200 }} tooltip="在线模式仅支持 calico">
              <Select options={[{ value: 'calico', label: 'Calico' }, { value: 'cilium', label: 'Cilium', disabled: mode === 'online' }]} />
            </Form.Item>
            <Form.Item name="cniVersion" label="CNI 版本" style={{ flex: 1 }}><Input placeholder="默认" /></Form.Item>
          </Space>
          <Space size="large" style={{ display: 'flex' }} align="start">
            <Form.Item name="vip" label="控制面 VIP（kube-vip）" tooltip="多控制面高可用地址；单控制面可留空" style={{ flex: 1 }}><Input placeholder="10.0.0.100" /></Form.Item>
            <Form.Item name="vipInterface" label="VIP 网卡" style={{ width: 160 }}><Input placeholder="自动" /></Form.Item>
          </Space>
          <Form.Item name="controlPlaneEndpoint" label="控制面入口（host:port）" tooltip="留空：有 VIP 用 VIP:6443，否则首 master 内网 IP:6443">
            <Input placeholder="留空自动" />
          </Form.Item>
          <Space size="large" style={{ display: 'flex' }} align="start">
            <Form.Item name="podCIDR" label="Pod CIDR" style={{ flex: 1 }}><Input placeholder="10.244.0.0/16" /></Form.Item>
            <Form.Item name="serviceCIDR" label="Service CIDR" style={{ flex: 1 }}><Input placeholder="10.96.0.0/12" /></Form.Item>
          </Space>
          <Form.Item name="addons" label="附加组件">
            <Select mode="multiple" options={ADDONS.map((a) => ({ value: a, label: a }))} />
          </Form.Item>
        </Form>
      </Modal>

      <Modal
        title="导入已有集群"
        open={importOpen}
        onCancel={() => { setImportOpen(false); setProbe(null); }}
        onOk={doImport}
        okText="接管"
        okButtonProps={{ disabled: !probe }}
        confirmLoading={saving}
        width={680}
        destroyOnClose
      >
        <Alert
          type="info"
          showIcon
          style={{ marginBottom: 12 }}
          message="接管只需要 admin kubeconfig：面板据此管理集群内资源、部署应用。这类集群没有 kubeadm 凭据，不能经面板加 / 移节点。"
        />
        <Form form={importForm} layout="vertical">
          <Form.Item name="name" label="名称" rules={[{ required: true, message: '请输入集群名' }, { pattern: /^[a-z0-9]([a-z0-9-]*[a-z0-9])?$/, message: '小写字母、数字、连字符' }]}>
            <Input placeholder="legacy-prod" />
          </Form.Item>
          <Form.Item name="kubeconfig" label="kubeconfig" rules={[{ required: true, message: '请粘贴 kubeconfig' }]}>
            <Input.TextArea rows={10} style={{ fontFamily: 'monospace', fontSize: 12 }} placeholder="apiVersion: v1&#10;kind: Config&#10;clusters: ..." onChange={() => setProbe(null)} />
          </Form.Item>
        </Form>
        <Space align="start">
          <Button onClick={doProbe} loading={probing}>连接测试</Button>
          {probe && (
            <Descriptions size="small" column={1} style={{ marginLeft: 8 }}>
              <Descriptions.Item label="版本">{probe.version}</Descriptions.Item>
              <Descriptions.Item label="入口">{probe.endpoint}</Descriptions.Item>
              <Descriptions.Item label="节点数">{probe.nodeCount}</Descriptions.Item>
              {probe.podCIDR && <Descriptions.Item label="Pod CIDR">{probe.podCIDR}</Descriptions.Item>}
            </Descriptions>
          )}
        </Space>
      </Modal>
    </>
  );
}
