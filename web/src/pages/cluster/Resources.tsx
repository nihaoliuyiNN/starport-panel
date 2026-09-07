import { useMemo, useState } from 'react';
import { App, Alert, Button, Drawer, Popconfirm, Select, Space, Table, Tag, Typography } from 'antd';
import { CopyOutlined, DeleteOutlined, ReloadOutlined } from '@ant-design/icons';
import { useQuery } from '@tanstack/react-query';
import { errMsg, k8sApi, type ResourceItem, type ResourceKind } from '../../api';
import NamespaceSelect from './NamespaceSelect';
import { copyText, fromNow } from '../../util';

const kindKey = (k: ResourceKind) => `${k.group}|${k.version}|${k.resource}`;

/** 任意资源类型（含 CRD）的通用浏览：列表 + YAML 查看 + 删除。 */
export default function Resources({ cid }: { cid: number }) {
  const { message } = App.useApp();
  const api = k8sApi(cid);
  const kinds = useQuery({ queryKey: ['k8s', cid, 'resource-kinds'], queryFn: api.resourceKinds, staleTime: 60_000 });
  const [sel, setSel] = useState<string>('|v1|configmaps');
  const [ns, setNs] = useState('');
  const [viewing, setViewing] = useState<{ item: ResourceItem; yaml: string } | null>(null);

  const kind = useMemo(() => (kinds.data ?? []).find((k) => kindKey(k) === sel), [kinds.data, sel]);
  const [g, v, r] = sel.split('|');
  const list = useQuery({
    queryKey: ['k8s', cid, 'resources', sel, kind?.namespaced ? ns : ''],
    queryFn: () => api.listResources(g, v, r, kind?.namespaced ? ns : ''),
    enabled: !!kind,
  });

  const view = async (item: ResourceItem) => {
    try {
      const yaml = await api.getResource(g, v, r, item.name, item.namespace);
      setViewing({ item, yaml });
    } catch (e) {
      message.error(errMsg(e));
    }
  };
  const del = async (item: ResourceItem) => {
    try {
      await api.deleteResource(g, v, r, item.name, item.namespace);
      message.success(`已删除 ${item.name}`);
      list.refetch();
    } catch (e) {
      message.error(errMsg(e));
    }
  };

  const options = useMemo(() => {
    const groups = new Map<string, ResourceKind[]>();
    for (const k of kinds.data ?? []) {
      const gname = k.group || 'core';
      if (!groups.has(gname)) groups.set(gname, []);
      groups.get(gname)!.push(k);
    }
    return [...groups.entries()].map(([gname, ks]) => ({
      label: gname,
      options: ks.map((k) => ({ value: kindKey(k), label: `${k.kind} (${k.resource}${k.group ? `.${k.group}` : ''}/${k.version})` })),
    }));
  }, [kinds.data]);

  return (
    <>
      <Space style={{ marginBottom: 12 }} wrap>
        <Select showSearch optionFilterProp="label" value={sel} onChange={setSel} options={options} loading={kinds.isLoading} style={{ width: 420 }} />
        {kind?.namespaced && <NamespaceSelect cid={cid} value={ns} onChange={setNs} />}
        {kind && !kind.namespaced && <Tag>集群级</Tag>}
        <Button icon={<ReloadOutlined />} onClick={() => list.refetch()} loading={list.isFetching}>刷新</Button>
      </Space>
      {list.isError && <Alert type="error" message={errMsg(list.error)} style={{ marginBottom: 12 }} />}
      <Table<ResourceItem>
        rowKey={(i) => `${i.namespace ?? ''}/${i.name}`}
        size="small"
        loading={list.isLoading}
        dataSource={list.data ?? []}
        scroll={{ x: 'max-content' }}
        pagination={{ pageSize: 50, hideOnSinglePage: true }}
        columns={[
          ...(kind?.namespaced ? [{ title: '命名空间', dataIndex: 'namespace', width: 160 }] : []),
          { title: '名称', dataIndex: 'name', render: (v: string, it: ResourceItem) => <a onClick={() => view(it)}>{v}</a> },
          { title: '标签', render: (_, it) => Object.entries(it.labels).slice(0, 4).map(([k, val]) => <Tag key={k} style={{ fontSize: 11 }}>{k}={val}</Tag>) },
          { title: '存活', width: 100, render: (_, it) => fromNow(it.createdAt) },
          {
            title: '', width: 50,
            render: (_, it) => (
              <Popconfirm title={`删除 ${kind?.kind ?? ''} ${it.name}？`} onConfirm={() => del(it)} okButtonProps={{ danger: true }} disabled={!kind?.verbs.includes('delete')}>
                <Button size="small" danger icon={<DeleteOutlined />} disabled={!kind?.verbs.includes('delete')} />
              </Popconfirm>
            ),
          },
        ]}
      />

      <Drawer
        open={!!viewing}
        onClose={() => setViewing(null)}
        width={820}
        title={viewing ? `${kind?.kind ?? ''} ${viewing.item.namespace ? `${viewing.item.namespace}/` : ''}${viewing.item.name}` : ''}
        extra={<Button icon={<CopyOutlined />} size="small" onClick={async () => { if (viewing && (await copyText(viewing.yaml))) message.success('已复制'); }}>复制</Button>}
      >
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>只读视图；修改请到「YAML」页 apply。</Typography.Text>
        <pre style={{ background: '#fafafa', padding: 12, borderRadius: 4, fontSize: 12, lineHeight: 1.5, overflow: 'auto', marginTop: 8 }}>{viewing?.yaml}</pre>
      </Drawer>
    </>
  );
}
