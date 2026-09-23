# RPC-Compat

Ethereum JSON-RPC 一致性对比测试工具，用于验证 geth 和 reth（或任意两个以太坊执行层客户端）在 JSON-RPC 请求与响应上的**完全一致性**。

## 设计依据

https://skfc4x16la.larksuite.com/wiki/ZhIuwPzqriOK02k2oxfuGMEGsAe

主要参考以太坊官方测试库 hive/rpc-compat. 它执行大量集成测试来检查多个 Ethereum 客户端（包括 Geth 和 Reth）在不同方面的协议实现正确性。但它不是实现细节的 RPC 一致性测试框架，而是综合合规性测试，需要自己构建运行测试驱动框架来对比两个客户端返回值。
- https://github.com/ethereum/execution-apis
- https://ethereum.github.io/execution-apis/tests/
- https://github.com/ethereum/hive
- https://github.com/ethereum/hive/tree/master/simulators/ethereum/rpc-compat

## 快速开始

```bash
# 编译
cd tests/rpc_compat
go build -o rpc_compat .

# 全量 RPC 查询测试（默认输出 report.json）
./rpc_compat

# 交易测试（包含标准方式和预确认方式，会修改链上状态）
# 默认输出报告到 report.tx.json
./rpc_compat --tx

# 只跑标准交易（适用于 reth sequencer devnet，不执行 preconf 场景）
./rpc_compat --tx --tx-standard-only
```

> **注意**：`--tx` 只运行交易测试，不运行 RPC 查询测试。两者互斥。
> **注意**：`--tx-standard-only` 必须与 `--tx` 一起使用；它会保留 Legacy、EIP-1559、EIP-7702、合约和 txpool 测试，只跳过 preconf 交易场景。
> **注意**：由于测试使用了标签，可能会导致结果不一致，请重复测试，或确保标签指向的区块是一致的

## 主要使用场景

### 场景 1: 全量 RPC 兼容性测试

验证所有 JSON-RPC 方法的返回结果一致性，**不修改链上状态**。默认输出到 `report.json`。

```bash
./rpc_compat
```

测试内容包括：
- 基础方法：`eth_chainId`, `eth_blockNumber`, `eth_gasPrice` 等
- 区块查询：`eth_getBlockByNumber`, `eth_getBlockReceipts` 等
- 账户状态：`eth_getBalance`, `eth_getCode`, `eth_getStorageAt`, `eth_getProof`
- 交易查询：`eth_getTransactionByHash`, `eth_getTransactionReceipt` 等
- 调用模拟：`eth_call`, `eth_estimateGas`；Mantle 扩展：`eth_estimateTotalFee`（仅 Arsia+，见 `eth_estimateTotalFee.json`）
- 日志查询：`eth_getLogs`
- 错误场景：无效参数、方法不存在、OOG 等

### 场景 2: 交易测试

验证交易发送、状态变更的一致性，**会修改链上状态**。默认输出到 `report.tx.json`。

```bash
./rpc_compat --tx

# reth sequencer 路线：仅验证标准交易，不运行要求 op-geth sequencer 的 preconf 场景
./rpc_compat --tx --tx-standard-only
```

测试内容包括：
- **Legacy 交易**：标准方式 + 预确认方式
- **EIP-1559 交易**：标准方式 + 预确认方式
- **EIP-7702 交易**：标准方式 + 预确认方式
- **合约部署和调用**：SimpleStorage 合约测试
- **状态验证**：余额变化、Gas 使用、交易收据

输出示例：
```
📤 测试 Legacy 交易 (eth_sendRawTransaction)...
  ✓ PASS 原生代币转账_Legacy
    余额变化: 1000000000000000 (一致)
    Gas 使用: 21000 (一致)

📤 测试 EIP-1559 交易 (eth_sendRawTransactionWithPreconf)...
  ✓ PASS 原生代币转账_EIP-1559_preconf
    Preconf 状态: Geth=success, Reth=success

📜 测试 EIP-7702 交易...
  ✓ PASS EIP-7702 基本转账
    余额变化: 1000000000000000 (一致)
    Gas 使用: 46000 (一致)  # 包含授权处理开销
  ✓ PASS EIP-7702 预确认转账
    Preconf 状态: Geth=success, Reth=success

📦 测试 SimpleStorage 合约部署...
  ✓ PASS SimpleStorage部署
    Geth 合约地址: 0x40918ba7f132e0acba2ce4de4c4baf9bd2d7d849
    Reth 合约地址: 0xf32d39ff9f6aa7a7a64d7a4f00a54826ef791a55
    Gas 使用: 123009 (一致)

📞 测试 SimpleStorage.get() 方法（初始值）...
  ✓ PASS SimpleStorage.get()_初始值
    返回值: 0x0 (Geth 和 Reth 一致)

📝 测试 SimpleStorage.set(42) 方法...
  ✓ PASS SimpleStorage.set(42)
    Gas 使用: 43696 (一致)

📞 测试 SimpleStorage.get() 方法（设置后）...
  ✓ PASS SimpleStorage.get()_设置后
    返回值: 42 (0x2a) - Geth 和 Reth 一致
```

### 场景 3: 指定测试文件

```bash
# 只测试基础方法
./rpc_compat -f testcases/eth_basic.json

# 只测试状态查询（eth_getProof, eth_getStorageAt）
./rpc_compat -f testcases/eth_state.json

# 只测试 eth_call
./rpc_compat -f testcases/eth_call_full.json
```

### 场景 4: 排除特定测试

```bash
# 排除 debug 和 trace 方法（这些通常差异较大）
./rpc_compat --exclude eth_debug.json --exclude eth_trace.json
```

### 场景 5: 持续发交易 + 监听 GPO 交易顺序

用于链上实时观测：
- 向指定 RPC 持续发交易（尽量贴近每个区块末尾）
- 持续监听每个区块，检查是否出现 GPO 交易，以及 GPO 交易后是否还有交易

```bash
# 只发送交易（默认 RPC: QA8）
./rpc_compat stream --send \
  --private-key 0x你的私钥

# 只监听（默认监听同一个 --rpc）
./rpc_compat stream --watch

# 同时发送 + 监听（监听独立 RPC）
./rpc_compat stream --send --watch \
  --rpc http://127.0.0.1:9545 \
  --watch-rpc http://127.0.0.1:19545 \
  --private-key 0x你的私钥
```

常用参数：
- `--rpc`：发送交易 RPC，默认 `https://op-geth-rpc0-sepolia-qa8.qa4.gomantle.org`
- `--watch-rpc`：监听 RPC（默认跟 `--rpc` 一样）
- `--to`：接收地址（默认发送给自己，仅消耗手续费）
- `--lead-ms`：预计下个区块前多少毫秒发送（默认 `1200`）
- `--start-block`：监听起始块（默认 `latest`，即从“当前最新块+1”开始）
- `--gpo-address`：GPO 地址（默认 `0x420000000000000000000000000000000000000F`）

### 场景 6: Mantle 预确认专项测试（`preconf` 子命令）

裸 JSON-RPC 测 `eth_sendRawTransactionWithPreconf`（不依赖 op-geth fork 的 typed client）。

```bash
# 前置：devnet 走 op-geth sequencer 路线（task up-all），app.yaml 里已配 preconf 白名单
./rpc_compat preconf              # 核心场景（默认）
./rpc_compat preconf --heavy      # 追加吞吐压测（stress）
./rpc_compat preconf --heavy --stress-count 300
```

覆盖的场景：

- `valid_native_success` — 白名单原生转账 preconf 返回 `success`
- `reason_*` — 失败原因：`allowance insufficient` / `out of gas` / `intrinsic gas too low` / `insufficient funds` / `underflow balance sender` / `nonce too low`
- `predicted_block_matches_actual` — preconf 预测的 `blockHeight` == 实际落块
- `geth_reth_parity_null_logs` — **同一笔 revert 交易，op-geth sequencer 与 op-reth 转发节点返回的 `receipt.logs` 逐字节一致（都为 `null`）**，守护 reth 的 `logs:null` 反序列化修复
- `stress_throughput`（`--heavy`）— N 笔 preconf 全部成功 + 收款方余额守恒
- `concurrent_burst`（`--heavy`）— 多轮并发突发 preconf 全部成功（高 TPS）
- `preconf_ordered_before_regular`（`--heavy`）— 同块内 preconf 交易(Addr1)排在普通池交易(Addr3)之前
- `deposit_ordered_before_user`（`--heavy`）— L1 deposit 派生到 L2 后，块内 deposit(0x7e) 排在普通交易之前（本地 devnet 上 L1 `depositTransaction` 走 Mantle ResourceMetering 会 revert，故标记 **INCONCLUSIVE 跳过**，非失败）
- `block_full_spills_across_blocks`（`--heavy`）— 用 GasBurner 合约（烧光所发 gas）从单账户连发多笔 preconf，每笔烧 ~45% 区块 gas。约 2 笔就填满一块，超出容量的 preconf 不会被拒、而是**自动溢出到后续块**（`already known` 表示已入池排队）；等收据后校验这些 burner **跨 ≥2 个块**落地，且**每个块 gasUsed 都不超过 gasLimit**。这种（无论受 gas、DA footprint 还是 byte-size 约束的）切分即预期的 block-full 行为。需把 GasBurner 地址 `0xf0620ca0820DE5BcAc573f2DaD9243A1427d41f7` 加进 op-geth `txpool.topreconfs` 并重启

与其它模式不同，`preconf` 有**副作用**：自动做幂等 setup —— 缺合约且 funder nonce 0 时用内嵌字节码部署 TestERC20/TestPay；funder 余额低时从 dev 账户补；Addr3 approve/mint 以支持 OOG/underflow 用例。

> 已全部移植（stress / transfer→nonce / basefee_stress→concurrent / sort→ordering / blockfull→spills）。block-full 用 GasBurner 稳定跨块通过；deposit-before-user 因 devnet 上 L1 `depositTransaction` 会 revert 而标记 INCONCLUSIVE（见上）。

flags：`--sequencer`(默认 9545) / `--reth`(29545) / `--l1`(38545) / `--funder-key` / `--addr1-key` / `--addr3-key` / `--heavy` / `--stress-count`。

失败即 exit 1，可进 CI。

> 注意：`preconf` 必须走 op-geth sequencer 路线（`task up-all`）——只有 op-geth 会对 revert 交易吐 `logs:null`；reth sequencer 吐 `[]`，parity 场景在那条路线下不成立。

## 默认配置

| 配置 | 默认值 | 说明 |
|------|--------|------|
| Geth RPC | `http://127.0.0.1:19545` | 可通过 `--geth` 或 `GETH_RPC_URL` 修改 |
| Reth RPC | `http://127.0.0.1:29545` | 可通过 `--reth` 或 `RETH_RPC_URL` 修改 |
| 输出文件 | `report.json` | 可通过 `-o` 修改，`-o ""` 禁用 |
| 发送者私钥 | Hardhat #0 账户 | `0xac0974...` |
| 接收地址（标准） | Hardhat #1 账户 | `0x70997970...` |
| 接收地址（预确认） | 白名单地址 | `0x71920E3c...` |

## 测试结果状态

| 状态 | 符号 | 说明 | 影响退出码 |
|------|------|------|-----------|
| PASS | ✓ | 完全一致 | 否 |
| COMPATIBLE | ≈ | 兼容（如双方都返回方法不存在错误） | 否 |
| WARNING | ⚠ | 有差异但不严重（如 reth 返回额外字段） | 否 |
| SKIP | ⊘ | 已知差异，跳过检查 | 否 |
| FAIL | ✗ | 真正的失败 | **是** |

## 测试用例文件

| 文件 | 说明 | 测试数量 |
|------|------|---------|
| `eth_basic.json` | 基础 RPC 方法 | ~20 |
| `eth_blocks.json` | 区块查询方法 | ~15 |
| `eth_accounts.json` | 账户相关方法 | ~10 |
| `eth_state.json` | 状态证明（eth_getProof, eth_getStorageAt） | ~15 |
| `eth_call.json` | eth_call 基础测试 | ~10 |
| `eth_call_full.json` | eth_call 完整参数组合 | ~40 |
| `eth_estimateGas_full.json` | eth_estimateGas 完整参数组合 | ~30 |
| `eth_estimateTotalFee.json` | eth_estimateTotalFee（Mantle/Arsia+，正常/边界/异常） | ~22 |
| `eth_getLogs_full.json` | eth_getLogs 完整参数组合 | ~20 |
| `eth_fee.json` | Fee 相关方法 | ~10 |
| `eth_txpool.json` | 交易池方法 | ~5 |
| `eth_debug.json` | Debug 方法 | ~10 |
| `eth_trace.json` | Trace 方法 | ~10 |
| `eth_rollup.json` | Rollup/Optimism 方法 | ~5 |
| `errors_full.json` | 错误场景测试 | ~50 |
| `known_diffs.json` | **已知差异配置（非测试文件）** | - |

## 已知差异配置

`testcases/known_diffs.json` 用于配置已知的、预期的差异，这些测试会被标记为 `SKIP`：

```json
{
  "description": "已知差异配置文件",
  "known_diffs": [
    {
      "test_name": "eth_hashrate",
      "reason": "eth_hashrate 在 PoS 链上已弃用。geth 返回方法不存在错误，reth 返回 0x0",
      "geth_example": {"error": {"code": -32601, "message": "method does not exist"}},
      "reth_example": {"result": "0x0"}
    }
  ]
}
```

## 报告输出

默认输出 `report.json`，包含完整的测试结果：

```json
{
  "geth_url": "http://127.0.0.1:19545",
  "reth_url": "http://127.0.0.1:29545",
  "timestamp": "2024-01-01T00:00:00Z",
  "total_tests": 302,
  "pass_count": 200,
  "compatible_count": 10,
  "warning_count": 15,
  "skip_count": 50,
  "fail_count": 27,
  "results": [...]
}
```

## CI 集成

```bash
#!/bin/bash
set -e

cd tests/rpc_compat

# 全量 RPC 测试
./rpc_compat

# 检查结果（FAIL 时退出码为 1）
if [ $? -ne 0 ]; then
    echo "RPC 一致性测试失败！查看 report.json"
    exit 1
fi

echo "RPC 一致性测试通过"
```

## 完整参数

```
Usage:
  rpc_compat [flags]

Flags:
      --geth string                  geth RPC 端点 URL (default "http://127.0.0.1:19545")
      --reth string                  reth RPC 端点 URL (default "http://127.0.0.1:29545")
  -f, --file string                  指定测试文件路径 (不指定则运行所有)
      --testcases-dir string         测试用例目录 (default "testcases")
      --exclude strings              排除的文件名 (可多次指定)
  -o, --output string                输出报告文件路径 (default "report.json")
  -v, --verbose                      详细输出
      --timeout duration             请求超时时间 (default 30s)
      --retries int                  请求失败重试次数 (default 3)
      --retry-delay duration         重试间隔 (default 1s)
      
      --tx-test                      运行交易测试（Legacy/EIP-1559/EIP-7702，会修改链上状态）
      --tx-private-key string        交易测试私钥
      --tx-recipient string          交易接收地址（标准方式）
      --tx-recipient-preconf string  预确认交易接收地址（白名单地址）
      --tx-amount string             转账金额（wei）(default "1000000000000000")
      
  -h, --help                         help for rpc_compat
```

## 预确认交易配置

预确认交易需要在 op-geth 中配置白名单地址：

```yaml
# local/services/op-geth/app.yaml
--txpool.frompreconfs=0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266  # 允许的发送地址
--txpool.topreconfs=0x71920E3cb420fbD8Ba9a495E6f801c50375ea127   # 允许的接收地址
--miner.enablepreconfchecker                                      # 启用预确认检查器
```

## 项目结构

```
tests/rpc_compat/
├── main.go                    # 入口
├── cmd/
│   ├── root.go               # CLI 配置和全局标志
│   └── run.go                # 测试执行逻辑
├── pkg/
│   ├── rpc/client.go         # RPC 客户端、重试、并行对比
│   ├── diff/compare.go       # JSON 深度对比算法
│   ├── report/report.go      # 结果收集、格式化输出
│   └── tx/                   # 交易测试
│       ├── types.go          # 类型定义
│       ├── builder.go        # 交易构建器（Legacy, EIP-1559, EIP-7702）
│       └── tester.go         # 交易测试执行
└── testcases/                # 测试用例
    ├── known_diffs.json      # 已知差异配置
    ├── eth_basic.json
    ├── eth_state.json        # eth_getProof, eth_getStorageAt
    └── ...
```

## License

MIT
