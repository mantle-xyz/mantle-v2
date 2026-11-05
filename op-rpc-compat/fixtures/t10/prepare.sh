#!/usr/bin/env bash
# Deploys the T10 fixture contracts and sends scenario transactions from ONE funded account.
#
# Required env:
#   ETH_PRIVATE_KEY   signing key of the funded account (read by cast; never printed here)
#   SEQ_RPC           RPC endpoint accepting transactions
# Optional env:
#   CAST              cast binary (default: cast)
#   OUT               output directory (default: ./out)
#
# Output: $OUT/tx.log  (one "<name> <txhash> <status>" line per transaction)
#         $OUT/addr.env (LOG_EMITTER=..., SCENARIOS=...)
set -u
CAST=${CAST:-cast}
OUT=${OUT:-./out}
HERE=$(cd "$(dirname "$0")" && pwd)
mkdir -p "$OUT"
: "${SEQ_RPC:?}"
: "${ETH_PRIVATE_KEY:?}"
export ETH_PRIVATE_KEY

BIN=$(solc --evm-version cancun --optimize --combined-json bin,bin-runtime "$HERE/Fixtures.sol" 2>/dev/null)
bin_of() { python3 -c "import json,sys;d=json.loads(sys.argv[1]);print('0x'+[v for k,v in d['contracts'].items() if k.endswith(':$1')][0]['bin'])" "$BIN"; }

: > "$OUT/tx.log"
send() { # name, then cast send args
  local name=$1; shift
  local j
  j=$("$CAST" send --rpc-url "$SEQ_RPC" --private-key "$ETH_PRIVATE_KEY" --json "$@" 2>"$OUT/last.err")
  local rc=$?
  if [ -z "$j" ]; then echo "$name FAILED_TO_SEND $(tr '\n' ' ' < "$OUT/last.err" | cut -c1-200)" >> "$OUT/tx.log"; return 1; fi
  python3 - "$name" "$j" >> "$OUT/tx.log" <<'P'
import json,sys
d=json.loads(sys.argv[2]);print(sys.argv[1],d['transactionHash'],d.get('status'),d.get('blockNumber'))
P
}
wait_blocks() { sleep "$1"; }

LOGEMIT_INIT=$(bin_of LogEmitter)
SCEN_INIT=$(bin_of Scenarios)
send deploy_LogEmitter --gas-limit 600000 --create "$LOGEMIT_INIT"
send deploy_Scenarios --gas-limit 2500000 --create "$SCEN_INIT"
python3 - "$OUT" <<'P'
import json,subprocess,sys
out=sys.argv[1]
lines=[l.split() for l in open(out+'/tx.log')]
import os
rpc=os.environ['SEQ_RPC']
addr={}
for l in lines:
    if l[0].startswith('deploy_') and l[1]!='FAILED_TO_SEND':
        r=json.loads(subprocess.check_output(['cast','receipt',l[1],'--rpc-url',rpc,'--json']))
        addr[l[0][7:]]=r['contractAddress']
open(out+'/addr.env','w').write('LOG_EMITTER=%s\nSCENARIOS=%s\n'%(addr.get('LogEmitter',''),addr.get('Scenarios','')))
P
# shellcheck disable=SC1091
. "$OUT/addr.env"
: "${LOG_EMITTER:?deploy failed}" "${SCENARIOS:?deploy failed}"

# ---- logs, spread over several blocks ----
send logs_ping_1 "$LOG_EMITTER" "ping(uint256,bytes)" 1 0x01 --gas-limit 100000
wait_blocks 6
send logs_burst_1 "$LOG_EMITTER" "burst(uint256,uint256)" 100 5 --gas-limit 400000
wait_blocks 6
send logs_ping_2 "$LOG_EMITTER" "ping(uint256,bytes)" 2 0x$(printf 'ab%.0s' $(seq 1 40)) --gas-limit 100000
wait_blocks 6
send logs_burst_2 "$LOG_EMITTER" "burst(uint256,uint256)" 200 3 --gas-limit 400000

# ---- trace scenarios ----
send fund_Scenarios "$SCENARIOS" --value 1000000000000000 --gas-limit 100000
send tx_transfer 0x00000000000000000000000000000000000000a1 --value 1 --gas-limit 21000
send tx_call_precompile_sha256 0x0000000000000000000000000000000000000002 0x616263 --gas-limit 60000
send tx_create_ok --gas-limit 200000 --create 0x600a600c600039600a6000f3600160005360016000f3
send tx_create_fail --gas-limit 200000 --create 0x60aa60005360016000fd
send sc_pay "$SCENARIOS" "pay(address)" 0x00000000000000000000000000000000000000a2 --value 7 --gas-limit 100000
send sc_revert "$SCENARIOS" "doRevert()" --gas-limit 100000
send sc_revert_custom "$SCENARIOS" "doRevertCustom()" --gas-limit 100000
send sc_oog "$SCENARIOS" "burnAll()" --gas-limit 150000
send sc_static_write "$SCENARIOS" "staticWrite()" --gas-limit 200000
send sc_selfdestruct_self "$SCENARIOS" "selfDestructSelf()" --gas-limit 100000
send sc_selfdestruct_other "$SCENARIOS" "selfDestructOther(address)" 0x00000000000000000000000000000000000000a3 --gas-limit 100000
send sc_create_and_destroy "$SCENARIOS" "createAndDestroy()" --value 5 --gas-limit 200000
send sc_delegate_value "$SCENARIOS" "delegateWithValue(address)" "$SCENARIOS" --value 1000 --gas-limit 200000
send sc_precompiles "$SCENARIOS" "precompiles()" --gas-limit 200000
send sc_sstore_clear "$SCENARIOS" "sstoreClear()" --gas-limit 200000
send sc_log_then_revert "$SCENARIOS" "logThenRevert()" --gas-limit 100000
send sc_create_ok "$SCENARIOS" "createOk()" --gas-limit 300000
send sc_create_fail "$SCENARIOS" "createFail()" --gas-limit 300000
send sc_create2_fail "$SCENARIOS" "create2Fail(bytes32)" 0x0000000000000000000000000000000000000000000000000000000000000001 --gas-limit 300000
send sc_deep_20 "$SCENARIOS" "deep(uint256)" 20 --gas-limit 2000000
send sc_deep_overflow "$SCENARIOS" "deep(uint256)" 1030 --gas-limit 20000000
# runCode: odd opcodes in a freshly deployed contract
send rc_undefined_opcode "$SCENARIOS" "runCode(bytes,bytes)" 0x0c 0x --gas-limit 300000
send rc_invalid_jump "$SCENARIOS" "runCode(bytes,bytes)" 0x600156 0x --gas-limit 300000
send rc_invalid_opcode "$SCENARIOS" "runCode(bytes,bytes)" 0xfe 0x --gas-limit 300000
send rc_stack_underflow "$SCENARIOS" "runCode(bytes,bytes)" 0x01 0x --gas-limit 300000
send rc_inner_oog "$SCENARIOS" "runCode(bytes,bytes)" 0x5b600056 0x --gas-limit 300000
# DELEGATECALL that carries value (the sc_delegate_value tx above reverts: writeSlot is non-payable)
send rc_delegate_value "$SCENARIOS" "runCode(bytes,bytes)" 0x600060006000600060045af400 0x --value 3 --gas-limit 300000
echo done >> "$OUT/tx.log"
