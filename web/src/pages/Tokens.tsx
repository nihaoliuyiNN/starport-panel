import { useState } from 'react';
import { App, Alert, Button, Card, Form, Input, Modal, Popconfirm, Space, Table, Tag, Typography } from 'antd';
import { CopyOutlined, PlusOutlined, ReloadOutlined } from '@ant-design/icons';
import { useQuery } from '@tanstack/react-query';
import { errMsg, tokensApi, type APIToken } from '../api';
import { copyText, fmtTime, fromNow } from '../util';

export default function Tokens() {
  const { message } = App.useApp();
  const q = useQuery({ queryKey: ['tokens'], queryFn: tokensApi.list });
  const [open, setOpen] = useState(false);
  const [plain, setPlain] = useState('');
  const [form] = Form.useForm<{ name: string }>();

  const create = async () => {
    const v = await form.validateFields();
    try {
      const res = await tokensApi.create(v.name);
      setPlain(res.plain);
      setOpen(false);
      form.resetFields();
      q.refetch();
    } catch (e) {
      message.error(errMsg(e));
    }
  };
  const revoke = async (t: APIToken) => {
    try {
      await tokensApi.revoke(t.id);
      message.success(`已吊销 ${t.name}`);
      q.refetch();
    } catch (e) {
      message.error(errMsg(e));
    }
  };

  return (
    <>
      <Card
        title="API 令牌"
        extra={<Space><Button type="primary" icon={<PlusOutlined />} onClick={() => setOpen(true)}>签发令牌</Button><Button icon={<ReloadOutlined />} onClick={() => q.refetch()} loading={q.isFetching}>刷新</Button></Space>}
      >
        <Typography.Paragraph type="secondary">任一有效令牌拥有全部权限（含管理令牌）。吊销当前正在使用的令牌会立刻让本页面掉线。</Typography.Paragraph>
        <Table<APIToken>
          rowKey="id"
          size="middle"
          loading={q.isLoading}
          dataSource={q.data ?? []}
          scroll={{ x: 'max-content' }}
          pagination={false}
          columns={[
            { title: 'ID', dataIndex: 'id', width: 60 },
            { title: '名称', dataIndex: 'name' },
            { title: '前缀', dataIndex: 'prefix', width: 160, render: (v: string) => <code>{v}…</code> },
            { title: '状态', width: 100, render: (_, t) => (t.revokedAt && !t.revokedAt.startsWith('0001-') ? <Tag>已吊销</Tag> : <Tag color="success">有效</Tag>) },
            { title: '创建', width: 170, render: (_, t) => fmtTime(t.createdAt) },
            { title: '最近使用', width: 120, render: (_, t) => fromNow(t.lastUsedAt) },
            {
              title: '', width: 80,
              render: (_, t) => (!t.revokedAt || t.revokedAt.startsWith('0001-')) && (
                <Popconfirm title={`吊销 ${t.name}？`} okText="吊销" okButtonProps={{ danger: true }} onConfirm={() => revoke(t)}>
                  <Button size="small" danger>吊销</Button>
                </Popconfirm>
              ),
            },
          ]}
        />
      </Card>

      <Modal title="签发令牌" open={open} onCancel={() => setOpen(false)} onOk={create} okText="签发" destroyOnClose>
        <Form form={form} layout="vertical"><Form.Item name="name" label="名称" rules={[{ required: true, message: '请输入名称' }]}><Input placeholder="ci / admin-ui / java-cloud" autoFocus /></Form.Item></Form>
      </Modal>

      <Modal title="令牌已签发" open={!!plain} onCancel={() => setPlain('')} footer={<Button type="primary" onClick={() => setPlain('')}>我已保存</Button>}>
        <Alert type="warning" showIcon message="明文只显示这一次，关闭后无法再次查看。" style={{ marginBottom: 12 }} />
        <Space.Compact style={{ width: '100%' }}>
          <Input value={plain} readOnly style={{ fontFamily: 'monospace' }} />
          <Button icon={<CopyOutlined />} onClick={async () => { if (await copyText(plain)) message.success('已复制'); }} />
        </Space.Compact>
      </Modal>
    </>
  );
}
