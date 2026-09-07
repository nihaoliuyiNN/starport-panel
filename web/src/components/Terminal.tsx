import { useEffect, useRef, useState } from 'react';
import { Terminal as XTerm } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import { Tag } from 'antd';

interface Props {
  /** 完整 ws(s):// 地址（已带 token）。传 cols/rows 由本组件按容器尺寸追加。 */
  url: string;
  height?: number | string;
}

/**
 * 与面板终端协议对接的 xterm：二进制帧 = 字节流，文本帧 = {type:"resize"} / {type:"exit"}。
 * 节点终端与容器 exec 共用。
 */
export default function Terminal({ url, height = 480 }: Props) {
  const ref = useRef<HTMLDivElement>(null);
  const [status, setStatus] = useState<'connecting' | 'open' | 'closed'>('connecting');
  const [exitInfo, setExitInfo] = useState('');

  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const term = new XTerm({ cursorBlink: true, fontSize: 13, fontFamily: 'Menlo, Consolas, "Courier New", monospace', theme: { background: '#1e1e1e' } });
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.open(el);
    fit.fit();

    const u = new URL(url);
    u.searchParams.set('cols', String(term.cols));
    u.searchParams.set('rows', String(term.rows));
    const ws = new WebSocket(u.toString());
    ws.binaryType = 'arraybuffer';
    const enc = new TextEncoder();

    ws.onopen = () => {
      setStatus('open');
      term.focus();
    };
    ws.onmessage = (ev) => {
      if (typeof ev.data === 'string') {
        try {
          const msg = JSON.parse(ev.data);
          if (msg.type === 'exit') {
            const err = msg.error ? `：${msg.error.message}（${msg.error.code}）` : '';
            setExitInfo(`进程已退出，exitCode=${msg.exitCode}${err}`);
            term.write(`\r\n\x1b[90m[会话结束 exitCode=${msg.exitCode}${err}]\x1b[0m\r\n`);
          }
        } catch {
          term.write(ev.data);
        }
        return;
      }
      term.write(new Uint8Array(ev.data as ArrayBuffer));
    };
    ws.onclose = () => setStatus('closed');
    ws.onerror = () => setStatus('closed');

    const onData = term.onData((d) => {
      if (ws.readyState === WebSocket.OPEN) ws.send(enc.encode(d));
    });
    const onBinary = term.onBinary((d) => {
      if (ws.readyState === WebSocket.OPEN) ws.send(Uint8Array.from(d, (c) => c.charCodeAt(0)));
    });
    const onResize = term.onResize(({ cols, rows }) => {
      if (ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ type: 'resize', cols, rows }));
    });
    const ro = new ResizeObserver(() => fit.fit());
    ro.observe(el);

    return () => {
      ro.disconnect();
      onData.dispose();
      onBinary.dispose();
      onResize.dispose();
      ws.close();
      term.dispose();
    };
  }, [url]);

  return (
    <div>
      <div style={{ marginBottom: 8 }}>
        {status === 'connecting' && <Tag color="processing">连接中</Tag>}
        {status === 'open' && <Tag color="success">已连接</Tag>}
        {status === 'closed' && <Tag>已断开</Tag>}
        {exitInfo && <span style={{ color: '#888', fontSize: 12 }}>{exitInfo}</span>}
      </div>
      <div ref={ref} style={{ height, background: '#1e1e1e', padding: 4, borderRadius: 4 }} />
    </div>
  );
}
