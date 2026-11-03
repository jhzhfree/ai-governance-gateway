import { useState } from 'react';
import { Button, Dialog, Tag } from 'tdesign-react';
import type { IssuedKey } from '../../api';

export function SecretDialog({ issued, close }: { issued: IssuedKey; close: () => void }) {
  const [copied, setCopied] = useState(false);
  const [copyError, setCopyError] = useState(false);
  return (
    <Dialog header="密钥已创建" visible footer={false} onClose={close}>
      <div className="secret-panel">
        <Tag theme="warning" variant="light">
          仅展示本次
        </Tag>
        <p>请立即保存到应用的密钥管理系统。关闭此窗口后无法再次查看。</p>
        <label>
          新密钥
          <textarea aria-label="新密钥" readOnly value={issued.secret} rows={3} />
        </label>
        <div role="status">
          {copied
            ? '已复制，请妥善保存。'
            : copyError
              ? '无法自动复制，请手动选择并保存密钥。'
              : ''}
        </div>
        <div className="dialog-actions">
          <Button
            variant="outline"
            onClick={() => {
              void navigator.clipboard.writeText(issued.secret).then(
                () => setCopied(true),
                () => setCopyError(true),
              );
            }}
          >
            复制密钥
          </Button>
          <Button theme="primary" onClick={close}>
            我已保存，关闭
          </Button>
        </div>
      </div>
    </Dialog>
  );
}
