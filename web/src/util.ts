import dayjs from 'dayjs';

/** 零值时间（Go time.Time 零值序列化为 0001-01-01…）显示为 "-"。 */
export function fmtTime(s?: string, fmt = 'YYYY-MM-DD HH:mm:ss'): string {
  if (!s || s.startsWith('0001-')) return '-';
  return dayjs(s).format(fmt);
}

export function fromNow(s?: string): string {
  if (!s || s.startsWith('0001-')) return '-';
  return dayjs(s).fromNow();
}

export function fmtBytes(n: number): string {
  if (!n) return '-';
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
  let i = 0;
  let v = n;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(i >= 3 ? 1 : 0)} ${units[i]}`;
}

/** 复制到剪贴板（http 环境下 navigator.clipboard 不可用时降级）。 */
export async function copyText(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.style.position = 'fixed';
    ta.style.opacity = '0';
    document.body.appendChild(ta);
    ta.select();
    const ok = document.execCommand('copy');
    document.body.removeChild(ta);
    return ok;
  }
}
