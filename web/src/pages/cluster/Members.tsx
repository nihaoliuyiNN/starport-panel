import { useState } from 'react';
import { App, Alert, Button, Form, Modal, Popconfirm, Select, Space, Table, Typography } from 'antd';
import { PlusOutlined } from '@ant-design/icons';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { clustersApi, errMsg, nodesApi, type ClusterDetail, type Member, type Role } from '../../api';
import { MemberStatusTag, OnlineTag, RoleTag } from '../../components/tags';
import { fmtTime } from '../../util';

interface Props {
  cluster: ClusterDetail;
  onTask: (id: number) => void;
}

export default function Members({ cluster, onTask }: Props) {
  const { message } = App.useApp();
  const qc = useQueryClient();
  const nodes = useQuery({ queryKey: ['nodes'], queryFn: nodesApi.list, refetchInterval: 5000 });
  const [open, setOpen] = useState(false);
  const [form] = Form.useForm<{ nodeId: number; role: Role }>();
  const [saving, setSaving] = useState(false);

  const nodeMap = new Map((nodes.data ?? []).map((n) => [n.id, n]));
  const memberIds = new Set(cluster.members.map((m) => m.nodeId));
  const hasControlPlane = cluster.members.some((m) => m.role !== 'worker' && m.status !== 'failed');
  const ready = cluster.status === 'ready';

  // 可选角色：没控制面 → 只能 first-master；有了 → join-master / worker
  const roleOptions = hasControlPlane
    ? [{ value: 'join-master', label: '控制面（加入）', disabled: !ready }, { value: 'worker', label: '工作节点', disabled: !ready }]
    : [{ value: 'first-master', label: '首控制面（初始化集群）' }];

  const candidates = (nodes.data ?? []).filter((n) => !memberIds.has(n.id));

  const add = async () => {
    const v = await form.validateFields();
    setSaving(true);
    try {
      const res = await clustersApi.addNode(cluster.id, v.nodeId, v.role);
      setOpen(false);
      form.resetFields();
      await qc.invalidateQueries({ queryKey: ['cluster', cluster.id] });
      onTask(res.taskId);
    } catch (e) {
      message.error(errMsg(e));
    } finally {
      setSaving(false);
    }
  };

  const remove = async (m: Member) => {
    try {
      const res = await clustersApi.removeNode(cluster.id, m.nodeId);
      await qc.invalidateQueries({ queryKey: ['cluster', cluster.id] });
      onTask(res.taskId);
    } catch (e) {
      message.error(errMsg(e));
    }
  };

  return (
    <>
      {!hasControlPlane && (
        <Alert type="info" showIcon style={{ marginBottom: 12 }} message="集群尚无控制面：先选一台在线节点作为「首控制面」开始安装；就绪后再加入其它控制面或工作节点。" />
      )}
      <Space style={{ marginBottom: 12 }}>
        <Button type="primary" icon={<PlusOutlined />} onClick={() => setOpen(true)} disabled={hasControlPlane && !ready}>加入节点</Button>
        {hasControlPlane && !ready && <Typography.Text type="secondary">控制面{cluster.status === 'installing' ? '安装中' : '未就绪'}，暂不能加入其它节点</Typography.Text>}
      </Space>
      <Table<Member>
        rowKey="nodeId"
        size="middle"
        scroll={{ x: 'max-content' }}
        pagination={false}
        dataSource={cluster.members}
        columns={[
          { title: '节点', render: (_, m) => { const n = nodeMap.get(m.nodeId); return n ? <Space direction="vertical" size={0}><b>{n.facts.hostname}</b><Typography.Text type="secondary" style={{ fontSize: 12 }}>{n.facts.internalIp} · #{n.id}</Typography.Text></Space> : `#${m.nodeId}`; } },
          { title: '在线', width: 80, render: (_, m) => <OnlineTag online={!!nodeMap.get(m.nodeId)?.online} /> },
          { title: '角色', width: 110, render: (_, m) => <RoleTag role={m.role} /> },
          { title: '状态', width: 100, render: (_, m) => <MemberStatusTag status={m.status} /> },
          { title: '说明', render: (_, m) => (m.error ? <Typography.Text type="danger">{m.error}</Typography.Text> : '-') },
          { title: '更新时间', width: 170, render: (_, m) => fmtTime(m.updatedAt) },
          {
            title: '操作', width: 200,
            render: (_, m) => (
              <Space>
                {m.taskId > 0 && <Button size="small" onClick={() => onTask(m.taskId)}>任务日志</Button>}
                <Popconfirm
                  title="移除节点？"
                  description="将在其它控制面上 drain 并删除该节点，再对其执行 kubeadm reset。"
                  okText="移除" okButtonProps={{ danger: true }}
                  onConfirm={() => remove(m)}
                  disabled={m.status === 'installing' || m.status === 'removing'}
                >
                  <Button size="small" danger disabled={m.status === 'installing' || m.status === 'removing'}>移除</Button>
                </Popconfirm>
              </Space>
            ),
          },
        ]}
      />

      <Modal title="加入节点" open={open} onCancel={() => setOpen(false)} onOk={add} okText="开始安装" confirmLoading={saving} destroyOnClose>
        <Form form={form} layout="vertical" initialValues={{ role: roleOptions[0].value }}>
          <Form.Item name="nodeId" label="节点" rules={[{ required: true, message: '请选择节点' }]}>
            <Select
              showSearch
              optionFilterProp="label"
              placeholder="选择在线节点"
              options={candidates.map((n) => ({ value: n.id, label: `${n.facts.hostname} (${n.facts.internalIp})${n.online ? '' : ' · 离线'}`, disabled: !n.online }))}
            />
          </Form.Item>
          <Form.Item name="role" label="角色" rules={[{ required: true }]}>
            <Select options={roleOptions} />
          </Form.Item>
        </Form>
      </Modal>
    </>
  );
}
