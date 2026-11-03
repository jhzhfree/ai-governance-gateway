import { Button } from 'tdesign-react';
import { ApiError } from '../api';

export function Mark() {
  return (
    <span className="mark" aria-hidden="true">
      <svg viewBox="0 0 28 28">
        <path d="M5 8 14 3 23 8v11l-9 6-9-6Z" />
        <path d="m5 8 9 6 9-6M14 14v11" />
      </svg>
    </span>
  );
}

export function ErrorBox({ error, retry }: { error: unknown; retry?: () => void }) {
  return (
    <div className="error-box" role="alert">
      <strong>{error instanceof Error ? error.message : '请求未完成，请稍后重试。'}</strong>
      {error instanceof ApiError && error.requestID && <small>请求编号：{error.requestID}</small>}
      {retry && (
        <Button variant="outline" size="small" onClick={retry}>
          重试
        </Button>
      )}
    </div>
  );
}

export function Empty({ title, description }: { title: string; description: string }) {
  return (
    <div className="empty">
      <span className="empty-icon" aria-hidden="true">
        ◇
      </span>
      <h3>{title}</h3>
      <p>{description}</p>
    </div>
  );
}
