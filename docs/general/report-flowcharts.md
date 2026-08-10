# ai-factory 自托管服务汇报流程图

> 本文档包含 ai-factory 自托管服务的 10 幅 Mermaid 流程图，用于进度汇报。
> Mermaid 可直接在 GitHub、Typora、VS Code 等支持渲染的 Markdown 阅读器中显示。
> 每幅图控制在单屏内可完整截取，节点文案已精简，详细说明见 `docs/` 下的设计文档。

## 目录

1. [整体端到端流程](#1-整体端到端流程)
2. [Webhook 处理流程](#2-webhook-处理流程)
3. [控制器调度与沙箱准备](#3-控制器调度与沙箱准备)
4. [控制器执行与收尾](#4-控制器执行与收尾)
5. [标签状态机](#5-标签状态机)
6. [三个镜像的协作](#6-三个镜像的协作)
7. [三个镜像的泳道协作](#7-三个镜像的泳道协作)
8. [agent-sandbox 资源拓扑](#8-agent-sandbox-资源拓扑)
9. [暖池生命周期](#9-暖池生命周期)
10. [Claim 全生命周期](#10-claim-全生命周期)

---

## 1. 整体端到端流程

**一句话说明**：$从"用户给 Issue 打标签"到"自动生成 PR"的完整链路，是汇报的主图。$

```mermaid
%%{init: {"theme":"base","themeVariables":{"fontSize":"22px"}}}%%
flowchart TD
    U["用户在 Issue 打<br/>ai-factory-run 标签"] --> V["GitHub 推送 Webhook<br/>到 /webhook/github"]
    V --> H["验证签名 +<br/>解析 Issue 事件"]
    H --> T["创建 FactoryTask<br/>打 running 标签"]
    T --> W["控制器接管任务"]
    W --> S["创建 SandboxClaim<br/>从 warm pool 取 Pod"]
    S --> EX["Claim Ready<br/>kubectl exec 下发命令"]
    EX --> E["沙箱内执行：clone<br/>agent 改码 → 验证 → push"]
    E --> R{"执行结果?"}
    R -->|成功| P["创建 Pull Request"]
    P --> D["打 done 标签<br/>发 PR 评论"]
    R -->|失败| F["打 failed 标签<br/>发失败评论"]
    D --> C["销毁 Sandbox Pod"]
    F --> C
```

---

## 2. Webhook 处理流程

**一句话说明**：只讲两个核心机制——**终态删除重建（失败重试）**：任务名确定，CR 到终态（Failed/Succeeded）时删旧建新即可重跑；**AlreadyExists 并发去重**：同名任务只建一个，并发 apply 的输家视为成功。前置的签名、标签校验流程已省略。

```mermaid
%%{init: {"theme":"base","themeVariables":{"fontSize":"26px"}}}%%
flowchart LR
    A["收到 Webhook<br/>（签名 / run-smoke 标签<br/>已校验通过）"] --> B{"查询已有任务<br/>CR phase?"}
    B -->|不存在| C["kubectl apply<br/>创建新 FactoryTask"]
    B -->|中间态| C0["忽略<br/>（任务运行中）"]
    B -->|终态 Failed/Succeeded| D["kubectl delete<br/>删旧任务（失败重试）"]
    D --> C
    C --> K{"apply 报<br/>AlreadyExists?"}
    K -->|是| E0["并发去重"]
    K -->|否| F["打 running 标签<br/>返回 200 triggered"]
    C0 --> Z["结束"]
    E0 --> Z
    F --> Z
```

---

## 3. 控制器调度与沙箱准备

**一句话说明**：控制器把任务从"等待"推进到"沙箱就绪"，含 waiting↔running 标签切换。

```mermaid
%%{init: {"theme":"base","themeVariables":{"fontSize":"22px"}}}%%
flowchart TD
    A["每 15s 轮询所有 FactoryTask"] --> B{"phase 可协调?"}
    B -->|否| A
    B -->|是| C["获取处理锁"]
    C --> D["Pending：发开始评论"]
    D --> E["创建 SandboxClaim"]
    E --> F["ClaimCreated：打 waiting 标签"]
    F --> G["轮询等 Claim Ready"]
    G --> H{"限时就绪?"}
    H -->|超时| X["Failed：failed 标签 + 评论"]
    H -->|就绪| I["SandboxReady：打 running 标签"]
    I --> J["Running：开始执行步骤"]
```

---

## 4. 控制器执行与收尾

**一句话说明**：在沙箱内执行 agent 步骤、失败重试、创建 PR 并收尾。因节点较多，从原"控制器执行流程"拆为两幅，保证单屏可完整显示。

```mermaid
%%{init: {"theme":"base","themeVariables":{"fontSize":"22px"}}}%%
flowchart TD
    A["逐条执行 agent 步骤"] --> B{"步骤失败?"}
    B -->|否| C["下一条步骤"]
    C --> D{"全部完成?"}
    D -->|否| A
    D -->|是| E["创建 Pull Request"]
    E --> F{"创建成功?"}
    F -->|否| G["Failed：failed 标签 + 评论"]
    F -->|是| H["Succeeded：done 标签 + PR 评论"]
    B -->|是| I{"可重试错误?"}
    I -->|否| G
    I -->|是| J["重试该步骤一次"]
    J --> K{"重试成功?"}
    K -->|否| G
    K -->|是| C
    G --> Z["结束"]
    H --> Z
```

---

## 5. 标签状态机

**一句话说明**：用户通过标签实时看到任务卡在哪——`running`(已受理) → `waiting`(等沙箱) → `running`(执行中) → `done`/`failed`。注意 **`running` 出现两次但含义不同**；到 `done`/`failed` 时系统会**移除触发标签 `run`/`smoke`**，用户重打即可删除重建、重新执行。

```mermaid
%%{init: {"theme":"base","themeVariables":{"fontSize":"26px"}}}%%
stateDiagram-v2
    direction LR
    [*] --> Start
    Start: 初始<br/>无状态标签<br/>等待用户触发
    Running1: ai-factory-running<br/>已受理<br/>（首次 SetRunning）
    Waiting: ai-factory-waiting<br/>排队等沙箱<br/>（轮询 Claim Ready）
    Running2: ai-factory-running<br/>执行中<br/>（Claim Ready SetRunning）
    Done: ai-factory-done<br/>成功<br/>（移除 run/smoke）
    Failed: ai-factory-failed<br/>失败<br/>（移除 run/smoke）

    Start --> Running1: 创建 FactoryTask<br/>首次 SetRunning
    Running1 --> Waiting: 创建 SandboxClaim<br/>SetWaiting
    Waiting --> Running2: Claim Ready<br/>SetRunning
    Running2 --> Done: 成功创建 PR<br/>SetDone
    Running2 --> Failed: 步骤失败<br/>SetFailed
    Waiting --> Failed: 等沙箱超时<br/>SetFailed
    Done --> Start: 用户重打 run<br/>删除重建重跑
    Failed --> Start: 用户重打 run<br/>删除重建重跑
```

---

## 6. 三个镜像的协作

**一句话说明**：自托管服务打包后产出 **3 个镜像**，编号 + 颜色标识每个环节由哪个镜像负责，镜像明细见下方表格。

```mermaid
%%{init: {"theme":"base","themeVariables":{"fontSize":"22px"}}}%%
flowchart TD
    classDef server fill:#e3f2fd,stroke:#1565c0,color:#0d47a1
    classDef ctrl fill:#fff3e0,stroke:#ef6c00,color:#e65100
    classDef sandbox fill:#e8f5e9,stroke:#2e7d32,color:#1b5e20

    A["① 接收 GitHub/GitLab Webhook"]:::server --> B["① 验证签名 → 创建 FactoryTask"]:::server
    B --> C["② 监听 SandboxClaim → 从暖池分配沙箱 Pod（③ 镜像）"]:::ctrl
    C --> D["② Claim 置为 Ready"]:::ctrl
    D --> E["① 注入环境变量，下发 agent 命令"]:::server
    E --> F["③ 沙箱内克隆目标仓库"]:::sandbox
    F --> G["③ ai-factory-agent 改码（LLM）"]:::sandbox
    G --> H["③ 执行验证命令"]:::sandbox
    H --> I["③ push 分支"]:::sandbox
    I --> J["① 建 PR、打标签、回收沙箱"]:::server
```

**三个镜像**（对应 `scripts/package.sh` 产出的 3 个 tar 包）：

| 编号  | 镜像                         | 来源                               | 职责                                |
| --- | -------------------------- | -------------------------------- | --------------------------------- |
| ①   | `ai-factory-server`        | 自研                               | Webhook 服务 + 任务控制器（Go，含 kubectl）  |
| ②   | `agent-sandbox-controller` | 上游 kubernetes-sigs/agent-sandbox | SandboxClaim / 暖池控制器              |
| ③   | `coding-agent-sandbox`     | 自研                               | 沙箱开发环境 + ai-factory-agent（Python） |

---

## 7. 三个镜像的泳道协作

**一句话说明**：上一幅按"环节先后"讲，这幅按"职责划分"讲——每个镜像一个泳道（subgraph），跨块箭头即协作点。① server 放中间作为编排中心，②③ 分列左右，跨块箭头最少。交接机制：server 不直连 Pod，靠 kubectl 读 SandboxClaim 状态拿 Pod 名，再 `kubectl exec` 进沙箱执行命令；暖池已把 Pod 预热为 Running（/workspace 就绪），任务无需等待冷启动。

```mermaid
%%{init: {"theme":"base","themeVariables":{"fontSize":"26px"}}}%%
flowchart LR
    classDef server fill:#e3f2fd,stroke:#1565c0,color:#0d47a1
    classDef ctrl fill:#fff3e0,stroke:#ef6c00,color:#e65100
    classDef sandbox fill:#e8f5e9,stroke:#2e7d32,color:#1b5e20

    subgraph CTRL["② agent-sandbox-controller"]
        direction TB
        B1["监听<br/>SandboxClaim<br/>事件"]:::ctrl
        B2["从暖池挑<br/>空闲沙箱<br/>Pod 绑定"]:::ctrl
        B3["Claim 状态<br/>置为 Ready<br/>写入 Pod 名"]:::ctrl
    end

    subgraph SRV["① ai-factory-server"]
        direction TB
        A1["接收<br/>GitHub/GitLab<br/>Webhook"]:::server
        A2["验证签名<br/>创建<br/>FactoryTask"]:::server
        A3["创建<br/>SandboxClaim<br/>申请沙箱"]:::server
        A4["读 Claim 状态<br/>拿 Pod 名<br/>kubectl exec 下发"]:::server
        A5["建 PR<br/>打 done/failed 标签<br/>回收沙箱"]:::server
    end

    subgraph SBX["③ coding-agent-sandbox"]
        direction TB
        C1["沙箱 Pod 运行中<br/>暖池已预热<br/>/workspace 就绪"]:::sandbox
        C2["克隆仓库<br/>运行 agent<br/>LLM 改码"]:::sandbox
        C3["验证命令<br/>push 分支<br/>任务完成"]:::sandbox
    end

    A1 --> A2 --> A3
    A3 -->|创建 Claim| B1
    B1 --> B2 --> B3
    B3 -->|Ready| A4
    A4 -->|kubectl exec| C2
    B2 -->|绑定已运行 Pod| C1
    C1 --> C2 --> C3
    C3 -->|完成| A5
```

---

## 8. agent-sandbox 资源拓扑

**一句话说明**：静态结构——4 个 CRD 的关系。`SandboxTemplate` 定义 Pod 模板（不建 Pod）；`SandboxWarmPool` 引用模板做预热；`SandboxClaim` 引用模板 + 暖池 + 注入 env 申请沙箱；`Sandbox` 是 Claim 名下的沙箱实例抽象（owner 指向 Claim，是级联删除的钩子）；实际 Pod 由 Sandbox 控制器创建。agent-sandbox 控制器 watch 全部 4 种 CRD。

```mermaid
%%{init: {"theme":"base","themeVariables":{"fontSize":"26px"}}}%%
flowchart TD
    classDef template fill:#f3e5f5,stroke:#6a1b9a,color:#4a148c
    classDef pool fill:#fff3e0,stroke:#ef6c00,color:#e65100
    classDef claim fill:#e3f2fd,stroke:#1565c0,color:#0d47a1
    classDef sandbox fill:#e8f5e9,stroke:#2e7d32,color:#1b5e20

    T["SandboxTemplate<br/>go-dev<br/>纯模板（不建 Pod）"]:::template
    W["SandboxWarmPool<br/>go-dev<br/>replicas: 2 预热"]:::pool
    C["SandboxClaim<br/>github-...-claim<br/>模板 + 暖池 + env"]:::claim
    S["Sandbox<br/>github-...-claim<br/>owner→Claim<br/>模板 spec+合并 env"]:::sandbox
    P["Pod<br/>github-...-claim<br/>注入 env 运行"]:::sandbox

    T -->|"引用 templateRef"| W
    T -->|"引用 templateRef"| C
    W -->|"引用 warmPoolRef"| C
    C -->|"ownerReferences<br/>级联删除钩子"| S
    S -->|"创建并管理"| P
```

---

## 9. 暖池生命周期

**一句话说明**：`SandboxWarmPool` 声明 `replicas: 2`，控制器创建对应数量的空闲 Sandbox/Pod 待命；被 Claim 认领消耗名额后，控制器自动补充新 Pod，池中始终维持设定数量——**暖池的 Pod 数量固定，不会无限增长**。

```mermaid
%%{init: {"theme":"base","themeVariables":{"fontSize":"26px"}}}%%
flowchart TD
    classDef pool fill:#fff3e0,stroke:#ef6c00,color:#e65100
    classDef pod fill:#e8f5e9,stroke:#2e7d32,color:#1b5e20

    A["SandboxWarmPool<br/>go-dev replicas: 2<br/>声明期望数量"]:::pool
    B["控制器创建 N 个<br/>空闲 Sandbox<br/>（warm-pool 标签）"]:::pool
    C["对应 N 个 Pod<br/>go-dev-xxx 待命<br/>镜像已预热"]:::pod
    D["被 Claim 认领 1 个<br/>名额空出"]:::pod
    E["控制器检测空缺<br/>补充新 Sandbox + Pod<br/>回到待命"]:::pool

    A --> B --> C
    C --> D
    D --> E
    E --> C
```

---

## 10. Claim 全生命周期（泳道）

**一句话说明**：一个任务从申请沙箱到回收的完整过程，三泳道表示 server / 控制器 / Sandbox Pod 各自职责。交接点：server 只提交 Claim、读回 Pod 名、`kubectl exec`、删 Claim；中间的分配、建 Sandbox、建 Pod、回写状态、补充暖池全由控制器声明式完成；删除 Claim 靠 `ownerReferences` 级联回收 Sandbox + Pod。

```mermaid
%%{init: {"theme":"base","themeVariables":{"fontSize":"26px"}}}%%
flowchart LR
    classDef server fill:#e3f2fd,stroke:#1565c0,color:#0d47a1
    classDef ctrl fill:#fff3e0,stroke:#ef6c00,color:#e65100
    classDef pod fill:#e8f5e9,stroke:#2e7d32,color:#1b5e20

    subgraph SRV["① ai-factory-server"]
        direction TB
        A1["创建 SandboxClaim<br/>warmPoolRef: go-dev<br/>注入 env"]:::server
        A2["读 claim.status<br/>拿 Pod 名<br/>kubectl exec 执行"]:::server
        A3["任务完成<br/>删除 SandboxClaim"]:::server
    end

    subgraph CTRL["② agent-sandbox 控制器"]
        direction TB
        B1["找空闲暖池<br/>Sandbox 绑定<br/>占用名额"]:::ctrl
        B2["创建 Claim 专属<br/>Sandbox（复制模板<br/>+ 合并 env，owner→claim）"]:::ctrl
        B3["回写 claim.status<br/>sandbox.name<br/>Ready=True"]:::ctrl
        B4["名额空缺<br/>补充新 go-dev"]:::ctrl
    end

    subgraph SBX["③ Sandbox Pod"]
        direction TB
        C1["Sandbox 控制器<br/>创建 Pod<br/>（env 已注入）"]:::pod
        C2["Pod Ready<br/>运行任务"]:::pod
        C3["Claim 删除后<br/>级联回收<br/>Sandbox + Pod"]:::pod
    end

    A1 -->|"kubectl apply"| B1
    B1 --> B2
    B2 -->|"创建"| C1
    C1 --> C2
    C2 -->|"Ready"| B3
    B3 -->|"返回 Pod 名"| A2
    A2 -->|"kubectl exec"| C2
    A3 -->|"kubectl delete"| C3
    B1 -.->|"消耗名额"| B4
```

---

## 标签对照表（附）

| 标签                   | 颜色   | 含义        | 谁添加 | 何时移除    |
| -------------------- | ---- | --------- | --- | ------- |
| `ai-factory-run`     | 用户定义 | 触发正式执行    | 用户  | 任务完成/失败 |
| `ai-factory-smoke`   | 用户定义 | 触发冒烟测试    | 用户  | 任务完成/失败 |
| `ai-factory-running` | 蓝    | 执行中       | 系统  | 状态切换    |
| `ai-factory-waiting` | 黄    | 排队等沙箱 Pod | 系统  | 状态切换    |
| `ai-factory-done`    | 绿    | 成功        | 系统  | 重新触发时   |
| `ai-factory-failed`  | 红    | 失败        | 系统  | 重新触发时   |
