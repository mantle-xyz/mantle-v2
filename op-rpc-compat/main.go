/*
RPC-Diff: Ethereum JSON-RPC 一致性对比测试工具

用于验证 geth 和 reth (或任意两个以太坊执行层客户端) 在 JSON-RPC
请求与响应上的完全一致性。

用法:
  rpc-diff single -m eth_blockNumber                    # 单个方法测试
  rpc-diff batch -f tests.yaml                          # 批量测试
  rpc-diff compare -m eth_getBlockByNumber -p '["0x1", true]'  # 带参数测试

环境变量:
  GETH_RPC_URL  - geth RPC 端点 (默认: http://localhost:8545)
  RETH_RPC_URL  - reth RPC 端点 (默认: http://localhost:8546)
*/
package main

import (
	"os"

	"github.com/ethereum-optimism/optimism/op-rpc-compat/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}

