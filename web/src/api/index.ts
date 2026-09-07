import { api } from './client';
import type {
  APIToken, Applied, Cluster, ClusterDetail, CreateClusterRequest, Deployment, Ingress, K8sEvent, K8sNode,
  Namespace, Node, Pod, ResourceItem, ResourceKind, Role, Service, Task, TaskLog,
} from './types';

export * from './client';
export * from './types';

const q = (params: Record<string, string | number | undefined>) => {
  const s = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) if (v !== undefined && v !== '') s.set(k, String(v));
  const str = s.toString();
  return str ? `?${str}` : '';
};

export const nodesApi = {
  list: () => api.get<Node[]>('/nodes'),
  get: (id: number) => api.get<Node>(`/nodes/${id}`),
  exec: (id: number, script: string, timeoutMs?: number) => api.post<{ taskId: number }>(`/nodes/${id}/exec`, { script, timeoutMs }),
};

export const clustersApi = {
  list: () => api.get<Cluster[]>('/clusters'),
  get: (id: number) => api.get<ClusterDetail>(`/clusters/${id}`),
  create: (req: CreateClusterRequest) => api.post<Cluster>('/clusters', req),
  remove: (id: number, force: boolean) => api.del<{ taskIds: number[] }>(`/clusters/${id}${q({ force: force ? 'true' : undefined })}`),
  addNode: (id: number, nodeId: number, role: Role) => api.post<{ taskId: number }>(`/clusters/${id}/nodes`, { nodeId, role }),
  removeNode: (id: number, nodeId: number) => api.del<{ taskId: number }>(`/clusters/${id}/nodes/${nodeId}`),
  kubeconfig: (id: number) => api.get<string>(`/clusters/${id}/kubeconfig`),
};

export const tasksApi = {
  list: (nodeId?: number, limit = 100) => api.get<Task[]>(`/tasks${q({ nodeId, limit })}`),
  get: (id: number) => api.get<Task>(`/tasks/${id}`),
  logs: (id: number, after = 0, limit = 1000) => api.get<{ task: Task; logs: TaskLog[] }>(`/tasks/${id}/logs${q({ after, limit })}`),
  cancel: (id: number) => api.post<void>(`/tasks/${id}/cancel`),
};

export const tokensApi = {
  list: () => api.get<APIToken[]>('/tokens'),
  create: (name: string) => api.post<{ token: APIToken; plain: string }>('/tokens', { name }),
  revoke: (id: number) => api.del(`/tokens/${id}`),
};

export const k8sApi = (cid: number) => {
  const base = `/clusters/${cid}/k8s`;
  const gvr = (g: string, v: string, r: string) => `${base}/resources/${g || 'core'}/${v}/${r}`;
  return {
    nodes: () => api.get<K8sNode[]>(`${base}/nodes`),
    namespaces: () => api.get<Namespace[]>(`${base}/namespaces`),
    pods: (namespace?: string) => api.get<Pod[]>(`${base}/pods${q({ namespace })}`),
    deletePod: (ns: string, name: string) => api.del(`${base}/namespaces/${ns}/pods/${name}`),
    deployments: (namespace?: string) => api.get<Deployment[]>(`${base}/deployments${q({ namespace })}`),
    scale: (ns: string, name: string, replicas: number) => api.post<void>(`${base}/namespaces/${ns}/deployments/${name}/scale`, { replicas }),
    restart: (ns: string, name: string) => api.post<void>(`${base}/namespaces/${ns}/deployments/${name}/restart`),
    expose: (ns: string, name: string, req: { name?: string; type: string; port: number; targetPort?: number; nodePort?: number; protocol?: string }) =>
      api.post<Service>(`${base}/namespaces/${ns}/deployments/${name}/expose`, req),
    services: (namespace?: string) => api.get<Service[]>(`${base}/services${q({ namespace })}`),
    deleteService: (ns: string, name: string) => api.del(`${base}/namespaces/${ns}/services/${name}`),
    ingresses: (namespace?: string) => api.get<Ingress[]>(`${base}/ingresses${q({ namespace })}`),
    events: (namespace?: string, object?: string) => api.get<K8sEvent[]>(`${base}/events${q({ namespace, object })}`),
    apply: (yaml: string, namespace?: string) => api.postYaml<{ applied: Applied[] }>(`${base}/apply${q({ namespace })}`, yaml),
    deleteManifest: (yaml: string, namespace?: string) => api.postYaml<{ deleted: Applied[] }>(`${base}/delete${q({ namespace })}`, yaml),
    resourceKinds: () => api.get<ResourceKind[]>(`${base}/resource-kinds`),
    listResources: (g: string, v: string, r: string, namespace?: string) => api.get<ResourceItem[]>(`${gvr(g, v, r)}${q({ namespace })}`),
    getResource: (g: string, v: string, r: string, name: string, namespace?: string) => api.get<string>(`${gvr(g, v, r)}/${name}${q({ namespace })}`),
    deleteResource: (g: string, v: string, r: string, name: string, namespace?: string) => api.del(`${gvr(g, v, r)}/${name}${q({ namespace })}`),
    logsPath: (ns: string, name: string) => `${base}/namespaces/${ns}/pods/${name}/logs`,
    execPath: (ns: string, name: string) => `${base}/namespaces/${ns}/pods/${name}/exec`,
  };
};
