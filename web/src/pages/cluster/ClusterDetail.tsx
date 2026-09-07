import { useState } from 'react';
import { App, Button, Card, Descriptions, Input, Modal, Popconfirm, Space, Tabs, Tag, Typography, Alert } from 'antd';
import { DeleteOutlined, DownloadOutlined, ReloadOutlined, UploadOutlined } from '@ant-design/icons';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useNavigate, useParams } from 'react-router-dom';
import { clustersApi, errMsg } from '../../api';
import { ClusterStatusTag } from '../../components/tags';
import TaskLogDrawer from '../../components/TaskLogDrawer';
import { fmtTime } from '../../util';
import Members from './Members';
import K8sNodes from './K8sNodes';
import Workloads from './Workloads';
import Network from './Network';
import Events from './Events';
import ApplyYaml from './ApplyYaml';
import Resources from './Resources';
import Apps from './Apps';

export default function ClusterDetail() {
  const { id, tab } = useParams();
  const cid = Number(id);
  const nav = useNavigate();
  const qc = useQueryClient();
  const { message, modal } = App.useApp();
  const [taskId, setTaskId] = useState<number | null>(null);
  const [kcOpen, setKcOpen] = useState(false);
  const [kcText, setKcText] = useState('');
  const [kcSaving, setKcSaving] = useState(false);

  const cluster = useQuery({ queryKey: ['cluster', cid], queryFn: () => clustersApi.get(cid), refetchInterval: 5000, enabled: !!cid });
  const c = cluster.data;
  const ready = c?.status === 'ready';
  const imported = c?.source === 'imported';

  const downloadKubeconfig = async () => {
    try {
      const kc = await clustersApi.kubeconfig(cid);
      const blob = new Blob([kc], { type: 'application/yaml' });
      const a = document.createElement('a');
      a.href = URL.createObjectURL(blob);
      a.download = `${c?.name ?? 'cluster'}.kubeconfig`;
      a.click();
      URL.revokeObjectURL(a.href);
    } catch (e) {
      message.error(errMsg(e));
    }
  };

  const saveKubeconfig = async () => {
    setKcSaving(true);
    try {
      await clustersApi.updateKubeconfig(cid, kcText);
      message.success('kubeconfig 已替换');
      setKcOpen(false);
      setKcText('');
      qc.invalidateQueries({ queryKey: ['cluster', cid] });
    } catch (e) {
      message.error(errMsg(e));
    } finally {
      setKcSaving(false);
    }
  };

  const remove = (force: boolean) => {
    modal.confirm({
      title: force ? '强制删除集群？' : imported ? '取消接管？' : '删除集群？',
      content: force
        ? '将对所有在线成员节点执行 kubeadm reset（数据不可恢复），并立即删除集群记录。'
        : imported
          ? '只删除面板里的记录与 kubeconfig，不影响集群本身。'
          : '集群没有成员，将删除集群记录。',
      okButtonProps: { danger: true },
      onOk: async () => {
        try {
          const res = await clustersApi.remove(cid, force);
          message.success(res.taskIds.length ? `已发起 ${res.taskIds.length} 个节点重置任务` : '集群已删除');
          await qc.invalidateQueries({ queryKey: ['clusters'] });
          nav('/clusters');
        } catch (e) {
          message.error(errMsg(e));
        }
      },
    });
  };

  if (cluster.isError) return <Alert type="error" message={errMsg(cluster.error)} />;
  if (!c) return <Card loading />;

  const hasMembers = c.members.length > 0;

  const tabs = [
    ...(imported ? [] : [{ key: 'members', label: '成员节点', children: <Members cluster={c} onTask={setTaskId} /> }]),
    { key: 'k8s-nodes', label: 'K8s 节点', disabled: !ready, children: <K8sNodes cid={cid} /> },
    { key: 'workloads', label: '工作负载', disabled: !ready, children: <Workloads cid={cid} /> },
    { key: 'apps', label: '应用（Helm）', disabled: !ready, children: <Apps cid={cid} /> },
    { key: 'network', label: '网络', disabled: !ready, children: <Network cid={cid} /> },
    { key: 'events', label: '事件', disabled: !ready, children: <Events cid={cid} /> },
    { key: 'apply', label: 'YAML', disabled: !ready, children: <ApplyYaml cid={cid} /> },
    { key: 'resources', label: '资源浏览', disabled: !ready, children: <Resources cid={cid} /> },
  ];

  return (
    <>
      <Card
        title={
          <Space>
            <span>集群 {c.name}</span>
            <ClusterStatusTag status={c.status} />
            {imported && <Tag>接管</Tag>}
          </Space>
        }
        extra={
          <Space>
            <Button icon={<DownloadOutlined />} disabled={!ready} onClick={downloadKubeconfig}>kubeconfig</Button>
            {imported && <Button icon={<UploadOutlined />} onClick={() => setKcOpen(true)}>替换 kubeconfig</Button>}
            {hasMembers ? (
              <Popconfirm title="集群仍有成员" description="需先逐个移除节点，或强制删除（重置全部节点）" okText="强制删除" okButtonProps={{ danger: true }} onConfirm={() => remove(true)}>
                <Button danger icon={<DeleteOutlined />}>删除集群</Button>
              </Popconfirm>
            ) : (
              <Button danger icon={<DeleteOutlined />} onClick={() => remove(false)}>{imported ? '取消接管' : '删除集群'}</Button>
            )}
            <Button icon={<ReloadOutlined />} onClick={() => cluster.refetch()} loading={cluster.isFetching} />
          </Space>
        }
        style={{ marginBottom: 16 }}
      >
        {imported ? (
          <Descriptions size="small" column={4}>
            <Descriptions.Item label="Kubernetes">{c.k8sVersion}</Descriptions.Item>
            <Descriptions.Item label="apiserver">{c.controlPlaneEndpoint || '-'}</Descriptions.Item>
            <Descriptions.Item label="Pod CIDR">{c.podCIDR || '-'}</Descriptions.Item>
            <Descriptions.Item label="来源">接管（仅 kubeconfig，不能经面板加 / 移节点）</Descriptions.Item>
            <Descriptions.Item label="接管时间">{fmtTime(c.createdAt)}</Descriptions.Item>
            <Descriptions.Item label="更新">{fmtTime(c.updatedAt)}</Descriptions.Item>
          </Descriptions>
        ) : (
          <Descriptions size="small" column={4}>
            <Descriptions.Item label="Kubernetes">{c.k8sVersion}</Descriptions.Item>
            <Descriptions.Item label="CNI">{c.cni} {c.cniVersion}</Descriptions.Item>
            <Descriptions.Item label="控制面入口">{c.controlPlaneEndpoint || <Typography.Text type="secondary">待回填</Typography.Text>}</Descriptions.Item>
            <Descriptions.Item label="VIP">{c.vip || '-'}</Descriptions.Item>
            <Descriptions.Item label="Pod CIDR">{c.podCIDR}</Descriptions.Item>
            <Descriptions.Item label="Service CIDR">{c.serviceCIDR}</Descriptions.Item>
            <Descriptions.Item label="制品">{c.artifactMode === 'bundle' ? `离线包 ${c.bundleUrl}` : `在线${c.useCnMirror ? '（国内源）' : ''}`}</Descriptions.Item>
            <Descriptions.Item label="附加组件">{c.addons.length ? c.addons.join(', ') : '-'}</Descriptions.Item>
            <Descriptions.Item label="创建">{fmtTime(c.createdAt)}</Descriptions.Item>
            <Descriptions.Item label="更新">{fmtTime(c.updatedAt)}</Descriptions.Item>
          </Descriptions>
        )}
      </Card>

      <Card>
        <Tabs
          activeKey={tab ?? (imported ? 'k8s-nodes' : 'members')}
          onChange={(k) => nav(`/clusters/${cid}/${k}`, { replace: true })}
          destroyInactiveTabPane
          items={tabs}
        />
      </Card>

      <Modal title="替换 kubeconfig" open={kcOpen} onCancel={() => setKcOpen(false)} onOk={saveKubeconfig} okText="保存" confirmLoading={kcSaving} okButtonProps={{ disabled: !kcText.trim() }} width={680} destroyOnClose>
        <Typography.Paragraph type="secondary" style={{ fontSize: 12 }}>证书轮换或换了入口时使用。保存前会先连接一次 apiserver 验证。</Typography.Paragraph>
        <Input.TextArea rows={12} value={kcText} onChange={(e) => setKcText(e.target.value)} style={{ fontFamily: 'monospace', fontSize: 12 }} />
      </Modal>

      <TaskLogDrawer taskId={taskId} onClose={() => setTaskId(null)} />
    </>
  );
}
