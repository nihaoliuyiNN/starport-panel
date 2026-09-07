import { Tag } from 'antd';
import type { ClusterStatus, MemberStatus, TaskStatus } from '../api';

const clusterColor: Record<ClusterStatus, string> = { created: 'default', installing: 'processing', ready: 'success', failed: 'error' };
const clusterText: Record<ClusterStatus, string> = { created: '未初始化', installing: '安装中', ready: '就绪', failed: '失败' };

export function ClusterStatusTag({ status }: { status: ClusterStatus }) {
  return <Tag color={clusterColor[status]}>{clusterText[status] ?? status}</Tag>;
}

const memberColor: Record<MemberStatus, string> = { installing: 'processing', ready: 'success', failed: 'error', removing: 'warning' };
const memberText: Record<MemberStatus, string> = { installing: '安装中', ready: '就绪', failed: '失败', removing: '移除中' };

export function MemberStatusTag({ status }: { status: MemberStatus }) {
  return <Tag color={memberColor[status]}>{memberText[status] ?? status}</Tag>;
}

const taskColor: Record<TaskStatus, string> = { running: 'processing', succeeded: 'success', failed: 'error', cancelled: 'default' };
const taskText: Record<TaskStatus, string> = { running: '运行中', succeeded: '成功', failed: '失败', cancelled: '已取消' };

export function TaskStatusTag({ status }: { status: TaskStatus }) {
  return <Tag color={taskColor[status]}>{taskText[status] ?? status}</Tag>;
}

export function OnlineTag({ online }: { online: boolean }) {
  return online ? <Tag color="success">在线</Tag> : <Tag>离线</Tag>;
}

export function RoleTag({ role }: { role: string }) {
  const map: Record<string, [string, string]> = {
    'first-master': ['gold', '首控制面'],
    'join-master': ['orange', '控制面'],
    worker: ['blue', '工作节点'],
  };
  const [color, text] = map[role] ?? ['default', role];
  return <Tag color={color}>{text}</Tag>;
}

export function PodPhaseTag({ phase }: { phase: string }) {
  const color = phase === 'Running' ? 'success' : phase === 'Pending' ? 'processing' : phase === 'Succeeded' ? 'default' : phase === 'Terminating' ? 'warning' : 'error';
  return <Tag color={color}>{phase}</Tag>;
}
