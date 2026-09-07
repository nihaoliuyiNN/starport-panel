import { useEffect, useState } from 'react';
import { Navigate, Route, Routes } from 'react-router-dom';
import { getToken } from './api';
import Layout from './layout/Layout';
import Login from './pages/Login';
import Nodes from './pages/Nodes';
import Clusters from './pages/Clusters';
import ClusterDetail from './pages/cluster/ClusterDetail';
import Tasks from './pages/Tasks';
import Tokens from './pages/Tokens';

function useAuthed() {
  const [authed, setAuthed] = useState(!!getToken());
  useEffect(() => {
    const fn = () => setAuthed(!!getToken());
    window.addEventListener('starport-auth', fn);
    return () => window.removeEventListener('starport-auth', fn);
  }, []);
  return authed;
}

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
    <Routes>
      <Route element={<Layout />}>
        <Route path="/" element={<Navigate to="/nodes" replace />} />
        <Route path="/nodes" element={<Nodes />} />
        <Route path="/clusters" element={<Clusters />} />
        <Route path="/clusters/:id" element={<ClusterDetail />} />
        <Route path="/clusters/:id/:tab" element={<ClusterDetail />} />
        <Route path="/tasks" element={<Tasks />} />
        <Route path="/tokens" element={<Tokens />} />
        <Route path="*" element={<Navigate to="/nodes" replace />} />
      </Route>
    </Routes>
  );
}
