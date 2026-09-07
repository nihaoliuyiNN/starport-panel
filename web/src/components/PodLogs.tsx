import { useEffect, useRef, useState } from 'react';
import { Button, Select, Space, Switch, InputNumber, Tag } from 'antd';
import { apiUrl, getToken } from '../api';

interface Props {
  path: string; // /clusters/{id}/k8s/namespaces/{ns}/pods/{name}/logs
  containers: string[];
}

/** 容器日志：follow 时 fetch 流式读取，逐行追加。 */
export default function PodLogs({ path, containers }: Props) {
  const [container, setContainer] = useState(containers[0] ?? '');
  const [follow, setFollow] = useState(true);
  const [tail, setTail] = useState(200);
  const [previous, setPrevious] = useState(false);
  const [lines, setLines] = useState<string[]>([]);
  const [status, setStatus] = useState<'loading' | 'streaming' | 'done' | 'error'>('loading');
  const [err, setErr] = useState('');
  const [gen, setGen] = useState(0);
  const bottom = useRef<HTMLDivElement>(null);
  const autoScroll = useRef(true);

  useEffect(() => {
    const ctrl = new AbortController();
    setLines([]);
    setErr('');
    setStatus('loading');
    (async () => {
      try {
        const res = await fetch(apiUrl(path, { container, follow, tail, previous }), {
          headers: { Authorization: `Bearer ${getToken()}` },
          signal: ctrl.signal,
        });
        if (!res.ok) {
          const body = await res.json().catch(() => null);
          throw new Error(body?.error?.message ?? `HTTP ${res.status}`);
        }
        setStatus(follow ? 'streaming' : 'loading');
        const reader = res.body!.getReader();
        const dec = new TextDecoder();
        let buf = '';
        for (;;) {
          const { value, done } = await reader.read();
          if (done) break;
          buf += dec.decode(value, { stream: true });
          const parts = buf.split('\n');
          buf = parts.pop() ?? '';
          if (parts.length) setLines((prev) => [...prev, ...parts].slice(-5000));
        }
        if (buf) setLines((prev) => [...prev, buf]);
        setStatus('done');
      } catch (e) {
        if (ctrl.signal.aborted) return;
        setErr(e instanceof Error ? e.message : String(e));
        setStatus('error');
      }
    })();
    return () => ctrl.abort();
  }, [path, container, follow, tail, previous, gen]);

  useEffect(() => {
    if (autoScroll.current) bottom.current?.scrollIntoView({ block: 'end' });
  }, [lines.length]);

  return (
    <div>
      <Space style={{ marginBottom: 8 }} wrap>
        <Select value={container} onChange={setContainer} options={containers.map((c) => ({ value: c, label: c }))} style={{ width: 200 }} />
        <span>tail <InputNumber size="small" min={10} max={10000} value={tail} onChange={(v) => v && setTail(v)} style={{ width: 90 }} /></span>
        <span>跟随 <Switch size="small" checked={follow} onChange={setFollow} /></span>
        <span>上一次 <Switch size="small" checked={previous} onChange={setPrevious} /></span>
        <Button size="small" onClick={() => setGen((g) => g + 1)}>重新加载</Button>
        {status === 'streaming' && <Tag color="processing">实时</Tag>}
        {status === 'done' && <Tag>已结束</Tag>}
        {status === 'error' && <Tag color="error">{err}</Tag>}
      </Space>
      <pre
        onScroll={(e) => { const el = e.currentTarget; autoScroll.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40; }}
        style={{ background: '#1e1e1e', color: '#ddd', padding: 12, borderRadius: 4, height: 480, overflow: 'auto', fontSize: 12, lineHeight: 1.5, margin: 0, whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}
      >
        {lines.join('\n')}
        <div ref={bottom} />
      </pre>
    </div>
  );
}
