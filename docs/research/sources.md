# 固定提交与源码证据索引

研究日期：2026-11-03。所有链接固定至本轮实际读取的 commit。HEAD 快照仅用于研究，不是已选稳定依赖版本。许可证列是根文件初查，不替代采用时的逐文件审查。

运行结果与设计推导见 [研究报告](findings.md)。

## Bifrost

- 源码提交：`056789f5df34f32654772999c2bd0cbecf27c20f`
- 许可证初查：Apache-2.0（根 LICENSE）。
- 阅读文件：
  - [core/schemas/provider.go](https://github.com/maximhq/bifrost/blob/056789f5df34f32654772999c2bd0cbecf27c20f/core/schemas/provider.go)
  - [core/schemas/plugin.go](https://github.com/maximhq/bifrost/blob/056789f5df34f32654772999c2bd0cbecf27c20f/core/schemas/plugin.go)
  - [core/bifrost.go](https://github.com/maximhq/bifrost/blob/056789f5df34f32654772999c2bd0cbecf27c20f/core/bifrost.go)
  - [core/abandonedstream_test.go](https://github.com/maximhq/bifrost/blob/056789f5df34f32654772999c2bd0cbecf27c20f/core/abandonedstream_test.go)
  - [core/go.mod](https://github.com/maximhq/bifrost/blob/056789f5df34f32654772999c2bd0cbecf27c20f/core/go.mod)
  - [LICENSE](https://github.com/maximhq/bifrost/blob/056789f5df34f32654772999c2bd0cbecf27c20f/LICENSE)

## LiteLLM

- 源码提交：`baddc67041e0888b8bcdc32d43e86c946c867c76`
- 许可证初查：根 LICENSE：enterprise/ 单独授权，其余按 MIT 声明。
- 阅读文件：
  - [litellm/proxy/spend_tracking/budget_reservation.py](https://github.com/BerriAI/litellm/blob/baddc67041e0888b8bcdc32d43e86c946c867c76/litellm/proxy/spend_tracking/budget_reservation.py)
  - [litellm/proxy/spend_tracking/cost_finalization.py](https://github.com/BerriAI/litellm/blob/baddc67041e0888b8bcdc32d43e86c946c867c76/litellm/proxy/spend_tracking/cost_finalization.py)
  - [litellm/proxy/middleware/budget_reservation_release_middleware.py](https://github.com/BerriAI/litellm/blob/baddc67041e0888b8bcdc32d43e86c946c867c76/litellm/proxy/middleware/budget_reservation_release_middleware.py)
  - [LICENSE](https://github.com/BerriAI/litellm/blob/baddc67041e0888b8bcdc32d43e86c946c867c76/LICENSE)

## Coze Studio

- 源码提交：`fefb05ff27be1da939612fbf9faf5db62583b8ae`
- 许可证初查：Apache-2.0（根 LICENSE-APACHE）。
- 阅读文件：
  - [backend/domain/app/service/publish_app.go](https://github.com/coze-dev/coze-studio/blob/fefb05ff27be1da939612fbf9faf5db62583b8ae/backend/domain/app/service/publish_app.go)
  - [backend/domain/app/repository/app.go](https://github.com/coze-dev/coze-studio/blob/fefb05ff27be1da939612fbf9faf5db62583b8ae/backend/domain/app/repository/app.go)
  - [frontend/apps/coze-studio/src/app.tsx](https://github.com/coze-dev/coze-studio/blob/fefb05ff27be1da939612fbf9faf5db62583b8ae/frontend/apps/coze-studio/src/app.tsx)
  - [frontend/apps/coze-studio/package.json](https://github.com/coze-dev/coze-studio/blob/fefb05ff27be1da939612fbf9faf5db62583b8ae/frontend/apps/coze-studio/package.json)
  - [LICENSE-APACHE](https://github.com/coze-dev/coze-studio/blob/fefb05ff27be1da939612fbf9faf5db62583b8ae/LICENSE-APACHE)

## Dify

- 源码提交：`eea40c683bbc8d2a396f80ee25ebfef1555a84ad`
- 许可证初查：修改版 Apache-2.0，有多租户与前端标识附加条件。
- 阅读文件：
  - [api/core/app/public_runtime.py](https://github.com/langgenius/dify/blob/eea40c683bbc8d2a396f80ee25ebfef1555a84ad/api/core/app/public_runtime.py)
  - [web/package.json](https://github.com/langgenius/dify/blob/eea40c683bbc8d2a396f80ee25ebfef1555a84ad/web/package.json)
  - [LICENSE](https://github.com/langgenius/dify/blob/eea40c683bbc8d2a396f80ee25ebfef1555a84ad/LICENSE)

## TDesign React

- 源码提交：`d10a8eb4a04b7282f3d74061b2fc7dcb5330c6ba`
- 许可证初查：MIT（根 LICENSE）。
- 阅读文件：
  - [packages/components/config-provider/ConfigProvider.tsx](https://github.com/Tencent/tdesign-react/blob/d10a8eb4a04b7282f3d74061b2fc7dcb5330c6ba/packages/components/config-provider/ConfigProvider.tsx)
  - [packages/components/table/_example/pagination-ajax.tsx](https://github.com/Tencent/tdesign-react/blob/d10a8eb4a04b7282f3d74061b2fc7dcb5330c6ba/packages/components/table/_example/pagination-ajax.tsx)
  - [LICENSE](https://github.com/Tencent/tdesign-react/blob/d10a8eb4a04b7282f3d74061b2fc7dcb5330c6ba/LICENSE)

## Bubble Tea

- 源码提交：`67b4c7a8a2b34a0b1ce15261b0c705bc7e1a04b7`
- 许可证初查：MIT（根 LICENSE）。
- 阅读文件：
  - [tea.go](https://github.com/charmbracelet/bubbletea/blob/67b4c7a8a2b34a0b1ce15261b0c705bc7e1a04b7/tea.go)
  - [tea_test.go](https://github.com/charmbracelet/bubbletea/blob/67b4c7a8a2b34a0b1ce15261b0c705bc7e1a04b7/tea_test.go)
  - [go.mod](https://github.com/charmbracelet/bubbletea/blob/67b4c7a8a2b34a0b1ce15261b0c705bc7e1a04b7/go.mod)
  - [LICENSE](https://github.com/charmbracelet/bubbletea/blob/67b4c7a8a2b34a0b1ce15261b0c705bc7e1a04b7/LICENSE)

## Cobra

- 源码提交：`adbc8813901bba65827259daa8e22ff94ec1f30e`
- 许可证初查：Apache-2.0（根 LICENSE）。
- 阅读文件：
  - [command.go](https://github.com/spf13/cobra/blob/adbc8813901bba65827259daa8e22ff94ec1f30e/command.go)
  - [command_test.go](https://github.com/spf13/cobra/blob/adbc8813901bba65827259daa8e22ff94ec1f30e/command_test.go)
  - [go.mod](https://github.com/spf13/cobra/blob/adbc8813901bba65827259daa8e22ff94ec1f30e/go.mod)
  - [LICENSE](https://github.com/spf13/cobra/blob/adbc8813901bba65827259daa8e22ff94ec1f30e/LICENSE)

## Casdoor

- 源码提交：`04a36b79e3d8f19cc70b68d617edabccd9a9cc83`
- 许可证初查：Apache-2.0（根 LICENSE）。
- 阅读文件：
  - [controllers/token.go](https://github.com/casdoor/casdoor/blob/04a36b79e3d8f19cc70b68d617edabccd9a9cc83/controllers/token.go)
  - [object/token_oauth.go](https://github.com/casdoor/casdoor/blob/04a36b79e3d8f19cc70b68d617edabccd9a9cc83/object/token_oauth.go)
  - [object/device_auth_store.go](https://github.com/casdoor/casdoor/blob/04a36b79e3d8f19cc70b68d617edabccd9a9cc83/object/device_auth_store.go)
  - [go.mod](https://github.com/casdoor/casdoor/blob/04a36b79e3d8f19cc70b68d617edabccd9a9cc83/go.mod)
  - [LICENSE](https://github.com/casdoor/casdoor/blob/04a36b79e3d8f19cc70b68d617edabccd9a9cc83/LICENSE)
