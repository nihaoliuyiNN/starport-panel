import { useState } from 'react';
import { Select } from 'antd';
import { useQuery } from '@tanstack/react-query';
import { k8sApi } from '../../api';

interface Props {
  cid: number;
  value?: string;
  onChange?: (ns: string) => void;
  allowAll?: boolean;
  /** 允许输入不存在的命名空间（如 Helm 安装时配合 createNamespace）。开启时不再提供「全部」选项。 */
  allowCreate?: boolean;
  style?: React.CSSProperties;
}

/** 命名空间下拉；空值表示全部。可作 antd Form.Item 受控子组件（value/onChange 由 Form 注入）。 */
export default function NamespaceSelect({ cid, value, onChange, allowAll = true, allowCreate = false, style }: Props) {
  const q = useQuery({ queryKey: ['k8s', cid, 'namespaces'], queryFn: k8sApi(cid).namespaces, staleTime: 30_000 });
  const [typed, setTyped] = useState('');
  const names = (q.data ?? []).map((n) => n.name);
  const options = [
    ...(allowAll && !allowCreate ? [{ value: '', label: '全部命名空间' }] : []),
    ...names.map((n) => ({ value: n, label: n })),
    ...(allowCreate && typed && !names.includes(typed) ? [{ value: typed, label: `${typed}（新建）` }] : []),
  ];
  return (
    <Select
      showSearch
      value={value}
      onChange={onChange}
      onSearch={allowCreate ? setTyped : undefined}
      filterOption={(input, opt) => (opt?.value ?? '').toString().includes(input)}
      options={options}
      loading={q.isLoading}
      style={{ width: 220, ...style }}
      placeholder="命名空间"
    />
  );
}
