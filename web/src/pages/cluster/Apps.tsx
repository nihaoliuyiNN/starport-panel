import { useEffect, useMemo, useState } from 'react';
import { Alert, App, Button, Card, Checkbox, Col, Descriptions, Drawer, Empty, Form, Input, List, Modal, Popconfirm, Row, Select, Space, Table, Tabs, Tag, Typography } from 'antd';
import { AppstoreOutlined, DeleteOutlined, HistoryOutlined, PlusOutlined, ReloadOutlined, SearchOutlined, SyncOutlined } from '@ant-design/icons';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { errMsg, helmApi, type HelmChart, type HelmRelease, type HelmRepo } from '../../api';
import NamespaceSelect from './NamespaceSelect';
import { fmtTime, fromNow } from '../../util';

// 常用公共仓库，一键添加
const PRESET_REPOS: { name: string; url: string; note: string }[] = [
  { name: 'bitnami', url: 'https://charts.bitnami.com/bitnami', note: '常用中间件（MySQL / Redis / Kafka …）' },
  { name: 'ingress-nginx', url: 'https://kubernetes.github.io/ingress-nginx', note: 'Ingress 控制器' },
  { name: 'metrics-server', url: 'https://kubernetes-sigs.github.io/metrics-server/', note: '节点 / Pod 用量' },
  { name: 'jetstack', url: 'https://charts.jetstack.io', note: 'cert-manager' },
  { name: 'prometheus-community', url: 'https://prometheus-community.github.io/helm-charts', note: 'kube-prometheus-stack' },
  { name: 'grafana', url: 'https://grafana.github.io/helm-charts', note: 'Grafana / Loki' },
  { name: 'longhorn', url: 'https://charts.longhorn.io', note: '分布式块存储' },
];

const statusColor = (s: string) =>
  s === 'deployed' ? 'success' : s.startsWith('pending') ? 'processing' : s === 'failed' ? 'error' : s === 'uninstalling' || s === 'superseded' ? 'warning' : 'default';

/** 应用（Helm）：Release 列表 + Chart 市场 + 仓库管理。 */
export default function Apps({ cid }: { cid: number }) {
  const [tab, setTab] = useState('releases');
  const [installChart, setInstallChart] = useState<HelmChart | null>(null);
  return (
    <>
      <Tabs
        activeKey={tab}
        onChange={setTab}
        items={[
          { key: 'releases', label: '已部署', children: <Releases cid={cid} onInstall={() => setTab('charts')} /> },
          { key: 'charts', label: '应用市场', children: <Charts cid={cid} onInstall={setInstallChart} onManageRepos={() => setTab('repos')} /> },
          { key: 'repos', label: '仓库', children: <Repos cid={cid} /> },
        ]}
      />
      <InstallModal cid={cid} chart={installChart} onClose={() => setInstallChart(null)} onDone={() => { setInstallChart(null); setTab('releases'); }} />
    </>
  );
}

// ── 已部署 ────────────────────────────────────────────────────────────────────

function Releases({ cid, onInstall }: { cid: number; onInstall: () => void }) {
  const { message } = App.useApp();
  const api = helmApi(cid);
  const [ns, setNs] = useState('');
  const q = useQuery({ queryKey: ['helm', cid, 'releases', ns], queryFn: () => api.releases(ns), refetchInterval: 10000 });
  const [history, setHistory] = useState<HelmRelease | null>(null);
  const [upgrading, setUpgrading] = useState<HelmRelease | null>(null);
  const [notes, setNotes] = useState<HelmRelease | null>(null);

  const uninstall = async (r: HelmRelease) => {
    try {
      await api.uninstall(r.namespace, r.name);
      message.success(`已卸载 ${r.name}`);
      q.refetch();
    } catch (e) {
      message.error(errMsg(e));
    }
  };

  if (q.isError) return <Alert type="error" message={errMsg(q.error)} />;
  return (
    <>
      <Space style={{ marginBottom: 12 }} wrap>
        <NamespaceSelect cid={cid} value={ns} onChange={setNs} />
        <Button icon={<ReloadOutlined />} onClick={() => q.refetch()} loading={q.isFetching}>刷新</Button>
        <Button type="primary" icon={<PlusOutlined />} onClick={onInstall}>安装应用</Button>
        <Typography.Text type="secondary">{q.data?.length ?? 0} 个 release</Typography.Text>
      </Space>
      <Table<HelmRelease>
        rowKey={(r) => `${r.namespace}/${r.name}`}
        size="small"
        loading={q.isLoading}
        dataSource={q.data ?? []}
        scroll={{ x: 'max-content' }}
        pagination={{ pageSize: 50, hideOnSinglePage: true }}
        locale={{ emptyText: <Empty description="尚无 Helm 应用" /> }}
        columns={[
          { title: '名称', dataIndex: 'name', render: (v: string) => <b>{v}</b> },
          ...(ns ? [] : [{ title: '命名空间', dataIndex: 'namespace', width: 140 }]),
          { title: '状态', width: 120, render: (_, r) => <Tag color={statusColor(r.status)}>{r.status}</Tag> },
          { title: 'Chart', render: (_, r) => <Space direction="vertical" size={0}><span>{r.chart}</span>{r.appVersion && <Typography.Text type="secondary" style={{ fontSize: 12 }}>app {r.appVersion}</Typography.Text>}</Space> },
          { title: '修订', dataIndex: 'revision', width: 70 },
          { title: '更新', width: 110, render: (_, r) => fromNow(r.updated) },
          {
            title: '操作', width: 300,
            render: (_, r) => (
              <Space size={4}>
                <Button size="small" icon={<SyncOutlined />} onClick={() => setUpgrading(r)}>升级</Button>
                <Button size="small" icon={<HistoryOutlined />} onClick={() => setHistory(r)}>历史</Button>
                {r.notes && <Button size="small" onClick={() => setNotes(r)}>说明</Button>}
                <Popconfirm title={`卸载 ${r.name}？`} description="删除该 release 创建的全部资源（PVC 通常保留）。" okText="卸载" okButtonProps={{ danger: true }} onConfirm={() => uninstall(r)}>
                  <Button size="small" danger icon={<DeleteOutlined />} />
                </Popconfirm>
              </Space>
            ),
          },
        ]}
      />
      <HistoryDrawer cid={cid} release={history} onClose={() => setHistory(null)} onChanged={() => q.refetch()} />
      <UpgradeModal cid={cid} release={upgrading} onClose={() => setUpgrading(null)} onDone={() => { setUpgrading(null); q.refetch(); }} />
      <Modal title={`${notes?.name ?? ''} · NOTES`} open={!!notes} onCancel={() => setNotes(null)} footer={null} width={760}>
        <pre style={{ background: '#fafafa', padding: 12, borderRadius: 4, fontSize: 12, whiteSpace: 'pre-wrap' }}>{notes?.notes}</pre>
      </Modal>
    </>
  );
}

function HistoryDrawer({ cid, release, onClose, onChanged }: { cid: number; release: HelmRelease | null; onClose: () => void; onChanged: () => void }) {
  const { message } = App.useApp();
  const api = helmApi(cid);
  const q = useQuery({
    queryKey: ['helm', cid, 'history', release?.namespace, release?.name],
    queryFn: () => api.history(release!.namespace, release!.name),
    enabled: !!release,
  });
  const rollback = async (rev: number) => {
    if (!release) return;
    try {
      await api.rollback(release.namespace, release.name, rev);
      message.success(`已回滚 ${release.name} 到修订 ${rev}`);
      q.refetch();
      onChanged();
    } catch (e) {
      message.error(errMsg(e));
    }
  };
  return (
    <Drawer open={!!release} onClose={onClose} width={720} title={release ? `${release.name} · 修订历史` : ''}>
      <Table<HelmRelease>
        rowKey="revision"
        size="small"
        loading={q.isLoading}
        dataSource={q.data ?? []}
        pagination={false}
        scroll={{ x: 'max-content' }}
        columns={[
          { title: '修订', dataIndex: 'revision', width: 70 },
          { title: '状态', width: 110, render: (_, r) => <Tag color={statusColor(r.status)}>{r.status}</Tag> },
          { title: 'Chart', dataIndex: 'chart' },
          { title: 'app', dataIndex: 'appVersion', width: 90 },
          { title: '时间', width: 160, render: (_, r) => fmtTime(r.updated) },
          { title: '说明', dataIndex: 'description', ellipsis: true },
          {
            title: '', width: 80,
            render: (_, r) =>
              r.status !== 'deployed' && (
                <Popconfirm title={`回滚到修订 ${r.revision}？`} onConfirm={() => rollback(r.revision)}>
                  <Button size="small">回滚</Button>
                </Popconfirm>
              ),
          },
        ]}
      />
    </Drawer>
  );
}

// ── 应用市场 ──────────────────────────────────────────────────────────────────

function Charts({ cid, onInstall, onManageRepos }: { cid: number; onInstall: (c: HelmChart) => void; onManageRepos: () => void }) {
  const api = helmApi(cid);
  const [kw, setKw] = useState('');
  const [repo, setRepo] = useState<string | undefined>();
  const repos = useQuery({ queryKey: ['helm', cid, 'repos'], queryFn: api.repos });
  const q = useQuery({ queryKey: ['helm', cid, 'charts', kw, repo ?? ''], queryFn: () => api.search(kw, repo), enabled: (repos.data?.length ?? 0) > 0, staleTime: 60_000 });

  if (repos.data && repos.data.length === 0) {
    return (
      <Empty description="尚未配置 Chart 仓库">
        <Button type="primary" onClick={onManageRepos}>去添加仓库</Button>
      </Empty>
    );
  }
  const errs = Object.entries(q.data?.errors ?? {});
  return (
    <>
      <Space style={{ marginBottom: 12 }} wrap>
        <Input.Search allowClear placeholder="搜索 chart 名称 / 描述 / 关键词" prefix={<SearchOutlined />} style={{ width: 360 }} onSearch={setKw} enterButton />
        <Select allowClear placeholder="全部仓库" style={{ width: 200 }} value={repo} onChange={setRepo} options={(repos.data ?? []).map((r) => ({ value: r.name, label: r.name }))} />
        <Typography.Text type="secondary">{q.data?.charts.length ?? 0} 个 chart</Typography.Text>
      </Space>
      {errs.length > 0 && <Alert type="warning" showIcon style={{ marginBottom: 12 }} message={`部分仓库索引拉取失败：${errs.map(([n, m]) => `${n}（${m}）`).join('；')}`} />}
      {q.isError && <Alert type="error" message={errMsg(q.error)} style={{ marginBottom: 12 }} />}
      <List<HelmChart>
        grid={{ gutter: 12, xs: 1, sm: 2, md: 2, lg: 3, xl: 4, xxl: 4 }}
        loading={q.isLoading}
        dataSource={q.data?.charts ?? []}
        pagination={{ pageSize: 24, hideOnSinglePage: true, size: 'small' }}
        renderItem={(c) => (
          <List.Item>
            <Card
              size="small"
              hoverable
              onClick={() => onInstall(c)}
              title={
                <Space size={8}>
                  {c.icon ? <img src={c.icon} alt="" style={{ width: 20, height: 20, objectFit: 'contain' }} onError={(e) => ((e.target as HTMLImageElement).style.display = 'none')} /> : <AppstoreOutlined />}
                  <span>{c.name}</span>
                  {c.deprecated && <Tag color="warning">deprecated</Tag>}
                </Space>
              }
              extra={<Tag>{c.repo}</Tag>}
            >
              <Typography.Paragraph ellipsis={{ rows: 2 }} style={{ minHeight: 44, marginBottom: 8, fontSize: 12 }}>{c.description || '—'}</Typography.Paragraph>
              <Space size={4} wrap>
                <Tag color="blue">v{c.version}</Tag>
                {c.appVersion && <Tag>app {c.appVersion}</Tag>}
                <Typography.Text type="secondary" style={{ fontSize: 12 }}>{c.versions} 个版本</Typography.Text>
              </Space>
            </Card>
          </List.Item>
        )}
      />
    </>
  );
}

// ── 安装 ──────────────────────────────────────────────────────────────────────

function InstallModal({ cid, chart, onClose, onDone }: { cid: number; chart: HelmChart | null; onClose: () => void; onDone: () => void }) {
  const { message } = App.useApp();
  const api = helmApi(cid);
  const [form] = Form.useForm<{ name: string; namespace: string; version: string; values: string; createNamespace: boolean; wait: boolean }>();
  const version = Form.useWatch('version', form);
  const [saving, setSaving] = useState(false);
  const versions = useQuery({ queryKey: ['helm', cid, 'versions', chart?.repo, chart?.name], queryFn: () => api.versions(chart!.repo, chart!.name), enabled: !!chart });
  const detail = useQuery({
    queryKey: ['helm', cid, 'chart', chart?.repo, chart?.name, version ?? ''],
    queryFn: () => api.chart(chart!.repo, chart!.name, version || undefined),
    enabled: !!chart,
    staleTime: 5 * 60_000,
  });

  // 打开时重置；默认 values 到手后填入（用户未改动时）
  useEffect(() => {
    if (chart) form.setFieldsValue({ name: chart.name.slice(0, 53), namespace: 'default', version: chart.version, values: '', createNamespace: true, wait: false });
  }, [chart, form]);
  useEffect(() => {
    if (detail.data && !form.getFieldValue('values')) form.setFieldsValue({ values: detail.data.values });
  }, [detail.data, form]);

  const submit = async () => {
    if (!chart) return;
    const v = await form.validateFields();
    setSaving(true);
    try {
      const rel = await api.install({ ...v, repo: chart.repo, chart: chart.name, values: v.values, timeoutSeconds: 600 });
      message.success(`已安装 ${rel.name}（${rel.status}）`);
      onDone();
    } catch (e) {
      message.error(errMsg(e));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal title={chart ? `安装 ${chart.repo}/${chart.name}` : ''} open={!!chart} onCancel={onClose} onOk={submit} okText="安装" confirmLoading={saving} width={1000} destroyOnClose>
      <Row gutter={16}>
        <Col span={14}>
          <Form form={form} layout="vertical">
            <Row gutter={12}>
              <Col span={10}><Form.Item name="name" label="Release 名称" rules={[{ required: true }, { pattern: /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/, message: '小写字母 / 数字 / -' }]}><Input /></Form.Item></Col>
              <Col span={8}><Form.Item name="namespace" label="命名空间" rules={[{ required: true }]}><NamespaceSelect cid={cid} allowCreate /></Form.Item></Col>
              <Col span={6}>
                <Form.Item name="version" label="版本">
                  <Select showSearch loading={versions.isLoading} options={(versions.data ?? []).map((v) => ({ value: v.version, label: `${v.version}${v.appVersion ? ` (app ${v.appVersion})` : ''}` }))} />
                </Form.Item>
              </Col>
            </Row>
            <Form.Item name="values" label={<Space>values.yaml<Typography.Text type="secondary" style={{ fontSize: 12 }}>（已预填 chart 默认值，按需修改）</Typography.Text></Space>}>
              <Input.TextArea autoSize={{ minRows: 18, maxRows: 28 }} spellCheck={false} style={{ fontFamily: 'monospace', fontSize: 12 }} disabled={detail.isLoading} />
            </Form.Item>
            <Space size="large">
              <Form.Item name="createNamespace" valuePropName="checked" noStyle><Checkbox>命名空间不存在时创建</Checkbox></Form.Item>
              <Form.Item name="wait" valuePropName="checked" noStyle><Checkbox>等待资源就绪再返回（最长 10 分钟）</Checkbox></Form.Item>
            </Space>
          </Form>
        </Col>
        <Col span={10}>
          {detail.isError ? (
            <Alert type="error" message={errMsg(detail.error)} />
          ) : (
            <Card size="small" title="README" loading={detail.isLoading} style={{ maxHeight: 620, overflow: 'auto' }}>
              <Descriptions size="small" column={1} style={{ marginBottom: 8 }}>
                {chart?.home && <Descriptions.Item label="主页"><a href={chart.home} target="_blank" rel="noreferrer">{chart.home}</a></Descriptions.Item>}
                {chart?.keywords?.length ? <Descriptions.Item label="关键词">{chart.keywords.join(', ')}</Descriptions.Item> : null}
              </Descriptions>
              <pre style={{ whiteSpace: 'pre-wrap', fontSize: 12, lineHeight: 1.5 }}>{detail.data?.readme || '（chart 未提供 README）'}</pre>
            </Card>
          )}
        </Col>
      </Row>
    </Modal>
  );
}

// ── 升级 ──────────────────────────────────────────────────────────────────────

function UpgradeModal({ cid, release, onClose, onDone }: { cid: number; release: HelmRelease | null; onClose: () => void; onDone: () => void }) {
  const { message } = App.useApp();
  const api = helmApi(cid);
  const [form] = Form.useForm<{ repo: string; version: string; values: string; wait: boolean }>();
  const repo = Form.useWatch('repo', form);
  const [saving, setSaving] = useState(false);
  const repos = useQuery({ queryKey: ['helm', cid, 'repos'], queryFn: api.repos, enabled: !!release });
  const values = useQuery({ queryKey: ['helm', cid, 'values', release?.namespace, release?.name], queryFn: () => api.values(release!.namespace, release!.name), enabled: !!release });
  const versions = useQuery({
    queryKey: ['helm', cid, 'versions', repo, release?.chartName],
    queryFn: () => api.versions(repo!, release!.chartName),
    enabled: !!release && !!repo,
    retry: false,
  });

  // 猜仓库：哪个仓库有这个 chart 名就选它
  const guess = useMemo(() => {
    if (!release || !repos.data) return undefined;
    return repos.data[0]?.name;
  }, [release, repos.data]);
  useEffect(() => {
    if (release) form.setFieldsValue({ repo: guess, version: release.chartVersion, values: '', wait: false });
  }, [release, guess, form]);
  useEffect(() => {
    if (values.data && !form.getFieldValue('values')) form.setFieldsValue({ values: values.data.values });
  }, [values.data, form]);

  const submit = async () => {
    if (!release) return;
    const v = await form.validateFields();
    setSaving(true);
    try {
      const rel = await api.upgrade(release.namespace, release.name, { repo: v.repo, chart: release.chartName, version: v.version, values: v.values, wait: v.wait, timeoutSeconds: 600 });
      message.success(`已升级 ${rel.name} 到修订 ${rel.revision}`);
      onDone();
    } catch (e) {
      message.error(errMsg(e));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal title={release ? `升级 ${release.name}（${release.chart}）` : ''} open={!!release} onCancel={onClose} onOk={submit} okText="升级" confirmLoading={saving} width={760} destroyOnClose>
      <Form form={form} layout="vertical">
        <Row gutter={12}>
          <Col span={12}>
            <Form.Item name="repo" label="Chart 仓库" rules={[{ required: true }]} extra={versions.isError ? <Typography.Text type="danger" style={{ fontSize: 12 }}>该仓库没有 chart「{release?.chartName}」</Typography.Text> : undefined}>
              <Select options={(repos.data ?? []).map((r) => ({ value: r.name, label: r.name }))} />
            </Form.Item>
          </Col>
          <Col span={12}>
            <Form.Item name="version" label="目标版本" rules={[{ required: true }]}>
              <Select showSearch loading={versions.isLoading} options={(versions.data ?? []).map((v) => ({ value: v.version, label: `${v.version}${v.appVersion ? ` (app ${v.appVersion})` : ''}${v.version === release?.chartVersion ? '（当前）' : ''}` }))} />
            </Form.Item>
          </Col>
        </Row>
        <Form.Item name="values" label={<Space>values.yaml<Typography.Text type="secondary" style={{ fontSize: 12 }}>（已回填当前 release 的用户 values；本次以此为准，不合并旧值）</Typography.Text></Space>}>
          <Input.TextArea autoSize={{ minRows: 14, maxRows: 26 }} spellCheck={false} style={{ fontFamily: 'monospace', fontSize: 12 }} />
        </Form.Item>
        <Form.Item name="wait" valuePropName="checked" noStyle><Checkbox>等待资源就绪再返回</Checkbox></Form.Item>
      </Form>
    </Modal>
  );
}

// ── 仓库 ──────────────────────────────────────────────────────────────────────

function Repos({ cid }: { cid: number }) {
  const { message } = App.useApp();
  const qc = useQueryClient();
  const api = helmApi(cid);
  const q = useQuery({ queryKey: ['helm', cid, 'repos'], queryFn: api.repos });
  const [open, setOpen] = useState(false);
  const [form] = Form.useForm<{ name: string; url: string; username?: string; password?: string }>();
  const [saving, setSaving] = useState(false);
  const [busy, setBusy] = useState<string | null>(null);

  const invalidate = () => qc.invalidateQueries({ queryKey: ['helm', cid] });

  const add = async (v: { name: string; url: string; username?: string; password?: string }) => {
    setSaving(true);
    try {
      await api.addRepo(v);
      message.success(`已添加仓库 ${v.name}`);
      setOpen(false);
      form.resetFields();
      invalidate();
    } catch (e) {
      message.error(errMsg(e));
    } finally {
      setSaving(false);
    }
  };
  const del = async (r: HelmRepo) => {
    try {
      await api.deleteRepo(r.name);
      message.success(`已删除仓库 ${r.name}`);
      invalidate();
    } catch (e) {
      message.error(errMsg(e));
    }
  };
  const refresh = async (r: HelmRepo) => {
    setBusy(r.name);
    try {
      await api.refreshRepo(r.name);
      message.success(`已刷新 ${r.name} 索引`);
      invalidate();
    } catch (e) {
      message.error(errMsg(e));
    } finally {
      setBusy(null);
    }
  };

  const existing = new Set((q.data ?? []).map((r) => r.name));
  return (
    <>
      <Space style={{ marginBottom: 12 }} wrap>
        <Button type="primary" icon={<PlusOutlined />} onClick={() => setOpen(true)}>添加仓库</Button>
        <Button icon={<ReloadOutlined />} onClick={() => q.refetch()} loading={q.isFetching}>刷新</Button>
        <Typography.Text type="secondary">暂只支持 http(s) 仓库；OCI（oci://）后续支持。索引缓存 10 分钟。</Typography.Text>
      </Space>
      <Table<HelmRepo>
        rowKey="id"
        size="small"
        loading={q.isLoading}
        dataSource={q.data ?? []}
        pagination={false}
        scroll={{ x: 'max-content' }}
        columns={[
          { title: '名称', dataIndex: 'name', render: (v: string) => <b>{v}</b> },
          { title: 'URL', dataIndex: 'url', render: (v: string) => <a href={v} target="_blank" rel="noreferrer">{v}</a> },
          { title: '认证', width: 80, render: (_, r) => (r.hasAuth ? <Tag>Basic</Tag> : '-') },
          { title: '添加时间', width: 170, render: (_, r) => fmtTime(r.createdAt) },
          {
            title: '操作', width: 160,
            render: (_, r) => (
              <Space size={4}>
                <Button size="small" icon={<SyncOutlined />} loading={busy === r.name} onClick={() => refresh(r)}>刷新索引</Button>
                <Popconfirm title={`删除仓库 ${r.name}？`} description="不影响已部署的 release。" onConfirm={() => del(r)} okButtonProps={{ danger: true }}>
                  <Button size="small" danger icon={<DeleteOutlined />} />
                </Popconfirm>
              </Space>
            ),
          },
        ]}
      />
      <Typography.Title level={5} style={{ marginTop: 20 }}>常用公共仓库</Typography.Title>
      <List
        grid={{ gutter: 12, xs: 1, sm: 2, md: 3, lg: 4 }}
        dataSource={PRESET_REPOS}
        renderItem={(p) => (
          <List.Item>
            <Card size="small" title={p.name} extra={<Button size="small" type="link" disabled={existing.has(p.name) || saving} onClick={() => add({ name: p.name, url: p.url })}>{existing.has(p.name) ? '已添加' : '添加'}</Button>}>
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>{p.note}</Typography.Text>
              <div style={{ fontSize: 11, fontFamily: 'monospace', marginTop: 4, wordBreak: 'break-all' }}>{p.url}</div>
            </Card>
          </List.Item>
        )}
      />

      <Modal title="添加 Chart 仓库" open={open} onCancel={() => setOpen(false)} onOk={async () => add(await form.validateFields())} okText="添加" confirmLoading={saving} destroyOnClose>
        <Form form={form} layout="vertical">
          <Form.Item name="name" label="名称" rules={[{ required: true }, { pattern: /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/, message: '小写字母 / 数字 / -' }]}><Input placeholder="bitnami" /></Form.Item>
          <Form.Item name="url" label="URL" rules={[{ required: true, type: 'url' }]}><Input placeholder="https://charts.bitnami.com/bitnami" /></Form.Item>
          <Row gutter={12}>
            <Col span={12}><Form.Item name="username" label="用户名（可选）"><Input autoComplete="off" /></Form.Item></Col>
            <Col span={12}><Form.Item name="password" label="密码（可选）"><Input.Password autoComplete="new-password" /></Form.Item></Col>
          </Row>
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>添加时会拉取一次 index.yaml 验证可达性。</Typography.Text>
        </Form>
      </Modal>
    </>
  );
}
