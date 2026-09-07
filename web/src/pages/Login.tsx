import { useState } from 'react';
import { Button, Card, Form, Input, Typography, Alert } from 'antd';
import { errMsg, setToken } from '../api';

/** 令牌登录：把 API 令牌存进 localStorage，先用 /version 验一次再放行（直接 fetch，避免 401 触发全局登出）。 */
export default function Login() {
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');

  const submit = async ({ token }: { token: string }) => {
    setLoading(true);
    setError('');
    try {
      const t = token.trim();
      const res = await fetch('/api/v1/version', { headers: { Authorization: `Bearer ${t}` } });
      if (!res.ok) {
        const body = await res.json().catch(() => null);
        throw new Error(body?.error?.message ?? `HTTP ${res.status}`);
      }
      setToken(t);
    } catch (e) {
      setError(errMsg(e));
    } finally {
      setLoading(false);
    }
  };

  return (
    <div style={{ minHeight: '100vh', display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
      <Card style={{ width: 420 }}>
        <Typography.Title level={3} style={{ marginTop: 0 }}>Starport Panel</Typography.Title>
        <Typography.Paragraph type="secondary">
          输入 API 令牌登录。令牌由面板管理员在服务器上签发：
          <code style={{ display: 'block', marginTop: 6 }}>starport-panel token create --name admin</code>
        </Typography.Paragraph>
        {error && <Alert type="error" showIcon message={error} style={{ marginBottom: 16 }} />}
        <Form onFinish={submit} layout="vertical">
          <Form.Item name="token" label="API 令牌" rules={[{ required: true, message: '请输入令牌' }]}>
            <Input.Password placeholder="spt_..." autoFocus autoComplete="off" />
          </Form.Item>
          <Button type="primary" htmlType="submit" loading={loading} block>登录</Button>
        </Form>
      </Card>
    </div>
  );
}
