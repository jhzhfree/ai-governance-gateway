# GitHub交付状态与推送

目标：私有仓库`jhzhfree/ai-governance-gateway`，开发分支`codex/product-foundation`。

首次交付时因目标仓库为Public而暂停。本轮用户在收到可见性说明后明确要求“提交到目标仓库”，按当前目标仓库授权推进。源码、测试与设计提交独立分支，不修改仓库可见性，不上传原始内部PPT、密钥、运行数据库或客户正文。

远端已有初始化README，提交以远端主分支为父提交构建产品分支，保留主分支原状。使用连接的GitHub提交接口写入后核验远端文件及分支SHA。本地原型提交历史与远端初始化历史不同，不强推覆盖，也不修改AKB/Octop或其他工程。

已存在空仓库时，本地用户可执行：

```bash
git remote add origin https://github.com/jhzhfree/ai-governance-gateway.git
git push -u origin codex/product-foundation
```

使用已有非空仓库时，应先获取并比较远端默认分支，在独立工作分支集成本项目，保留对方原文件；禁止覆盖主分支或force push。推送后检查远端SHA及CI状态。当前源码包附带Git bundle用于恢复本轮本地提交历史。

原创代码未默认开源授权。商业设计不含客户密钥或原始内部PPT；原调研报告未原样复制到代码仓库，以免混入内部经营和人员信息。
