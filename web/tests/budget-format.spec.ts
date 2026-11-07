import { expect, test } from '@playwright/test';
import { budgetAmount } from '../src/shared/format';

test('budget display preserves micro-yuan evidence and simulation units', () => {
  expect(budgetAmount(1, 'CNY_MICRO')).toBe('0.000001');
  expect(budgetAmount(1_230_040, 'CNY_MICRO')).toBe('1.23004');
  expect(budgetAmount(1_000_000_000, 'CNY_MICRO')).toBe('1,000.00');
  expect(budgetAmount(-1, 'CNY_MICRO')).toBe('-0.000001');
  expect(budgetAmount(Number.MAX_SAFE_INTEGER, 'CNY_MICRO')).toBe('9,007,199,254.740991');
  expect(budgetAmount(Number.MAX_SAFE_INTEGER + 1, 'CNY_MICRO')).toBe('—');
  expect(budgetAmount(0.5, 'CNY_MICRO')).toBe('—');
  expect(budgetAmount(1000, 'MOCK')).toBe('1,000');
});
