import type { ProviderConfiguration } from '../../api';
type Pricing = ProviderConfiguration['models'][number]['cache_pricing'];
export function EmbeddedCachePricing({
  value,
  inputPrice,
  nativeMessages = false,
  onChange,
}: {
  value: Pricing;
  inputPrice: number;
  nativeMessages?: boolean;
  onChange: (value: Pricing) => void;
}) {
  return (
    <div className="provider-cache-pricing">
      {nativeMessages ? (
        <p>原生 Messages 要求已审核的缓存命中计费。</p>
      ) : (
        <label>
          <input
            type="checkbox"
            checked={!!value}
            onChange={(e) =>
              onChange(
                e.target.checked
                  ? { read_per_million: inputPrice, write_per_million: 0 }
                  : undefined,
              )
            }
          />
          启用 DeepSeek 缓存命中计费
        </label>
      )}
      <p>
        输入单价用于未命中
        Token，命中部分使用独立单价。仅接受供应商明确的命中和未命中计数；缺失时保留未知费用占额。
        {nativeMessages
          ? '仅接受供应商明确为零的原始写入量；非零写入或额外计费类别暂不支持。'
          : '此契约不提供缓存写入计量。'}
      </p>
      {value && (
        <label>
          缓存命中单价（微元/百万 Token）
          <input
            type="number"
            min={0}
            max={9007199254740991}
            required
            value={value.read_per_million}
            onChange={(e) =>
              onChange({ read_per_million: Number(e.target.value), write_per_million: 0 })
            }
          />
        </label>
      )}
    </div>
  );
}
