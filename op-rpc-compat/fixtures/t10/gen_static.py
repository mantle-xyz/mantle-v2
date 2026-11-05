#!/usr/bin/env python3
"""Generates the chain-independent T10 corpus files into op-rpc-compat/testcases/.

Everything here is self-contained: contracts are injected with stateOverrides, so
the cases run on any devnet. Chain-specific cases (deployed fixtures, cutoff/Arsia
block numbers, token-ratio update blocks) come from gen_chain.py.

Needs: solc, cast (CAST env, default "cast").
usage: gen_static.py <testcases-dir>
"""
import json
import os
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
CAST = os.environ.get("CAST", "cast")
OUT = sys.argv[1] if __name__ == "__main__" else None

F = "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266"  # hardhat 0, only used as a call sender
F2 = "0x70997970C51812dc3A010C7d01b50e0d17dc79C8"
L1BLOCK = "0x4200000000000000000000000000000000000015"
SC = "0x00000000000000000000000000000000000c0001"  # Scenarios (override)
GB = "0x00000000000000000000000000000000000b0001"  # GasBurner (override)
AP = "0x00000000000000000000000000000000000a0001"  # AccessProbe (override)
SD = "0x00000000000000000000000000000000000d0001"  # raw selfdestruct code (override)
A1 = "0x00000000000000000000000000000000000000a1"
A3 = "0x00000000000000000000000000000000000000a3"
ETH = "0xde0b6b3a7640000"
SALT = "0x" + "00" * 31 + "01"


def runtimes():
    out = json.loads(
        subprocess.check_output(
            ["solc", "--evm-version", "cancun", "--optimize", "--combined-json", "bin-runtime", os.path.join(HERE, "Fixtures.sol")],
            stderr=subprocess.DEVNULL,
        )
    )
    return {k.split(":")[-1]: "0x" + v["bin-runtime"] for k, v in out["contracts"].items()}


RT = runtimes()


def cd(sig, *args):
    return subprocess.check_output([CAST, "calldata", sig, *map(str, args)]).decode().strip()


def case(name, method, params, desc):
    return {"name": name, "method": method, "params": params, "description": desc}


def write(fname, cases):
    names = [c["name"] for c in cases]
    assert len(names) == len(set(names)), "duplicate case names in " + fname
    with open(os.path.join(OUT, fname), "w") as f:
        # one case per line keeps the large trace corpora small and diffable
        f.write("[\n" + ",\n".join("  " + json.dumps(c, separators=(",", ":")) for c in cases) + "\n]\n")
    print(fname, len(cases))


def hx(n):
    return hex(n)


# ---------------------------------------------------------------- alloy#4434
def storage_key_cases():
    keys = {
        "k32_control": "0x" + "00" * 31 + "01",
        "k31": "0x" + "00" * 30 + "01",
        "k33_zero": "0x" + "00" * 33,
        "k33_lead_one": "0x01" + "00" * 32,
        "k33_lead_zero_then_slot1": "0x00" + "00" * 31 + "01",
        "k33_trail_one": "0x" + "00" * 32 + "01",
        "k64": "0x" + "ab" * 64,
        "k128": "0x" + "cd" * 128,
        "short_0x1": "0x1",
        "odd_len_0x001": "0x001",
        "empty_0x": "0x",
        "non_hex": "0xzz",
        "no_prefix_33": "00" * 33,
    }
    cs = []
    for tag, k in keys.items():
        cs.append(case(f"t10_getStorageAt_{tag}", "eth_getStorageAt", [L1BLOCK, k, "latest"],
                       f"alloy#4434: eth_getStorageAt with storage key '{tag}' on the L1Block predeploy"))
        cs.append(case(f"t10_getProof_{tag}", "eth_getProof", [L1BLOCK, [k], "latest"],
                       f"alloy#4434: eth_getProof with storage key '{tag}' on the L1Block predeploy"))
    cs.append(case("t10_getProof_valid_then_long", "eth_getProof",
                   [L1BLOCK, [keys["k32_control"], keys["k33_zero"]], "latest"],
                   "alloy#4434: a valid key followed by a 33-byte key"))
    cs.append(case("t10_getProof_long_then_valid", "eth_getProof",
                   [L1BLOCK, [keys["k33_zero"], keys["k32_control"]], "latest"],
                   "alloy#4434: a 33-byte key followed by a valid key"))
    cs.append(case("t10_getProof_long_key_missing_account", "eth_getProof",
                   [SC, [keys["k33_zero"]], "latest"],
                   "alloy#4434: 33-byte key on an account that does not exist"))
    cs.append(case("t10_getStorageAt_long_key_missing_account", "eth_getStorageAt",
                   [SC, keys["k33_zero"], "latest"],
                   "alloy#4434: 33-byte key on an account that does not exist"))
    return cs


# ------------------------------------------------- revm-inspectors#479/#484, optimism#23270
def access_list_cases():
    ov = {AP: {"code": RT["AccessProbe"]}}
    touch = cd("touch()")
    selfpc = cd("touchSelfAndPrecompile()")
    slot = lambda n: "0x" + hex(n)[2:].zfill(64)
    X = "0x1111111111111111111111111111111111111111"
    Y = "0x6666666666666666666666666666666666666666"
    P2 = "0x0000000000000000000000000000000000000002"
    cs = []

    def al(name, tx, extra, desc, override=ov):
        cs.append(case(name, "eth_createAccessList", [tx, "latest", override], desc))

    base = {"from": F, "to": AP, "data": touch}
    al("t10_createAccessList_touch_many", base, None,
       "Contract reads 2 balances/codes, 4 storage slots, calls 2 addresses and writes slot 7: compare list, ordering and gasUsed")
    al("t10_createAccessList_touch_with_to_in_list", {**base, "accessList": [{"address": AP, "storageKeys": [slot(1)]}]}, None,
       "Request access list already contains `to` with one of the slots the call reads")
    al("t10_createAccessList_touch_with_to_and_extra", {**base, "accessList": [
        {"address": AP, "storageKeys": [slot(2), slot(0x777)]},
        {"address": Y, "storageKeys": [slot(5)]},
        {"address": X, "storageKeys": []}]}, None,
       "Request list holds `to`, an address the call never touches, and an address it does touch")
    al("t10_createAccessList_touch_with_from_and_precompile", {**base, "accessList": [
        {"address": F, "storageKeys": []}, {"address": P2, "storageKeys": []}, {"address": AP, "storageKeys": []}]}, None,
       "Request list holds `from`, a precompile and `to`")
    al("t10_createAccessList_touch_duplicate_entries", {**base, "accessList": [
        {"address": AP, "storageKeys": [slot(1), slot(1)]}, {"address": AP, "storageKeys": [slot(3)]}]}, None,
       "Request list repeats `to` and a storage key")
    al("t10_createAccessList_self_and_precompile", {"from": F, "to": AP, "data": selfpc}, None,
       "Reads own storage and calls a precompile: `to` and precompiles must be handled consistently")
    al("t10_createAccessList_self_with_to_in_list", {"from": F, "to": AP, "data": selfpc,
                                                       "accessList": [{"address": AP, "storageKeys": [slot(9)]}]}, None,
       "Same, with `to` and the slot it reads already in the request list")
    al("t10_createAccessList_touch_gas_explicit", {**base, "gas": "0x30d40"}, None,
       "Explicit gas limit 200000")
    al("t10_createAccessList_touch_1559_fields", {**base, "maxFeePerGas": "0x9184e72a000", "maxPriorityFeePerGas": "0x3b9aca00"}, None,
       "EIP-1559 fee fields on the request (maxFee 10000 gwei, above the devnet base fee)")
    al("t10_createAccessList_touch_value_zero_from_funded", {**base, "value": "0x0"}, None,
       "Explicit zero value", {AP: {"code": RT["AccessProbe"]}, F: {"balance": ETH}})
    al("t10_createAccessList_override_balances_touched", base, None,
       "The touched addresses carry balance/code via the same override set",
       {AP: {"code": RT["AccessProbe"]}, X: {"balance": "0x64"},
        "0x2222222222222222222222222222222222222222": {"code": "0x00"}})
    return cs


# ------------------------------------------------------------------ revm#3805
def simulate_static_cases():
    cs = []

    def sim(name, blocks, desc, **opts):
        req = {"blockStateCalls": blocks}
        req.update(opts)
        cs.append(case(name, "eth_simulateV1", [req, "latest"], desc))

    call_sd = lambda v="0x0", to=SD, data="0x": {"from": F, "to": to, "value": v, "data": data}
    sd_self = {SD: {"code": "0x30ff", "balance": ETH}}
    sd_other = {SD: {"code": "0x73" + A3[2:] + "ff", "balance": ETH}}
    full = {SC: {"code": RT["Scenarios"], "balance": ETH}}

    sim("t10_simulateV1_selfdestruct_self_transfers", [{"stateOverrides": sd_self, "calls": [call_sd()]}],
        "revm#3805: contract with code ADDRESS;SELFDESTRUCT and a balance, traceTransfers on: must not report an extra transfer",
        traceTransfers=True)
    sim("t10_simulateV1_selfdestruct_self_no_transfers", [{"stateOverrides": sd_self, "calls": [call_sd()]}],
        "Control for the case above with traceTransfers off", traceTransfers=False)
    sim("t10_simulateV1_selfdestruct_self_with_value", [{"stateOverrides": {**sd_self, F: {"balance": ETH}}, "calls": [call_sd("0x1")]}],
        "Call carries value: sender->contract transfer, then the contract self-destructs to itself", traceTransfers=True)
    sim("t10_simulateV1_selfdestruct_other_transfers", [{"stateOverrides": sd_other, "calls": [call_sd()]}],
        "Control: selfdestruct to a different address, traceTransfers on (a real transfer must be logged)", traceTransfers=True)
    sim("t10_simulateV1_selfdestruct_self_then_call_again", [{"stateOverrides": sd_self, "calls": [call_sd(), call_sd()]}],
        "Existing contract survives selfdestruct (EIP-6780): second call in the same block still runs the code", traceTransfers=True)
    sim("t10_simulateV1_selfdestruct_self_two_blocks", [{"stateOverrides": sd_self, "calls": [call_sd()]}, {"calls": [call_sd()]}],
        "Same, with the second call in a following simulated block", traceTransfers=True)
    sim("t10_simulateV1_selfdestruct_self_full_tx", [{"stateOverrides": sd_self, "calls": [call_sd()]}],
        "returnFullTransactions on", traceTransfers=True, returnFullTransactions=True)
    sim("t10_simulateV1_scenarios_selfDestructSelf", [{"stateOverrides": full, "calls": [call_sd(to=SC, data=cd("selfDestructSelf()"))]}],
        "Solidity contract selfdestruct(address(this)) via override", traceTransfers=True)
    sim("t10_simulateV1_scenarios_selfDestructOther", [{"stateOverrides": full, "calls": [call_sd(to=SC, data=cd("selfDestructOther(address)", A3))]}],
        "Solidity contract selfdestruct(other) via override", traceTransfers=True)
    sim("t10_simulateV1_scenarios_createAndDestroy", [{"stateOverrides": {**full, F: {"balance": ETH}}, "calls": [call_sd("0x5", SC, cd("createAndDestroy()"))]}],
        "Contract created and destroyed in the same transaction, value 5", traceTransfers=True)
    sim("t10_simulateV1_scenarios_pay_transfers", [{"stateOverrides": {**full, F: {"balance": ETH}}, "calls": [call_sd("0x7", SC, cd("pay(address)", A1))]}],
        "Nested value transfer (pay) with traceTransfers", traceTransfers=True)

    # ---- multi-block, chain independent
    gb = {GB: {"code": RT["GasBurner"]}}
    binfo = {"from": F, "to": GB, "data": cd("blockInfo()")}
    sim("t10_simulateV1_multiblock_default_step", [{"stateOverrides": gb, "calls": [binfo]}, {"calls": [binfo]}, {"calls": [binfo]}],
        "Three blocks, default block number and time step: compare number/timestamp/gaslimit/basefee seen by the contract")
    store = lambda k, v: {"from": F, "to": GB, "data": cd("store(uint256,uint256)", k, v)}
    load = lambda k: {"from": F, "to": GB, "data": cd("load(uint256)", k)}
    sim("t10_simulateV1_write_block1_read_block2",
        [{"stateOverrides": gb, "calls": [load(1), store(1, 66), load(1)]}, {"calls": [load(1), store(1, 77)]}, {"calls": [load(1)]}],
        "Write in block 1 is visible in blocks 2 and 3; overwrite in block 2 is visible in block 3")
    sim("t10_simulateV1_write_read_state_diff",
        [{"stateOverrides": {GB: {"code": RT["GasBurner"], "stateDiff": {"0x" + "00" * 31 + "09": "0x" + "00" * 31 + "2a"}}}, "calls": [load(9)]},
         {"stateOverrides": {GB: {"stateDiff": {"0x" + "00" * 31 + "09": "0x" + "00" * 31 + "2b"}}}, "calls": [load(9)]}],
        "stateDiff in block 1 and block 2 on top of earlier blocks")
    sim("t10_simulateV1_blockoverrides_fields", [{"stateOverrides": gb,
                                                   "blockOverrides": {"gasLimit": "0x1c9c380", "baseFee": "0x7", "feeRecipient": A1},
                                                   "calls": [binfo]}],
        "blockOverrides gasLimit/baseFee/feeRecipient applied to the single simulated block")
    sim("t10_simulateV1_validation_true_nonce_ok", [{"stateOverrides": {**gb, F: {"balance": "0x8ac7230489e80000", "nonce": "0x5"}},
                                                      "calls": [{**binfo, "nonce": "0x5", "gas": "0x30d40", "maxFeePerGas": "0x9184e72a000", "maxPriorityFeePerGas": "0x3b9aca00"}]}],
        "validation on, explicit matching nonce and fee", validation=True)
    return cs


# --------------------------------------------------------- config change #1: 55M / 61M
def gas_cases():
    gb = {GB: {"code": RT["GasBurner"]}}
    cs = []
    G55, G61, G60 = hx(55_000_000), hx(61_000_000), hx(60_000_000)
    burn = lambda n: cd("burn(uint256)", n)
    for g, tag in ((G55, "55M"), (G61, "61M"), (G60, "60M")):
        cs.append(case(f"t10_eth_call_gas_{tag}_simple", "eth_call",
                       [{"from": F, "to": "0x0000000000000000000000000000000000000004", "data": "0x01", "gas": g}, "latest"],
                       f"eth_call with gas={tag} on a trivial identity-precompile call"))
        cs.append(case(f"t10_eth_call_gas_{tag}_burn_400k", "eth_call",
                       [{"from": F, "to": GB, "data": burn(400000), "gas": g}, "latest", gb],
                       f"eth_call with gas={tag} running a ~49M-gas loop"))
        cs.append(case(f"t10_eth_call_gas_{tag}_burn_300k", "eth_call",
                       [{"from": F, "to": GB, "data": burn(300000), "gas": g}, "latest", gb],
                       f"eth_call with gas={tag} running a ~37M-gas loop"))
        cs.append(case(f"t10_estimateGas_gas_{tag}_simple", "eth_estimateGas",
                       [{"from": F, "to": F2, "value": "0x1", "gas": g}, "latest"],
                       f"eth_estimateGas with gas={tag} on a plain transfer (needs balance override on some nodes; compare errors)"))
        cs.append(case(f"t10_estimateGas_gas_{tag}_burn_400k", "eth_estimateGas",
                       [{"from": F, "to": GB, "data": burn(400000), "gas": g}, "latest", gb],
                       f"eth_estimateGas with gas={tag} running a ~49M-gas loop"))
    cs.append(case("t10_estimateGas_no_gas_burn_400k", "eth_estimateGas",
                   [{"from": F, "to": GB, "data": burn(400000)}, "latest", gb],
                   "No gas given: binary search upper bound is the node gas cap, loop needs ~49M"))
    cs.append(case("t10_estimateGas_no_gas_burn_520k", "eth_estimateGas",
                   [{"from": F, "to": GB, "data": burn(520000)}, "latest", gb],
                   "No gas given: loop needs ~63M, above a 60M cap"))
    cs.append(case("t10_eth_call_no_gas_burn_520k", "eth_call",
                   [{"from": F, "to": GB, "data": burn(520000)}, "latest", gb],
                   "eth_call without gas: default is the cap; loop needs ~63M"))
    cs.append(case("t10_eth_call_gas_u64_max_burn_400k", "eth_call",
                   [{"from": F, "to": GB, "data": burn(400000), "gas": "0xffffffffffffffff"}, "latest", gb],
                   "eth_call with gas=2^64-1: how the cap clamps it"))
    return cs


# ------------------------------------------- estimateGas 4th parameter (blockOverrides)
def estimate_override_cases():
    gb = {GB: {"code": RT["GasBurner"]}}
    cs = []
    burn = lambda n: cd("burn(uint256)", n)
    tx = lambda n, **kw: {"from": F, "to": GB, "data": burn(n), **kw}
    poor = "0x00000000000000000000000000000000000f00d1"

    def est(name, txobj, ov, bo, desc, block="latest"):
        cs.append(case(name, "eth_estimateGas", [txobj, block, ov, bo], desc))

    est("t10_estimateGas_bo_gaslimit_lowered_enough", tx(10000), gb, {"gasLimit": "0x1000000"},
        "blockOverrides.gasLimit lowered to 16M, still enough for a ~1.2M-gas call")
    est("t10_estimateGas_bo_gaslimit_lowered_tight", tx(10000), gb, {"gasLimit": "0x13d620"},
        "gasLimit lowered to just above the needed gas (1.3M)")
    est("t10_estimateGas_bo_gaslimit_below_need", tx(100000), gb, {"gasLimit": "0x100000"},
        "gasLimit 1M, the call needs ~12M")
    est("t10_estimateGas_bo_gaslimit_below_intrinsic", {"from": F, "to": F2, "value": "0x1"}, {F: {"balance": ETH}}, {"gasLimit": "0x5207"},
        "gasLimit 20999, below the 21000 intrinsic gas")
    est("t10_estimateGas_bo_gaslimit_equal_intrinsic", {"from": F, "to": F2, "value": "0x1"}, {F: {"balance": ETH}}, {"gasLimit": "0x5208"},
        "gasLimit exactly 21000")
    est("t10_estimateGas_bo_gaslimit_raised_capped", tx(520000), gb, {"gasLimit": "0xffffffffff"},
        "gasLimit raised far above the cap; the call needs ~63M, so the node gas cap decides")
    est("t10_estimateGas_bo_gaslimit_raised_ok", tx(400000), gb, {"gasLimit": "0xffffffffff"},
        "gasLimit raised; the call needs ~49M and fits under a 60M cap")
    est("t10_estimateGas_bo_gaslimit_raised_with_gas", tx(400000, gas="0x3938700"), gb, {"gasLimit": "0x2540be400"},
        "gasLimit raised and explicit gas 60M")
    est("t10_estimateGas_bo_gaslimit_raised_with_gas_61M", tx(400000, gas="0x3a2c940"), gb, {"gasLimit": "0x2540be400"},
        "gasLimit raised and explicit gas 61M (above 60M cap)")
    est("t10_estimateGas_bo_basefee_zero_poor_sender", {"from": poor, "to": GB, "data": burn(1000), "gasPrice": "0x3b9aca00"}, gb, {"baseFee": "0x0"},
        "sender without balance, gasPrice 1 gwei, baseFee overridden to 0")
    est("t10_estimateGas_bo_basefee_zero_rich_sender", {"from": poor, "to": GB, "data": burn(1000), "gasPrice": "0x3b9aca00"},
        {**gb, poor: {"balance": "0x2540be400"}}, {"baseFee": "0x0"},
        "same sender funded by stateOverride (balance 1e10 wei): allowance limited by balance")
    est("t10_estimateGas_bo_basefee_above_maxfee", {"from": F, "to": GB, "data": burn(1000), "maxFeePerGas": "0x3b9aca00", "maxPriorityFeePerGas": "0x1"},
        {**gb, F: {"balance": ETH}}, {"baseFee": "0x174876e800"},
        "maxFeePerGas 1 gwei below overridden baseFee 100 gwei")
    est("t10_estimateGas_bo_basefee_with_1559", {"from": F, "to": GB, "data": burn(1000), "maxFeePerGas": "0x174876e800", "maxPriorityFeePerGas": "0x3b9aca00"},
        {**gb, F: {"balance": "0x2386f26fc10000"}}, {"baseFee": "0x3b9aca00"},
        "1559 fields, baseFee override 1 gwei, balance 0.01 ETH limits the allowance")
    est("t10_estimateGas_bo_basefee_with_gasprice_balance_limited", {"from": poor, "to": GB, "data": burn(100000), "gasPrice": "0x3b9aca00"},
        {**gb, poor: {"balance": "0x2386f26fc10000"}}, {"baseFee": "0x1"},
        "balance 0.01 ETH, gasPrice 1 gwei: allowance 10M gas, the call needs ~12M")
    est("t10_estimateGas_bo_gaslimit_and_basefee_and_state", {"from": F, "to": GB, "data": cd("store(uint256,uint256)", 3, 4)},
        {**gb, F: {"balance": ETH}}, {"gasLimit": "0x2faf080", "baseFee": "0x3b9aca00", "number": "0x64", "time": "0x1000"},
        "gasLimit, baseFee, number and time overridden together with a storage write")
    est("t10_estimateGas_bo_number_time_only", tx(1000), gb, {"number": "0x64", "time": "0x1000"},
        "only number/time overridden")
    est("t10_estimateGas_bo_empty_object", tx(1000), gb, {}, "empty blockOverrides")
    est("t10_estimateGas_bo_null", tx(1000), gb, None, "null blockOverrides")
    est("t10_estimateGas_bo_null_state_override", tx(1000), None, {"gasLimit": "0x1000000"},
        "null stateOverride followed by blockOverrides (needs a deployed target; compare errors)")
    est("t10_estimateGas_bo_unknown_field", tx(1000), gb, {"nonsense": "0x1"}, "unknown blockOverrides field")
    est("t10_estimateGas_bo_gaslimit_zero", tx(1000), gb, {"gasLimit": "0x0"}, "gasLimit 0")
    est("t10_estimateGas_bo_gaslimit_with_state_override_nonce", {"from": F, "to": GB, "data": burn(1000), "nonce": "0x9"},
        {**gb, F: {"balance": ETH, "nonce": "0x9"}}, {"gasLimit": "0x1000000"}, "matching nonce override plus lowered gasLimit")

    # eth_call: confirm the overrides are visible to the contract
    binfo = {"from": F, "to": GB, "data": cd("blockInfo()")}
    cs.append(case("t10_eth_call_bo_visible_fields", "eth_call",
                   [binfo, "latest", gb, {"number": "0x64", "time": "0x1000", "gasLimit": "0x1234567", "baseFee": "0x7"}],
                   "blockOverrides seen from inside the contract: number/timestamp/gaslimit/basefee"))
    cs.append(case("t10_eth_call_bo_gaslimit_only", "eth_call",
                   [binfo, "latest", gb, {"gasLimit": "0x1000000"}], "only gasLimit overridden"))
    cs.append(case("t10_eth_call_bo_basefee_zero", "eth_call",
                   [binfo, "latest", gb, {"baseFee": "0x0"}], "baseFee 0"))
    return cs


# --------------------------------------------------------------- tracers (static)
BUILTIN = [
    ("callTracer", "callTracer", {}),
    ("callTracer_withLog", "callTracer", {"withLog": True}),
    ("callTracer_onlyTopCall", "callTracer", {"onlyTopCall": True}),
    ("callTracer_onlyTopCall_withLog", "callTracer", {"onlyTopCall": True, "withLog": True}),
    ("prestateTracer", "prestateTracer", {}),
    ("prestateTracer_diffMode", "prestateTracer", {"diffMode": True}),
    ("prestateTracer_disableCodeStorage", "prestateTracer", {"disableCode": True, "disableStorage": True}),
    ("prestateTracer_diffMode_disableCode", "prestateTracer", {"diffMode": True, "disableCode": True}),
    ("prestateTracer_includeEmpty", "prestateTracer", {"includeEmpty": True}),
    ("4byteTracer", "4byteTracer", None),
    ("flatCallTracer", "flatCallTracer", None),
    ("flatCallTracer_convertParity", "flatCallTracer", {"convertParityErrors": True}),
    ("muxTracer", "muxTracer", {"callTracer": None, "4byteTracer": None, "prestateTracer": {"diffMode": True}}),
    ("muxTracer_call_cfg", "muxTracer", {"callTracer": {"withLog": True}, "flatCallTracer": None}),
]
STRUCT = [
    ("structLogger", {}),
    ("structLogger_enableMemory", {"enableMemory": True}),
    ("structLogger_enableReturnData", {"enableReturnData": True}),
    ("structLogger_disableStack", {"disableStack": True}),
    ("structLogger_disableStorage", {"disableStorage": True}),
    ("structLogger_limit5", {"limit": 5}),
    ("structLogger_all_opts", {"enableMemory": True, "enableReturnData": True, "disableStorage": True, "limit": 30}),
    ("structLogger_cleanStructLogs", {"cleanStructLogs": True}),
]


def builtin_config(tracer, cfg):
    c = {"tracer": tracer}
    if cfg is not None:
        c["tracerConfig"] = cfg
    return c


def js_sources():
    d = os.path.join(HERE, "js-tracers")
    out = {}
    for fn in sorted(os.listdir(d)):
        if not fn.endswith(".js"):
            continue
        lines = [l for l in open(os.path.join(d, fn)).read().splitlines() if not l.startswith("//")]
        out[fn[:-3]] = "\n".join(lines).strip()
    return out


JS = js_sources()
assert len(JS) == 9, list(JS)

# scenario calls (name, to, data, value)
SCENARIO_CALLS = [
    ("doRevert", cd("doRevert()"), "0x0"),
    ("doRevertCustom", cd("doRevertCustom()"), "0x0"),
    ("staticWrite", cd("staticWrite()"), "0x0"),
    ("selfDestructSelf", cd("selfDestructSelf()"), "0x0"),
    ("selfDestructOther", cd("selfDestructOther(address)", A3), "0x0"),
    ("createAndDestroy", cd("createAndDestroy()"), "0x5"),
    ("delegateWithValue", cd("delegateWithValue(address)", SC), "0x3e8"),
    ("precompiles", cd("precompiles()"), "0x0"),
    ("sstoreClear", cd("sstoreClear()"), "0x0"),
    ("logThenRevert", cd("logThenRevert()"), "0x0"),
    ("createOk", cd("createOk()"), "0x0"),
    ("createFail", cd("createFail()"), "0x0"),
    ("create2Fail", cd("create2Fail(bytes32)", SALT), "0x0"),
    ("pay", cd("pay(address)", A1), "0x7"),
    ("deep6", cd("deep(uint256)", 6), "0x0"),
    ("rc_undefined_opcode", cd("runCode(bytes,bytes)", "0x0c", "0x"), "0x0"),
    ("rc_invalid_jump", cd("runCode(bytes,bytes)", "0x600156", "0x"), "0x0"),
    ("rc_invalid_opcode", cd("runCode(bytes,bytes)", "0xfe", "0x"), "0x0"),
    ("rc_stack_underflow", cd("runCode(bytes,bytes)", "0x01", "0x"), "0x0"),
    ("rc_inner_oog", cd("runCode(bytes,bytes)", "0x5b600056", "0x"), "0x0"),
    ("rc_delegate_value", cd("runCode(bytes,bytes)", "0x600060006000600060045af400", "0x"), "0x3"),
]
# explicit gas: without it each client picks its own default (block limit vs gas cap) and result.gas differs
TRACE_GAS = "0x7a120"
SC_OVERRIDE = {SC: {"code": RT["Scenarios"], "balance": ETH}, F: {"balance": ETH}}


def trace_call(name, data, value, config, desc):
    tx = {"from": F, "to": SC, "data": data, "value": value, "gas": TRACE_GAS}
    return case(name, "debug_traceCall", [tx, "latest", {**config, "stateOverrides": SC_OVERRIDE}], desc)


def trace_option_cases():
    cs = []
    for cname, data, value in SCENARIO_CALLS:
        for tag, tracer, cfg in BUILTIN:
            cs.append(trace_call(f"t10_traceCall_{cname}_{tag}", data, value, builtin_config(tracer, cfg),
                                 f"debug_traceCall of scenario '{cname}' with {tag}"))
        if cname != "deep6":
            for tag, cfg in STRUCT:
                cs.append(trace_call(f"t10_traceCall_{cname}_{tag}", data, value, dict(cfg),
                                     f"debug_traceCall of scenario '{cname}' with {tag}"))
    return cs


def js_tracer_cases():
    cs = []
    pick = ["createAndDestroy", "staticWrite", "doRevert", "selfDestructSelf", "precompiles", "createFail",
            "delegateWithValue", "rc_invalid_opcode", "rc_inner_oog", "sstoreClear"]
    calls = {n: (d, v) for n, d, v in SCENARIO_CALLS}
    for tname, src in JS.items():
        for cname in pick:
            d, v = calls[cname]
            cs.append(trace_call(f"t10_traceCall_{cname}_js_{tname}", d, v, {"tracer": src},
                                 f"geth's bundled JS tracer {tname} as a custom tracer on scenario '{cname}'"))
    return cs


def js_script_cases():
    cs = []
    calls = {n: (d, v) for n, d, v in SCENARIO_CALLS}
    gb = {GB: {"code": RT["GasBurner"], "balance": "0x64", "nonce": "0x3"}, F: {"balance": ETH}}

    def on_sc(name, script, cname, desc, tracer_config=None):
        d, v = calls[cname]
        cfg = {"tracer": script, "stateOverrides": SC_OVERRIDE}
        if tracer_config is not None:
            cfg["tracerConfig"] = tracer_config
        cs.append(case(f"t10_jsScript_{name}", "debug_traceCall", [{"from": F, "to": SC, "data": d, "value": v, "gas": TRACE_GAS}, "latest", cfg], desc))

    def on_gb(name, script, desc):
        cs.append(case(f"t10_jsScript_{name}", "debug_traceCall",
                       [{"from": F, "to": GB, "data": cd("load(uint256)", 1), "gas": TRACE_GAS}, "latest", {"tracer": script, "stateOverrides": gb}], desc))

    cap = "if(this.s.length<%d)"
    on_gb("log_basic", "{s:[],step:function(log,db){if(this.s.length<60)this.s.push([log.getPC(),log.op.toString(),log.getGas(),log.getCost(),log.getDepth(),log.getRefund()]);},fault:function(){},result:function(){return this.s;}}",
          "log.getPC/getGas/getCost/getDepth/getRefund and op.toString per step")
    on_gb("log_op_methods", "{s:[],step:function(log,db){if(this.s.length<40)this.s.push([log.op.toNumber(),log.op.isPush(),log.op.toString()]);},fault:function(){},result:function(){return this.s;}}",
          "op.toNumber/isPush/toString")
    on_gb("log_getError_no_error", "{e:[],step:function(log,db){var x=log.getError();if(x!==undefined&&x!==null&&x!=='')this.e.push(x);},fault:function(){},result:function(){return this.e;}}",
          "log.getError() on a clean execution")
    on_sc("log_getError_fault", "{e:[],step:function(log,db){var x=log.getError();if(x)this.e.push([log.op.toString(),x]);},fault:function(log){this.e.push(['fault',log.getError()]);},result:function(){return this.e;}}",
          "rc_invalid_opcode", "log.getError() in step and fault when the call hits INVALID")
    on_sc("log_getError_stack_underflow", "{e:[],step:function(log,db){var x=log.getError();if(x)this.e.push([log.op.toString(),x]);},fault:function(log){this.e.push(['fault',log.getError()]);},result:function(){return this.e;}}",
          "rc_stack_underflow", "same with stack underflow")
    on_gb("stack_ops", "{s:[],step:function(log,db){if(this.s.length<50){var n=log.stack.length();this.s.push([n,n>0?log.stack.peek(0).toString(16):null,n>1?log.stack.peek(1).toString():null]);}},fault:function(){},result:function(){return this.s;}}",
          "stack.length/peek")
    on_gb("stack_peek_oob", "{r:null,step:function(log,db){if(log.getPC()==0){try{this.r=log.stack.peek(50).toString();}catch(e){this.r='err:'+e;}}},fault:function(){},result:function(){return this.r;}}",
          "stack.peek beyond the stack depth, error caught inside the script")
    on_gb("stack_peek_oob_uncaught", "{step:function(log,db){if(log.getPC()==0){log.stack.peek(50);}},fault:function(){},result:function(){return 1;}}",
          "stack.peek beyond the stack depth, error not caught")
    on_gb("memory_ops", "{s:[],step:function(log,db){if(this.s.length<80&&log.memory.length()>=64){this.s.push([log.memory.length(),toHex(log.memory.slice(0,32)),log.memory.getUint(0).toString(16),log.memory.getUint(32).toString(16)]);}},fault:function(){},result:function(){return this.s;}}",
          "memory.length/slice/getUint")
    on_gb("memory_slice_oob_caught", "{r:null,step:function(log,db){if(log.memory.length()>=64&&this.r===null){try{this.r=toHex(log.memory.slice(100000,100032));}catch(e){this.r='err:'+e;}}},fault:function(){},result:function(){return this.r;}}",
          "memory.slice outside of memory, error caught inside the script")
    on_gb("memory_slice_oob_uncaught", "{step:function(log,db){if(log.memory.length()>=64){log.memory.slice(100000,100032);}},fault:function(){},result:function(){return 1;}}",
          "memory.slice outside of memory, error not caught")
    on_gb("memory_getuint_oob", "{r:null,step:function(log,db){if(log.memory.length()>=64&&this.r===null){try{this.r=log.memory.getUint(100000).toString();}catch(e){this.r='err:'+e;}}},fault:function(){},result:function(){return this.r;}}",
          "memory.getUint outside of memory")
    on_gb("memory_slice_negative", "{r:null,step:function(log,db){if(log.memory.length()>=64&&this.r===null){try{this.r=toHex(log.memory.slice(10,2));}catch(e){this.r='err:'+e;}}},fault:function(){},result:function(){return this.r;}}",
          "memory.slice with end before start")
    on_gb("contract_fields", "{s:[],step:function(log,db){if(this.s.length<3){var c=log.contract;this.s.push([toHex(c.getCaller()),toHex(c.getAddress()),c.getValue().toString(),toHex(c.getInput())]);}},fault:function(){},result:function(){return this.s;}}",
          "contract.getCaller/getAddress/getValue/getInput")
    on_sc("contract_fields_nested", "{s:[],step:function(log,db){if(log.getDepth()>=2&&this.s.length<3){var c=log.contract;this.s.push([log.getDepth(),toHex(c.getCaller()),toHex(c.getAddress()),c.getValue().toString(),toHex(c.getInput())]);}},fault:function(){},result:function(){return this.s;}}",
          "createAndDestroy", "contract fields inside a nested frame")
    on_gb("db_methods", "{r:null,step:function(log,db){},fault:function(){},result:function(ctx,db){var a=toAddress('0x00000000000000000000000000000000000b0001');return [db.getBalance(a).toString(),db.getNonce(a),toHex(db.getCode(a)).length,db.exists(a),db.getState(a,'0x0000000000000000000000000000000000000000000000000000000000000001').toString(),db.exists(toAddress('0x00000000000000000000000000000000deadbeef'))];}}",
          "db.getBalance/getNonce/getCode/exists/getState with a hex-string slot")
    on_gb("db_hex_string_args", "{step:function(log,db){},fault:function(){},result:function(ctx,db){return [db.getBalance('0x00000000000000000000000000000000000b0001').toString(),db.getNonce('0x00000000000000000000000000000000000b0001'),db.exists('0x00000000000000000000000000000000000b0001'),toHex(db.getCode('0x00000000000000000000000000000000000b0001')).length];}}",
          "db methods called with 0x-strings instead of byte arrays")
    on_sc("db_getState_inside_tx", "{r:[],step:function(log,db){if(log.op.toString()=='SSTORE'&&this.r.length<4){this.r.push(db.getState(log.contract.getAddress(),toWord(log.stack.peek(0).toString(16))).toString());}},fault:function(){},result:function(){return this.r;}}",
          "sstoreClear", "db.getState sees writes made earlier in the same transaction")
    on_sc("ctx_fields", "{step:function(){},fault:function(){},result:function(ctx,db){var o={};var k=['type','from','to','input','gas','gasUsed','gasPrice','intrinsicGas','value','block','output','time','error'];for(var i=0;i<k.length;i++){var v=ctx[k[i]];if(v===undefined){o[k[i]]='undefined';}else if(typeof v==='object'&&v!==null&&v.length!==undefined&&typeof v!=='string'){o[k[i]]=toHex(v);}else{o[k[i]]=String(v);}}return o;}}",
          "createAndDestroy", "ctx fields including intrinsicGas, time and error on a successful call")
    on_sc("ctx_fields_revert", "{step:function(){},fault:function(){},result:function(ctx,db){return [ctx.type,String(ctx.error),toHex(ctx.output),String(ctx.gasUsed),String(ctx.intrinsicGas)];}}",
          "doRevert", "ctx.error/output/gasUsed when the call reverts")
    on_sc("ctx_fields_invalid", "{step:function(){},fault:function(){},result:function(ctx,db){return [ctx.type,String(ctx.error),toHex(ctx.output)];}}",
          "rc_invalid_opcode", "ctx.error after an invalid opcode in a nested frame")
    on_sc("enter_exit", "{e:[],step:function(){},fault:function(){},enter:function(cf){this.e.push(['enter',cf.getType(),toHex(cf.getFrom()),toHex(cf.getTo()),toHex(cf.getInput()),cf.getGas(),String(cf.getValue())]);},exit:function(fr){this.e.push(['exit',fr.getGasUsed(),toHex(fr.getOutput()),String(fr.getError())]);},result:function(){return this.e;}}",
          "createAndDestroy", "enter/exit callbacks with all frame getters, on create + selfdestruct")
    on_sc("enter_exit_precompiles", "{e:[],step:function(){},fault:function(){},enter:function(cf){this.e.push(['enter',cf.getType(),toHex(cf.getTo()),cf.getGas()]);},exit:function(fr){this.e.push(['exit',fr.getGasUsed(),toHex(fr.getOutput()),String(fr.getError())]);},result:function(){return this.e;}}",
          "precompiles", "enter/exit around precompile calls")
    on_sc("enter_exit_revert", "{e:[],step:function(){},fault:function(){},enter:function(cf){this.e.push(['enter',cf.getType(),toHex(cf.getTo())]);},exit:function(fr){this.e.push(['exit',fr.getGasUsed(),toHex(fr.getOutput()),String(fr.getError())]);},result:function(){return this.e;}}",
          "createFail", "exit callback for a failing CREATE")
    on_gb("globals", "{step:function(){},fault:function(){},result:function(){return [toHex([1,2,255]),toHex(toAddress('0x00000000000000000000000000000000000000ff')),toHex(toWord('0x01')),isPrecompiled(toAddress('0x0000000000000000000000000000000000000002')),isPrecompiled(toAddress('0x00000000000000000000000000000000000000f0')),toHex(slice([1,2,3,4,5],1,3)),toHex(toContract(toAddress('0x00000000000000000000000000000000000b0001'),1)),toHex(toContract2(toAddress('0x00000000000000000000000000000000000b0001'),'0x00',[1,2,3]))];}}",
          "toHex/toAddress/toWord/isPrecompiled/slice/toContract/toContract2")
    on_gb("globals_precompile_range", "{step:function(){},fault:function(){},result:function(){var r=[];for(var i=0;i<=20;i++){r.push(isPrecompiled(toAddress('0x'+('00'+i.toString(16)).slice(-2).padStart(40,'0'))));}return r;}}",
          "isPrecompiled for addresses 0x00..0x14 (fork-dependent precompile set)")
    on_gb("bigint_methods", "{step:function(){},fault:function(){},result:function(){var a=bigInt('123456789012345678901234567890');var b=bigInt(1000);return [a.multiply(b).toString(),a.divide(b).toString(),a.mod(b).toString(),a.pow(2).toString(),a.compare(b),a.shiftLeft(3).toString(),a.and(255).toString(),a.toString(16),a.isZero(),b.negate().toString(),a.minus(a).isZero(),bigInt('ff',16).toString()];}}",
          "bigInt: multiply/divide/mod/pow/compare/shiftLeft/and/toString(16)/negate/parse with radix")
    on_gb("bigint_result_type", "{step:function(){},fault:function(){},result:function(){return bigInt(5).plus(7);}}",
          "result is a bigInt object (serialisation)")
    on_gb("throw_in_setup", "{setup:function(cfg){throw 'boom setup';},step:function(){},fault:function(){},result:function(){return 1;}}",
          "setup throws")
    on_gb("throw_in_step", "{step:function(){throw 'boom step';},fault:function(){},result:function(){return 1;}}",
          "step throws")
    on_gb("throw_in_result", "{step:function(){},fault:function(){},result:function(){throw 'boom result';}}",
          "result throws")
    on_sc("throw_in_fault", "{step:function(){},fault:function(){throw 'boom fault';},result:function(){return 1;}}",
          "rc_invalid_opcode", "fault throws (fault is called for the INVALID opcode)")
    on_sc("fault_called_invalid_opcode", "{f:[],step:function(){},fault:function(log){this.f.push([log.getPC(),String(log.getError())]);},result:function(){return this.f;}}",
          "rc_invalid_opcode", "does fault fire for INVALID in a nested frame, and with what error")
    on_sc("fault_called_invalid_jump", "{f:[],step:function(){},fault:function(log){this.f.push([log.getPC(),String(log.getError())]);},result:function(){return this.f;}}",
          "rc_invalid_jump", "does fault fire for an invalid jump destination")
    on_sc("fault_called_undefined_opcode", "{f:[],step:function(){},fault:function(log){this.f.push([log.getPC(),String(log.getError())]);},result:function(){return this.f;}}",
          "rc_undefined_opcode", "does fault fire for an undefined opcode")
    on_sc("fault_called_inner_oog", "{f:[],step:function(){},fault:function(log){this.f.push([log.getPC(),String(log.getError())]);},result:function(){return this.f.length;}}",
          "rc_inner_oog", "does fault fire when the nested frame runs out of gas")
    on_sc("fault_called_revert", "{f:[],step:function(){},fault:function(log){this.f.push(String(log.getError()));},result:function(){return this.f;}}",
          "doRevert", "does fault fire on a plain revert (it should not)")
    on_gb("setup_config", "{cfg:null,setup:function(cfg){this.cfg=cfg;},step:function(){},fault:function(){},result:function(){return [typeof this.cfg,JSON.stringify(this.cfg)];}}",
          "setup receives the tracerConfig as JSON text")
    cs[-1]["params"][2]["tracerConfig"] = {"a": 1, "b": "x", "c": [1, 2]}
    on_gb("setup_config_absent", "{cfg:'unset',setup:function(cfg){this.cfg=cfg;},step:function(){},fault:function(){},result:function(){return [typeof this.cfg,JSON.stringify(this.cfg)];}}",
          "setup called without a tracerConfig")
    on_gb("missing_result", "{step:function(){},fault:function(){}}", "tracer without result()")
    on_gb("missing_step", "{result:function(){return 1;},fault:function(){}}", "tracer without step()")
    on_gb("missing_fault", "{step:function(){},result:function(){return 1;}}", "tracer without fault()")
    on_gb("syntax_error", "{step:function(){,}", "tracer source with a syntax error")
    on_gb("infinite_loop_result", "{step:function(){},fault:function(){},result:function(){var i=0;while(i<1e5){i++;}return i;}}",
          "bounded busy loop in result")
    on_gb("deep_recursion", "{step:function(){},fault:function(){},result:function(){function f(n){return n==0?0:1+f(n-1);}return f(500);}}",
          "recursion depth 500 in the script (JS engine stack limit boundary is checked in T28)")
    on_gb("return_types", "{step:function(){},fault:function(){},result:function(){return {n:1,s:'a',b:true,z:null,a:[1,'x'],u:undefined,big:bigInt(2).pow(70).toString()};}}",
          "result returning mixed JSON types")
    on_gb("this_state_between_steps", "{n:0,m:0,step:function(log){this.n++;if(log.getPC()==0)this.m++;},fault:function(){},result:function(){return [this.n,this.m];}}",
          "state kept on `this` across steps; step count and PC==0 count")
    return cs


def main():
    write("t10_storage_key_len.json", storage_key_cases())
    write("t10_access_list.json", access_list_cases())
    write("t10_simulate_static.json", simulate_static_cases())
    write("t10_gas_limits.json", gas_cases())
    write("t10_estimate_block_overrides.json", estimate_override_cases())
    write("t10_trace_options.json", trace_option_cases())
    write("t10_trace_js_bundled.json", js_tracer_cases())
    write("t10_trace_js_scripts.json", js_script_cases())


if __name__ == "__main__":
    main()
