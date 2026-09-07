// 与 internal/panel 的 JSON 结构一一对应（见 docs/api.md）。

export interface Facts {
  hostname: string;
  internalIp: string;
  os: string;
  arch: string;
  kernel?: string;
  cpuCores: number;
  memBytes: number;
  cpuUsedPercent: number;
  memUsedPercent: number;
}

export interface Node {
  id: number;
  facts: Facts;
  agentVersion: string;
  online: boolean;
  registeredAt: string;
  lastSeenAt: string;
}

export type ClusterStatus = 'created' | 'installing' | 'ready' | 'failed';
export type MemberStatus = 'installing' | 'ready' | 'failed' | 'removing';
export type Role = 'first-master' | 'join-master' | 'worker';

export interface Cluster {
  id: number;
  name: string;
  k8sVersion: string;
  podCIDR: string;
  serviceCIDR: string;
  controlPlaneEndpoint: string;
  vip: string;
  vipInterface: string;
  cni: string;
  cniVersion: string;
  addons: string[];
  artifactMode: 'bundle' | 'online';
  bundleUrl: string;
  useCnMirror: boolean;
  status: ClusterStatus;
  createdAt: string;
  updatedAt: string;
}

export interface Member {
  clusterId: number;
  nodeId: number;
  role: Role;
  status: MemberStatus;
  error: string;
  taskId: number;
  updatedAt: string;
}

export interface ClusterDetail extends Cluster {
  members: Member[];
}

export interface CreateClusterRequest {
  name: string;
  k8sVersion?: string;
  podCIDR?: string;
  serviceCIDR?: string;
  controlPlaneEndpoint?: string;
  vip?: string;
  vipInterface?: string;
  cni?: string;
  cniVersion?: string;
  addons?: string[];
  artifactMode?: 'bundle' | 'online';
  bundleUrl?: string;
  useCnMirror?: boolean;
}

export type TaskStatus = 'running' | 'succeeded' | 'failed' | 'cancelled';

export interface Task {
  id: number;
  kind: string;
  nodeId: number;
  clusterId: number;
  status: TaskStatus;
  exitCode: number;
  errorCode: string;
  errorMessage: string;
  startedAt: string;
  finishedAt: string;
}

export interface TaskLog {
  seq: number;
  at: string;
  line: string;
}

export interface APIToken {
  id: number;
  name: string;
  prefix: string;
  createdAt: string;
  lastUsedAt?: string;
  revokedAt?: string;
}

// ── k8s ──

export interface K8sNode {
  name: string;
  ready: boolean;
  unschedulable: boolean;
  roles: string[];
  internalIp: string;
  kubeletVersion: string;
  osImage: string;
  kernel: string;
  containerRuntime: string;
  cpu: string;
  memory: string;
  pods: string;
  createdAt: string;
}

export interface Namespace {
  name: string;
  status: string;
  createdAt: string;
}

export interface Container {
  name: string;
  image: string;
  ready: boolean;
  state: string;
}

export interface Pod {
  namespace: string;
  name: string;
  phase: string;
  ready: string;
  restarts: number;
  node: string;
  podIp: string;
  containers: Container[];
  createdAt: string;
}

export interface Deployment {
  namespace: string;
  name: string;
  replicas: number;
  ready: number;
  updated: number;
  available: number;
  images: string[];
  labels: Record<string, string>;
  createdAt: string;
}

export interface ServicePort {
  name?: string;
  protocol: string;
  port: number;
  targetPort: string;
  nodePort?: number;
}

export interface Service {
  namespace: string;
  name: string;
  type: string;
  clusterIp: string;
  externalIps: string[];
  ports: ServicePort[];
  selector: Record<string, string>;
  createdAt: string;
}

export interface IngressRule {
  host: string;
  path: string;
  service: string;
  port: string;
}

export interface Ingress {
  namespace: string;
  name: string;
  class: string;
  rules: IngressRule[];
  tlsHosts: string[];
  addresses: string[];
  createdAt: string;
}

export interface K8sEvent {
  type: string;
  reason: string;
  message: string;
  object: string;
  count: number;
  firstSeen: string;
  lastSeen: string;
}

export interface ResourceKind {
  group: string;
  version: string;
  resource: string;
  kind: string;
  namespaced: boolean;
  verbs: string[];
}

export interface ResourceItem {
  namespace?: string;
  name: string;
  labels: Record<string, string>;
  createdAt: string;
}

export interface Applied {
  kind: string;
  namespace?: string;
  name: string;
}
