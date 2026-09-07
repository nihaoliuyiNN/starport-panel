import { lazy, Suspense, useEffect, useState } from 'react';
import { Navigate, Route, Routes } from 'react-router-dom';
import { Spin } from 'antd';
import { getToken } from './api';
import Layout from './layout/Layout';
import Login from './pages/Login';

// 路由级懒加载：集群详情（含 xterm / 工作负载 / Helm）体量最大，单独成块
const Nodes = lazy(() => import('./pages/Nodes'));
const Clusters = lazy(() => import('./pages/Clusters'));
const ClusterDetail = lazy(() => import('./pages/cluster/ClusterDetail'));
const Tasks = lazy(() => import('./pages/Tasks'));
const Tokens = lazy(() => import('./pages/Tokens'));
const Audit = lazy(() => import('./pages/Audit'));

function useAuthed() {
  const [authed, setAuthed] = useState(!!getToken());
  useEffect(() => {
    const fn = () => setAuthed(!!getToken());
    window.addEventListener('starport-auth', fn);
    return () => window.removeEventListener('starport-auth', fn);
  }, []);
  return authed;
}

const Loading = () => (
  <div style={{ padding: 80, textAlign: 'center' }}>
    <Spin />
  </div>
);

export default function App() {
  const authed = useAuthed();
  if (!authed) {
    return (
      <Routes>
        <Route path="*" element={<Login />} />
      </Routes>
    );
  }
  return (
    <Suspense fallback={<Loading />}>
      <Routes>
        <Route element={<Layout />}>
          <Route path="/" element={<Navigate to="/nodes" replace />} />
          <Route path="/nodes" element={<Nodes />} />
          <Route path="/clusters" element={<Clusters />} />
          <Route path="/clusters/:id" element={<ClusterDetail />} />
          <Route path="/clusters/:id/:tab" element={<ClusterDetail />} />
          <Route path="/tasks" element={<Tasks />} />
          <Route path="/tokens" element={<Tokens />} />
          <Route path="/audit" element={<Audit />} />
          <Route path="*" element={<Navigate to="/nodes" replace />} />
        </Route>
      </Routes>
    </Suspense>
  );
}
