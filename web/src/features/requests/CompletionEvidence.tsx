import { Tag } from 'tdesign-react';
import type { RequestRecord } from '../../api';
import { budgetAmount, number } from '../../shared/format';

type Evidence = NonNullable<RequestRecord['embedded_completion']>;
const labels: Record<Evidence['state'], string> = {
  awaiting_usage: '未取得完成证据',
  recorded: '完成证据已记录',
  receipted: '已生成结算回执',
};
const notes: Record<Evidence['state'], string> = {
  awaiting_usage: '尚无可信完成用量，请结合账本状态核对；缺失数据不会自动按零费用处理。',
  recorded: '原始用量已持久化，尚无结算回执。请结合账本状态核对；恢复任务不会重新调用供应商。',
  receipted: '已按派发时价格完成记账。回执不代表客户端收到成功响应，也不改变原有执行与审计结果。',
};
const tokens = (value: number | null) => (value === null ? '未取得' : number(value));
const timestamp = (value: string | null) =>
  value === null ? '未取得' : new Date(value).toLocaleString('zh-CN');

export function CompletionEvidence({
  evidence,
  currency,
}: {
  evidence: Evidence;
  currency: string;
}) {
  const unit = currency === 'CNY_MICRO' ? '元' : currency;
  return (
    <section className="completion-evidence" aria-label="供应商完成证据">
      <div className="panel-heading">
        <div>
          <h4>供应商完成证据</h4>
          <p>单进程执行 · 文本 Chat · 沿用派发价格快照</p>
        </div>
        <Tag theme={evidence.state === 'receipted' ? 'success' : 'warning'} variant="light">
          {labels[evidence.state]}
        </Tag>
      </div>
      <p className="request-note">{notes[evidence.state]}</p>
      <dl>
        <dt>记录输入 / 输出 Token</dt>
        <dd>
          {tokens(evidence.input_tokens)} / {tokens(evidence.output_tokens)}
        </dd>
        <dt>冻结输入 / 输出单价</dt>
        <dd>
          {budgetAmount(evidence.input_per_million, currency)} /{' '}
          {budgetAmount(evidence.output_per_million, currency)} {unit} / 百万 Token
        </dd>
        <dt>回执金额</dt>
        <dd>
          {evidence.actual === null
            ? '尚无结算回执'
            : `${budgetAmount(evidence.actual, currency)} ${unit}`}
        </dd>
        <dt>完成证据记录时间</dt>
        <dd>{timestamp(evidence.recorded_at)}</dd>
        <dt>结算回执时间</dt>
        <dd>{timestamp(evidence.received_at)}</dd>
      </dl>
    </section>
  );
}
