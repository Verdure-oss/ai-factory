# 把 issue-fix 工作流改造成 Codex Marketplace 插件（多 skill · 热更新）

- 日期：2026-08-28
- 状态：设计已批准，待写执行计划
- 相关：委托模式（`ai-factory-agent codex`）、`skills/issue-fix/`、第三方网关接入（v0.1.7）

## 1. 背景与问题

当前委托模式把工作流"skill"这样交给 Codex：

- 源文件 `skills/issue-fix/SKILL.md` → K8s ConfigMap `issue-fix-skill`
  （由 deploy/upgrade/update-config 脚本 `--from-file` 创建）
- 通过 **`subPath`** 只读挂到 go-dev pod 的 `/opt/ai-factory/skills/issue-fix/SKILL.md`
- `ai-factory-agent` 在 prompt 里注入一句"请阅读并遵循该文件"。**Codex 根本没用它的
  plugin/skill 系统**，只是把它当普通文件读。

两个痛点：

1. **改 skill 要重建 go-dev。** 根因是 `subPath` 挂载——它是唯一不热更新的挂载类型。
2. **只有单个 SKILL.md。** 无法按身份/角色（speccer / builder / reviewer …）把工作流拆开。

### 目标

- **真·Codex 插件**：通过 marketplace 正式注册，一个插件内含**多个 skill**（按角色拆）。
- **改完不重建 go-dev**：编辑插件 → 生效，无需重建预热 pod 或镜像。

## 2. Spike 结论（已在 go-dev pod 实测，非推断）

1. **headless `codex exec` 能自动发现并使用插件里的 skill。** 造了含两个 skill 的假插件，
   `marketplace add` + `plugin add` 后直接 `codex exec` 提问，模型**自主**按 skill 的
   `description` 找到并读取对应 `SKILL.md`，返回了两个 skill 各自的标记值。多 skill 全部加载。
2. **最小可用结构**（见 §4.1）。
3. **`codex plugin add` 是重拷贝语义，同版本号也刷新**：改了源未 bump 版本，重跑
   `plugin add`，cache 内容即更新。→ 刷新 = git 场景 `marketplace upgrade`（拉新快照）
   + `plugin add`（重拷进 cache）。
4. **集成坑（关键）**：`plugin add` 会把 `[marketplaces.*]` 和 `[plugins."x@y"]` 两个表
   写进 `$CODEX_HOME/config.toml`——正是 `maybe_write_codex_config()` 管的文件。TOML 规则下
   **根键（`model` / `model_provider`）必须在任何表头之前**，否则被算进上一个表。两者顺序与
   "不互相覆盖"必须在 `run_codex` 里安排好。

## 3. 总体架构

```
独立插件仓库 (Verdure-oss/ai-factory-codex-plugins)      ← 单一事实源，push 即"发布"
  .claude-plugin/marketplace.json
  plugins/issue-fix/
    .codex-plugin/plugin.json
    skills/<role>/SKILL.md  (多个)
        │  git push
        ▼
go-dev pod：ai-factory-agent 的 run_codex（codex exec 之前）
  codex plugin marketplace add  <source>   # 幂等确保源已注册（首次/新 pod）
  codex plugin marketplace upgrade         # git fetch 最新快照
  codex plugin add issue-fix@<marketplace> # 重拷进 CODEX_HOME/plugins/cache（同版本也刷新）
        │
  codex exec ...   → 本次任务用上最新的多 skill 工作流
```

**"改完不重建"如何成立**：插件源是 git 仓库（非 subPath/非镜像内置），每个任务 `upgrade`
拉最新快照并重拷 cache。改插件仓库 → `git push` → 下个任务自动拿到，无需重建 go-dev 或镜像。

## 4. 组件

### 4.1 插件源仓库（新增）

新建独立仓库，与主仓发布解耦；`marketplace upgrade` 只拉这个小仓。结构（§2 已实测）：

```
.claude-plugin/marketplace.json
{ "name": "ai-factory", "owner": "verdure-oss",
  "plugins": [ { "name": "issue-fix", "source": "./plugins/issue-fix",
                 "version": "0.1.0", "description": "ai-factory delegated issue→PR workflow" } ] }

plugins/issue-fix/.codex-plugin/plugin.json
{ "name": "issue-fix", "version": "0.1.0", "description": "ai-factory delegated issue→PR workflow" }

plugins/issue-fix/skills/<role>/SKILL.md   # frontmatter 必含 name + description
```

- **多 skill 按角色拆**：现有单个 `SKILL.md` 拆成若干角色 skill（如 `speccer` / `planner` /
  `builder` / `reviewer` / `cleanup`，具体划分在执行计划里定）。每个 skill 的 `description`
  写清"何时用"，Codex 靠它自动触发。
- `references/`、`scripts/local-ci.sh` 等资源随插件一起带上（解决现在没挂 references 的问题）。
- **什么做什么**：唯一事实源，定义"有哪些角色 skill + 版本"。
- **怎么用**：`codex plugin marketplace add <repo>`。
- **依赖**：go-dev pod 能访问该 git 源（见 §4.3 代理）。

### 4.2 `ai-factory-agent` 的 `run_codex`（改造核心）

新增 `maybe_register_codex_plugin()`，在 `codex exec` 之前、且与 `maybe_write_codex_config()`
协调顺序后调用：

```
1. maybe_write_codex_config()   # 先写：根键 model/model_provider 在最前 + [model_providers.*]
2. maybe_register_codex_plugin():
     codex plugin marketplace add   "$PLUGIN_SOURCE"  || true   # 幂等
     codex plugin marketplace upgrade                 || true   # 拉新；失败不致命（下有兜底）
     codex plugin add "$PLUGIN_NAME@$MARKETPLACE_NAME"          # 重拷 cache
3. compose prompt（见 §4.4）→ codex exec
```

新增环境变量（默认值让"官方/未配置"场景保持旧行为）：

| 变量 | 含义 | 默认 |
| --- | --- | --- |
| `AI_FACTORY_CODEX_PLUGIN_SOURCE` | marketplace 源：`owner/repo` / https git url / 本地路径 | 空=禁用插件，回退旧机制 |
| `AI_FACTORY_CODEX_PLUGIN_NAME` | 插件名 | `issue-fix` |
| `AI_FACTORY_CODEX_MARKETPLACE_NAME` | 市场名（同 marketplace.json 的 `name`） | `ai-factory` |
| `AI_FACTORY_CODEX_PLUGIN_REF` | git ref | `main` |
| `AI_FACTORY_CODEX_SKIP_PLUGIN` | `1` 时完全跳过注册 | 空 |

- **什么做什么**：任务开始时确保最新插件已装进本 pod 的 Codex。
- **怎么用**：设 `AI_FACTORY_CODEX_PLUGIN_SOURCE` 即启用。
- **依赖**：Codex CLI ≥ 0.121（marketplace 支持；实测 0.150.1 可用）；§4.3 网络。

### 4.3 config.toml 共存（§2 坑 #4）

`plugin add` 往 `$CODEX_HOME/config.toml` 追加 `[marketplaces.*]` / `[plugins."x@y"]` 表。约束：

- `maybe_write_codex_config()` 生成时，**根键 `model` / `model_provider` 必须写在文件最前**，
  所有 `[table]` 在其后。
- 顺序：**先** `maybe_write_codex_config()`（写我们的托管块），**后** `plugin add`（追加插件表）。
  两者操作 config.toml 的不同片段，不得互相覆盖。
- 托管块仍带 `# ai-factory: managed codex config, do not edit` marker；插件表由 Codex 维护，
  我们的写逻辑不得整体重写文件（只更新托管片段）——执行计划里需保证幂等追加而非覆盖。

### 4.4 prompt 组装变化

现有"请阅读 `/opt/.../SKILL.md`"退役。改为：不再点名单一文件路径，让 Codex 使用已注册插件里的
skill（元数据常驻，按 `description` 自主触发）。可在 prompt 里点名主入口 skill 或说明按身份选择；
具体措辞在执行计划里定。仍保留"最终打印唯一 `__AI_FACTORY_RESULT__=` 结果行"的约定。

### 4.5 网络与 git 代理

- go-dev pod 实测可直连 `github.com`（200）。任务态 `plan.go` 会注入
  `git config --global url."$AI_FACTORY_GIT_PROXY/https://github.com/".insteadOf "https://github.com/"`，
  `run_codex` 时该改写已生效，marketplace 的 git fetch 会经 `gh-proxy`。
- **决策**：`AI_FACTORY_CODEX_PLUGIN_SOURCE` 允许直接填 `owner/repo` 或完整 https url；执行计划需
  **实测**在任务态 insteadOf 生效下 `marketplace add/upgrade` 是否正常，若被 gh-proxy 干扰则改为
  直接传 gh-proxy 形式的 url，或为该 fetch 局部关闭 insteadOf。

### 4.6 退役旧机制（谨慎、可回退）

- 保留旧 `CODEX_SKILL_FILE` 注入作为**兜底**：当 `AI_FACTORY_CODEX_PLUGIN_SOURCE` 为空或插件注册
  失败且 cache 无可用副本时，回退到旧的"读单个 SKILL.md"（若仍挂载）。保证平滑迁移。
- 迁移完成后再清理：`charts/.../sandbox-warm-pool.yaml` 的 `issue-fix-skill` subPath 挂载、
  `codex.skillConfigMapName`、deploy/upgrade/`update-config.sh` 里创建 `issue-fix-skill` ConfigMap
  的段落——这些改到 §7 分阶段处理，本设计不一次性删干净。

## 5. 数据流

1. 维护者改插件仓库 → `git push`（version 视情况 bump）。
2. 新 issue 触发 FactoryTask → controller 准备 checkout（不变）。
3. go-dev pod 里 `run_codex`：写 config → `marketplace add/upgrade` + `plugin add`（拉到最新插件）。
4. `codex exec`：Codex 自动加载插件多 skill，按角色执行 fix→本地CI→commit→push→开 PR/MR。
5. 写 `.ai-factory/result-url.txt` + 打印结果行（不变）。

## 6. 错误处理

- **`marketplace upgrade` 失败但插件已在 cache**（同一 warm pod 之前装过）：记 warning，用现有
  cache 继续，不致命。
- **插件未装且 `add` 失败**（如网络不可达、源错误）：
  - 若旧 SKILL.md 兜底可用 → 回退旧机制并 warning；
  - 否则以清晰错误终止任务（不静默跑一个没有工作流指引的 codex）。
- **`AI_FACTORY_CODEX_SKIP_PLUGIN=1`**：跳过注册（排障/离线用）。
- 注册过程**绝不打印 token**；base_url / 源 url / 版本可打印一行 info 便于排障。

## 7. 分阶段落地（供执行计划细化）

1. 建插件源仓库骨架 + 把现有 SKILL.md 内容迁入（先单 skill 跑通，再拆多角色）。
2. `ai-factory-agent`：加 `maybe_register_codex_plugin()` + env + config.toml 顺序处理；
   加 `codex_plugin_test.sh`（TDD：假 codex on PATH，断言 add/upgrade/add 调用与 config 顺序、
   跳过开关、兜底路径）。
3. 任务态代理下 marketplace fetch 实测（§4.5）。
4. Helm/deploy/update-config：新增插件相关 env/values；旧 `issue-fix-skill` 挂载在兜底期保留。
5. 真实 issue 端到端验证：改插件 push → 新任务自动拿到（不重建 go-dev）。
6. 收尾：清理旧 subPath 挂载与 `skillConfigMapName`（确认兜底不再需要后）。

## 8. 测试

- **单元/脚本**：`bash -n`；`codex_plugin_test.sh`（假 codex 断言命令序列、config.toml 根键顺序、
  `SKIP_PLUGIN`、`add` 失败兜底）。
- **集成（pod 内）**：真插件 `marketplace add/upgrade`+`plugin add`，`codex exec` 验证多 skill 被
  加载使用（复刻 spike）。
- **代理**：任务态 insteadOf 下 fetch 正常。
- **端到端**：一个真实 issue 走完委托流程并开 PR；随后改插件 push，另一个 issue 不重建即用新版。
- **回归**：`go test ./...` 不受影响。

## 9. 不在本设计范围

- 具体角色 skill 如何切分内容（放执行计划/后续迭代）。
- 公开发布到外部 marketplace（当前是团队私仓，个人/仓库级注册即可）。
- scripted 模式（`openai-compatible`）——不受影响，不改。
- go-dev 之外的挂载优化。

