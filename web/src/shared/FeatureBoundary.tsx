import { Component, Suspense, type ReactNode } from 'react';
import { Button } from 'tdesign-react';

// Keep navigation usable when a deployment or connection interrupts a chunk load.
// Never display raw module URLs or application exception details to the user.
export class FeatureBoundary extends Component<{ children: ReactNode }, { failed: boolean }> {
  state = { failed: false };

  static getDerivedStateFromError() {
    return { failed: true };
  }

  render() {
    if (this.state.failed)
      return (
        <section className="panel feature-failure" role="alert">
          <h2>页面暂时无法显示</h2>
          <p>请检查网络后重新加载页面。尚未提交的表单内容将不会保留。</p>
          <Button variant="outline" onClick={() => window.location.reload()}>
            重新加载页面
          </Button>
        </section>
      );
    return (
      <Suspense
        fallback={
          <div className="loading" role="status">
            正在加载功能页面…
          </div>
        }
      >
        {this.props.children}
      </Suspense>
    );
  }
}
