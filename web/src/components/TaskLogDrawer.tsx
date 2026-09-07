import { useEffect, useRef, useState } from 'react';
import { Button, Descriptions, Drawer, Space, Tag, App } from 'antd';
import { errMsg, tasksApi, type Task, type TaskLog } from '../api';
import { TaskStatusTag } from './tags';
import { fmtTime } from '../util';

interface Props {
  taskId: number | null;
  onClose: () => void;
}

/** 任务日志抽屉：按 seq 增量轮询，任务结束停止。 */
export default function TaskLogDrawer({ taskId, onClose }: Props) {
  const { message } = App.useApp();
  const [task, setTask] = useState<Task | null>(null);
  const [logs, setLogs] = useState<TaskLog[]>([]);
  const bottom = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!taskId) return;
    setTask(null);
    setLogs([]);
    let after = 0;
    let stop = false;
    let timer: ReturnType<typeof setTimeout>;
    const tick = async () => {
      try {
        const res = await tasksApi.logs(taskId, after, 1000);
        if (stop) return;
        setTask(res.task);
        if (res.logs.length) {
          after = res.logs[res.logs.length - 1].seq;
          setLogs((prev) => [...prev, ...res.logs]);
        }
        if (res.task.status === 'running') timer = setTimeout(tick, 1000);
      } catch (e) {
        if (!stop) message.error(errMsg(e));
      }
    };
    void tick();
    return () => {
      stop = true;
      clearTimeout(timer);
    };
  }, [taskId, message]);

  useEffect(() => {
    bottom.current?.scrollIntoView({ block: 'end' });
  }, [logs.length]);

  const cancel = async () => {
    if (!taskId) return;
    try {
      await tasksApi.cancel(taskId);
      message.success('已发送取消');
    } catch (e) {
      message.error(errMsg(e));
    }
  };

  return (
    <Drawer
      open={!!taskId}
      onClose={onClose}
      width={760}
      title={`任务 #${taskId ?? ''}`}
      extra={task?.status === 'running' && <Button danger size="small" onClick={cancel}>取消任务</Button>}
    >
      {task && (
        <Descriptions size="small" column={3} style={{ marginBottom: 12 }}>
          <Descriptions.Item label="类型"><Tag>{task.kind}</Tag></Descriptions.Item>
          <Descriptions.Item label="状态"><TaskStatusTag status={task.status} /></Descriptions.Item>
          <Descriptions.Item label="节点">#{task.nodeId}{task.clusterId ? ` / 集群 #${task.clusterId}` : ''}</Descriptions.Item>
          <Descriptions.Item label="开始">{fmtTime(task.startedAt)}</Descriptions.Item>
          <Descriptions.Item label="结束">{fmtTime(task.finishedAt)}</Descriptions.Item>
          <Descriptions.Item label="退出码">{task.exitCode}</Descriptions.Item>
          {task.errorCode && (
            <Descriptions.Item label="错误" span={3}>
              <Space><Tag color="error">{task.errorCode}</Tag>{task.errorMessage}</Space>
            </Descriptions.Item>
          )}
        </Descriptions>
      )}
      <pre style={{ background: '#1e1e1e', color: '#ddd', padding: 12, borderRadius: 4, minHeight: 300, maxHeight: 'calc(100vh - 260px)', overflow: 'auto', fontSize: 12, lineHeight: 1.5, margin: 0 }}>
        {logs.map((l) => (
          <div key={l.seq}><span style={{ color: '#777' }}>{fmtTime(l.at, 'HH:mm:ss')} </span>{l.line}</div>
        ))}
        {task?.status === 'running' && <div style={{ color: '#888' }}>…</div>}
        <div ref={bottom} />
      </pre>
    </Drawer>
  );
}
