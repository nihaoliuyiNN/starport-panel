import { Layout as AntLayout, Menu, Button, Space, Typography, Tooltip } from 'antd';
import { ClusterOutlined, DesktopOutlined, KeyOutlined, LogoutOutlined, UnorderedListOutlined } from '@ant-design/icons';
import { Outlet, useLocation, useNavigate } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { api, setToken } from '../api';

const { Sider, Header, Content } = AntLayout;

const items = [
  { key: '/nodes', icon: <DesktopOutlined />, label: '节点' },
  { key: '/clusters', icon: <ClusterOutlined />, label: '集群' },
  { key: '/tasks', icon: <UnorderedListOutlined />, label: '任务' },
  { key: '/tokens', icon: <KeyOutlined />, label: 'API 令牌' },
];

export default function Layout() {
  const nav = useNavigate();
  const loc = useLocation();
  const selected = items.map((i) => i.key).find((k) => loc.pathname.startsWith(k)) ?? '/nodes';
  const version = useQuery({ queryKey: ['version'], queryFn: () => api.get<{ version: string }>('/version'), staleTime: Infinity });

  return (
    <AntLayout style={{ minHeight: '100vh' }}>
      <Sider width={200} theme="dark">
        <div style={{ height: 56, display: 'flex', alignItems: 'center', padding: '0 20px', color: '#fff', fontWeight: 600, fontSize: 16, letterSpacing: 0.5 }}>
          <span style={{ display: 'inline-block', width: 10, height: 10, borderRadius: 5, background: '#52c41a', marginRight: 10 }} />
          Starport Panel
        </div>
        <Menu theme="dark" mode="inline" selectedKeys={[selected]} items={items} onClick={(e) => nav(e.key)} />
      </Sider>
      <AntLayout>
        <Header style={{ background: '#fff', padding: '0 24px', display: 'flex', alignItems: 'center', justifyContent: 'flex-end', borderBottom: '1px solid #f0f0f0', height: 56 }}>
          <Space>
            <Typography.Text type="secondary">panel {version.data?.version ?? ''}</Typography.Text>
            <Tooltip title="清除本地令牌并退出">
              <Button icon={<LogoutOutlined />} type="text" onClick={() => setToken('')}>退出</Button>
            </Tooltip>
          </Space>
        </Header>
        <Content style={{ padding: 24 }}>
          <Outlet />
        </Content>
      </AntLayout>
    </AntLayout>
  );
}
