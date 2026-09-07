import { Select } from 'antd';
import { useQuery } from '@tanstack/react-query';
import { k8sApi } from '../../api';

interface Props {
  cid: number;
  value: string;
  onChange: (ns: string) => void;
  allowAll?: boolean;
  style?: React.CSSProperties;
}

/** 命名空间下拉；空值表示全部。 */
export default function NamespaceSelect({ cid, value, onChange, allowAll = true, style }: Props) {
  const q = useQuery({ queryKey: ['k8s', cid, 'namespaces'], queryFn: k8sApi(cid).namespaces, staleTime: 30_000 });
  const options = [
    ...(allowAll ? [{ value: '', label: '全部命名空间' }] : []),
    ...(q.data ?? []).map((n) => ({ value: n.name, label: n.name })),
  ];
  return <Select showSearch value={value} onChange={onChange} options={options} loading={q.isLoading} style={{ width: 220, ...style }} placeholder="命名空间" />;
}
