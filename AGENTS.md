# AGENTS.md - CFData-WEB 开发指南

## Go 工具链
- 路径: `/usr/local/go/bin/go`
- 版本: Go 1.26.2

## 编译测试版
每次修改完成后，主动编译测试版到 `release_assets/cfdata-test`，覆盖原文件：
```bash
mkdir -p /root/project/CFData-WEB/release_assets && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 /usr/local/go/bin/go build -trimpath -ldflags="-s -w" -o /tmp/cfdata-test-tmp . && mv /tmp/cfdata-test-tmp /root/project/CFData-WEB/release_assets/cfdata-test
```
注意：Go 模块在 `combined_refactor/` 下独立，须在此目录下构建。

## Git 操作规则
- **禁止**在未获得明确许可前执行 `git commit` 或 `git push`
- **禁止**在任何情况下操作 `main` 主分支，主分支仅限用户本人操作
- 所有修改均在 `beta` 等非主分支上完成

## 禁止修改的文件
- `.github/` 目录下的任何文件（GitHub Actions 配置）
- 顶层目录中注释包含"原版源码"的文件
