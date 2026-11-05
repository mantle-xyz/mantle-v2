#!/usr/bin/env python3
"""Generates the chain-specific T10 corpus (deployed fixtures, cutoff/Arsia blocks, token-ratio update block).

The cases embed transaction hashes, block numbers and block hashes of ONE devnet, so
they are written to a separate directory and are NOT part of the embedded corpus.
Run them with:  op-rpc-compat --testcases-dir <out> ...

usage: gen_chain.py --fixtures fixtures.json --rpc URL --arsia N --cutoff N [--ratio-block N] --out DIR
"""
import argparse
import json
import os
import subprocess
import sys
import urllib.request

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import gen_static as S  # noqa: E402  (module-level code only compiles contracts)

ap = argparse.ArgumentParser()
ap.add_argument("--fixtures", required=True)
ap.add_argument("--rpc", required=True, help="baseline RPC, used to read hashes/timestamps/raw blocks")
ap.add_argument("--arsia", type=int, required=True, help="Arsia activation block")
ap.add_argument("--cutoff", type=int, required=True, help="history-forwarding cutoff block")
ap.add_argument("--ratio-block", type=int, default=0, help="block containing a token-ratio update (0 = none available)")
ap.add_argument("--out", required=True)
args = ap.parse_args()
FX = json.load(open(args.fixtures))
TX = FX["txs"]
SCEN = FX["scenarios"]
LOGE = FX["logEmitter"]
h = hex


def rpc(method, params):
    req = urllib.request.Request(args.rpc, json.dumps({"jsonrpc": "2.0", "id": 1, "method": method, "params": params}).encode(),
                                 {"content-type": "application/json"})
    body = json.load(urllib.request.urlopen(req, timeout=60))
    if "error" in body:
        raise SystemExit(f"{method}{params}: {body['error']}")
    return body["result"]


def block(n):
    return rpc("eth_getBlockByNumber", [h(n), False])


def write(fname, cases):
    names = [c["name"] for c in cases]
    assert len(names) == len(set(names)), "duplicate case names in " + fname
    os.makedirs(args.out, exist_ok=True)
    with open(os.path.join(args.out, fname), "w") as f:
        # one case per line keeps the large trace corpora small and diffable
        f.write("[\n" + ",\n".join("  " + json.dumps(c, separators=(",", ":")) for c in cases) + "\n]\n")
    print(fname, len(cases))


case = S.case
C, A = args.cutoff, args.arsia


# ------------------------------------------------------------- trace on deployed txs
def trace_tx_cases():
    cs = []
    js_txs = {"sc_selfdestruct_self", "sc_create_and_destroy", "sc_static_write", "sc_revert", "sc_precompiles",
              "sc_create_fail", "sc_sstore_clear", "sc_log_then_revert", "rc_invalid_opcode", "rc_inner_oog",
              "sc_oog", "tx_create_fail", "tx_transfer", "sc_deep_20", "rc_delegate_value", "sc_delegate_value"}
    for name, t in TX.items():
        if name.startswith("deploy_LogEmitter") or name.startswith("logs_") or name.startswith("fund_"):
            continue
        small = int(t["gasUsed"], 16) < 1_000_000
        for tag, tracer, cfg in S.BUILTIN:
            cs.append(case(f"t10_traceTx_{name}_{tag}", "debug_traceTransaction", [t["hash"], S.builtin_config(tracer, cfg)],
                           f"debug_traceTransaction of {name} with {tag}"))
        if small:
            for tag, cfg in S.STRUCT:
                cs.append(case(f"t10_traceTx_{name}_{tag}", "debug_traceTransaction", [t["hash"], dict(cfg)],
                               f"debug_traceTransaction of {name} with {tag}"))
        if name in js_txs and name != "sc_deep_overflow":
            for tname, src in S.JS.items():
                cs.append(case(f"t10_traceTx_{name}_js_{tname}", "debug_traceTransaction", [t["hash"], {"tracer": src}],
                               f"bundled JS tracer {tname} on {name}"))
    return cs


def trace_block_cases():
    cs = []
    picks = ["sc_selfdestruct_self", "sc_create_and_destroy", "sc_revert", "sc_log_then_revert", "sc_create_fail",
             "rc_invalid_opcode", "sc_deep_20", "tx_create_fail", "sc_static_write", "logs_burst_1"]
    cfgs = [("callTracer", "callTracer", {}), ("callTracer_withLog", "callTracer", {"withLog": True}),
            ("callTracer_onlyTopCall", "callTracer", {"onlyTopCall": True}),
            ("prestateTracer", "prestateTracer", {}), ("prestateTracer_diffMode", "prestateTracer", {"diffMode": True}),
            ("4byteTracer", "4byteTracer", None), ("flatCallTracer", "flatCallTracer", None),
            ("muxTracer", "muxTracer", {"callTracer": None, "4byteTracer": None, "prestateTracer": {"diffMode": True}})]
    for name in picks:
        t = TX[name]
        for tag, tracer, cfg in cfgs:
            c = S.builtin_config(tracer, cfg)
            cs.append(case(f"t10_traceBlockByNumber_{name}_{tag}", "debug_traceBlockByNumber", [t["block"], c],
                           f"debug_traceBlockByNumber of the block holding {name}, {tag}"))
            cs.append(case(f"t10_traceBlockByHash_{name}_{tag}", "debug_traceBlockByHash", [t["blockHash"], c],
                           f"debug_traceBlockByHash of the block holding {name}, {tag}"))
        cs.append(case(f"t10_traceBlockByNumber_{name}_structLogger", "debug_traceBlockByNumber", [t["block"], {"disableStorage": True, "limit": 20}],
                       f"structLogger (limit 20) over the whole block holding {name}"))
    for name in ("sc_create_and_destroy", "sc_precompiles"):
        raw = rpc("debug_getRawBlock", [TX[name]["block"]])
        for tag, tracer, cfg in cfgs[:5]:
            cs.append(case(f"t10_traceBlockRLP_{name}_{tag}", "debug_traceBlock", [raw, S.builtin_config(tracer, cfg)],
                           f"debug_traceBlock (RLP) for the block holding {name}, {tag}"))
    return cs


# ------------------------------------------------------------------- logs / feeHistory
def logs_cases():
    log_blocks = sorted(int(TX[n]["block"], 16) for n in TX if n.startswith("logs_"))
    lo, hi = log_blocks[0], log_blocks[-1]
    sig = subprocess.check_output([S.CAST, "keccak", "Ping(uint256,address,bytes)"]).decode().strip()
    plain = subprocess.check_output([S.CAST, "keccak", "Plain(uint256,uint256)"]).decode().strip()
    w = lambda n: "0x" + h(n)[2:].zfill(64)
    cs = []

    def gl(name, flt, desc):
        cs.append(case(name, "eth_getLogs", [flt], desc))

    rng = [(0, C - 1, "below_cutoff_only"), (C - 1, C, "cross_by_one"), (C, C, "cutoff_block"), (C - 1, C + 1, "straddle"),
           (0, C + 1, "from_genesis_just_past"), (max(C - 100, 0), hi, "wide_cross_to_fixtures"), (0, hi, "genesis_to_fixtures"),
           (C, hi, "from_cutoff"), (C + 1, hi, "after_cutoff"), (lo, hi, "fixtures_only"), (C - 1, lo, "cross_to_first_fixture")]
    for a, b, tag in rng:
        gl(f"t10_getLogs_{tag}", {"fromBlock": h(a), "toBlock": h(b)}, f"unfiltered getLogs [{a}, {b}] around the cutoff {C}")
        gl(f"t10_getLogs_{tag}_emitter", {"fromBlock": h(a), "toBlock": h(b), "address": LOGE},
           f"getLogs [{a}, {b}] filtered by the LogEmitter address")
    gl("t10_getLogs_topic0", {"fromBlock": h(max(C - 100, 0)), "toBlock": h(hi), "topics": [sig]}, "topic0 = Ping across the cutoff")
    gl("t10_getLogs_topic_id100", {"fromBlock": h(0), "toBlock": h(hi), "topics": [sig, w(100)]}, "topic1 = id 100")
    gl("t10_getLogs_topic_id_or", {"fromBlock": h(0), "toBlock": h(hi), "topics": [sig, [w(100), w(200), w(1)]]}, "topic1 OR-list")
    gl("t10_getLogs_topic_wildcard", {"fromBlock": h(0), "toBlock": h(hi), "topics": [None, w(101)]}, "null topic0 wildcard")
    gl("t10_getLogs_topic0_or", {"fromBlock": h(0), "toBlock": h(hi), "topics": [[sig, plain]]}, "topic0 OR-list (Ping or Plain)")
    gl("t10_getLogs_address_list", {"fromBlock": h(C - 1), "toBlock": h(hi), "address": [LOGE, "0x4200000000000000000000000000000000000015"]},
       "address list across the cutoff")
    gl("t10_getLogs_cutoff_block_topic", {"fromBlock": h(C - 1), "toBlock": h(C + 1),
                                          "topics": [subprocess.check_output([S.CAST, "keccak", "Initialized(uint8)"]).decode().strip()]},
       "Initialized(uint8) logs emitted by the Arsia upgrade transactions in the cutoff block")
    gl("t10_getLogs_blockhash_fixture", {"blockHash": TX["logs_burst_1"]["blockHash"]}, "blockHash filter, block with logs")
    gl("t10_getLogs_blockhash_cutoff", {"blockHash": block(C)["hash"]}, "blockHash filter, the cutoff block")
    gl("t10_getLogs_blockhash_below_cutoff", {"blockHash": block(C - 1)["hash"]}, "blockHash filter, block below the cutoff")
    gl("t10_getLogs_blockhash_first_above_cutoff", {"blockHash": block(C + 1)["hash"]}, "blockHash filter, first block above the cutoff")
    gl("t10_getLogs_numbers_reversed", {"fromBlock": h(hi), "toBlock": h(C - 1)}, "fromBlock > toBlock across the cutoff")
    # fee history
    fh = lambda n, newest, pct=None: [h(n), h(newest)] + ([pct] if pct is not None else [[]])
    for n, newest, pct, tag in [(1, C - 1, [50], "newest_below_cutoff"), (1, C, [50], "newest_cutoff"), (5, C + 2, [10, 50, 90], "cross_5"),
                                (10, C - 1, [25, 75], "below_cutoff_10"), (3, C, [0, 100], "ending_at_cutoff"),
                                (30, hi, [10, 50, 90], "cross_30_to_fixtures"), (hi, hi, [0, 25, 50, 75, 100], "whole_chain_to_fixtures"),
                                (C + 5, C + 4, [50], "oldest_is_block_0_or_1_edge"), (2, hi, [], "no_percentiles"),
                                (4, C + 1, [99.5], "fractional_percentile")]:
        cs.append(case(f"t10_feeHistory_{tag}", "eth_feeHistory", fh(n, newest, pct),
                       f"eth_feeHistory count={n} newest={newest} around the cutoff {C}: oldest/baseFee/gasUsedRatio/reward"))
    return cs


def fee_update_cases():
    r = args.ratio_block
    cs = []
    if not r:
        # No token-ratio update block on this chain: only the Arsia boundary is covered, the ratio itself is not.
        for tag, n in (("before", C - 1), ("arsia_block", C), ("after", C + 1)):
            cs.append(case(f"t10_estimateTotalFee_arsia_boundary_{tag}", "eth_estimateTotalFee",
                           [{"from": S.F, "to": S.F2, "value": "0x1"}, h(n)],
                           f"NOT a token-ratio test: eth_estimateTotalFee at block {n} around the Arsia activation block {C}"))
        return cs
    for tag, n in (("before", r - 1), ("update_block", r), ("after", r + 1)):
        for ttag, tx in (("transfer", {"from": S.F, "to": S.F2, "value": "0x1"}),
                         ("data", {"from": S.F, "to": S.F2, "data": "0x" + "ab" * 200}),
                         ("fees", {"from": S.F, "to": S.F2, "maxFeePerGas": "0x77359400", "maxPriorityFeePerGas": "0x1"})):
            cs.append(case(f"t10_estimateTotalFee_ratio_{tag}_{ttag}", "eth_estimateTotalFee", [tx, h(n)],
                           f"token ratio update in block {r}: eth_estimateTotalFee at block {n} ({tag}); must use that block's state"))
        cs.append(case(f"t10_estimateGas_ratio_{tag}", "eth_estimateGas", [{"from": S.F, "to": S.F2, "value": "0x1"}, h(n), {S.F: {"balance": S.ETH}}],
                       f"eth_estimateGas at block {n} ({tag} the ratio update)"))
    return cs


# ------------------------------------------------------------ T9: pre-Arsia gas
def pre_arsia_cases():
    cs = []
    pres = sorted({min(209, A - 1), A - 1})
    for b in pres + [A + 1]:
        bh = block(b)["hash"]
        for ref, blk in (("num", h(b)), ("hash", {"blockHash": bh})):
            for gtag, g in (("21000", "0x5208"), ("30000", "0x7530"), ("100000", "0x186a0"), ("20M", "0x1312d00"), ("60M", "0x3938700"), ("none", None)):
                tx = {"from": S.F, "to": S.F2, "data": "0x01"}
                if g:
                    tx["gas"] = g
                cs.append(case(f"t10_preArsia_call_b{b}_{ref}_{gtag}", "eth_call", [tx, blk, {S.F: {"balance": S.ETH}}],
                               f"eth_call at block {b} ({ref}); gas {gtag}. Pre-Arsia: gas between intrinsic and intrinsic*tokenRatio"))
                cs.append(case(f"t10_preArsia_estimateGas_b{b}_{ref}_{gtag}", "eth_estimateGas", [tx, blk, {S.F: {"balance": S.ETH}}],
                               f"eth_estimateGas at block {b} ({ref}); gas {gtag}"))
    return cs


# --------------------------------------------------------- simulate: time/number/Arsia
def simulate_timeline_cases():
    cs = []
    base_n = int(TX["deploy_Scenarios"]["block"], 16)
    base = block(base_n)
    ts = int(base["timestamp"], 16)
    gb = {S.GB: {"code": S.RT["GasBurner"]}}
    binfo = {"from": S.F, "to": S.GB, "data": S.cd("blockInfo()")}

    def sim(name, base_block, blocks, desc, **opts):
        req = {"blockStateCalls": blocks}
        req.update(opts)
        cs.append(case(name, "eth_simulateV1", [req, h(base_block) if isinstance(base_block, int) else base_block], desc))

    blk = lambda calls, **bo: {**({"blockOverrides": bo} if bo else {}), "calls": calls}
    first = {"stateOverrides": gb, "calls": [binfo]}
    sim("t10_simulateV1_chain_default_step", base_n, [first, blk([binfo]), blk([binfo])],
        f"fixed base block {base_n}, default time step and block numbers")
    sim("t10_simulateV1_chain_explicit_time", base_n,
        [{**first, "blockOverrides": {"time": h(ts + 100)}}, blk([binfo], time=h(ts + 105)), blk([binfo])],
        "explicit `time` on blocks 1 and 2, default on block 3")
    sim("t10_simulateV1_chain_time_equal_base", base_n, [{**first, "blockOverrides": {"time": h(ts)}}],
        "time equal to the base block time: must be rejected")
    sim("t10_simulateV1_chain_time_decreasing", base_n,
        [{**first, "blockOverrides": {"time": h(ts + 50)}}, blk([binfo], time=h(ts + 40))], "time decreases between simulated blocks")
    sim("t10_simulateV1_chain_number_jump", base_n,
        [{**first, "blockOverrides": {"number": h(base_n + 5)}}, blk([binfo])],
        "`number` jumps by 5: four padding blocks, then a default-numbered block")
    sim("t10_simulateV1_chain_number_jump_twice", base_n,
        [{**first, "blockOverrides": {"number": h(base_n + 3)}}, blk([binfo], number=h(base_n + 10))], "two jumps: padding blocks in between")
    sim("t10_simulateV1_chain_number_jump_with_time", base_n,
        [{**first, "blockOverrides": {"number": h(base_n + 4), "time": h(ts + 200)}}, blk([binfo])],
        "number jump plus explicit time: padding blocks interpolate time")
    sim("t10_simulateV1_chain_number_equal_base", base_n, [{**first, "blockOverrides": {"number": h(base_n)}}], "number equal to base: rejected")
    sim("t10_simulateV1_chain_number_decreasing", base_n,
        [{**first, "blockOverrides": {"number": h(base_n + 5)}}, blk([binfo], number=h(base_n + 3))], "number decreases")
    sim("t10_simulateV1_chain_number_jump_big", base_n, [{**first, "blockOverrides": {"number": h(base_n + 400)}}],
        "jump of 400 blocks (padding-block count limit)")
    sim("t10_simulateV1_chain_number_jump_over_limit", base_n, [{**first, "blockOverrides": {"number": h(base_n + 1200)}}],
        "jump of 1200 blocks (beyond the 256-block-style limits)")
    sim("t10_simulateV1_chain_padding_then_state", base_n,
        [{"stateOverrides": gb, "calls": [{"from": S.F, "to": S.GB, "data": S.cd("store(uint256,uint256)", 1, 9)}]},
         blk([{"from": S.F, "to": S.GB, "data": S.cd("load(uint256)", 1)}], number=h(base_n + 6))],
        "write in block 1, read after 5 padding blocks")
    # Arsia boundary
    sim("t10_simulateV1_arsia_pre_only", A - 5, [{**first}, blk([binfo])], f"control: both simulated blocks below Arsia ({A})")
    t5 = int(block(A - 5)["timestamp"], 16)
    sim("t10_simulateV1_arsia_pre_only_explicit_time", A - 5, [{**first, "blockOverrides": {"time": h(t5 + 1)}}, blk([binfo], time=h(t5 + 2))],
        "control: two simulated blocks that stay below Arsia in both number and time (default 12s steps would cross it)")
    t1 = int(block(A - 1)["timestamp"], 16)
    sim("t10_simulateV1_arsia_base_last_pre_explicit_time", A - 1, [{**first, "blockOverrides": {"time": h(t1 + 1)}}],
        "base block A-1, one simulated block (number A) with time = base+1")
    sim("t10_simulateV1_arsia_cross_default", A - 2, [first, blk([binfo]), blk([binfo])],
        "default numbering from A-2 reaches the Arsia block in the second/third simulated block")
    sim("t10_simulateV1_arsia_cross_explicit_number", A - 2, [{**first, "blockOverrides": {"number": h(A - 1)}}, blk([binfo], number=h(A + 1))],
        "block 2 is explicitly above Arsia")
    sim("t10_simulateV1_arsia_cross_in_padding", A - 3, [{**first, "blockOverrides": {"number": h(A - 1)}}, blk([binfo], number=h(A + 3))],
        "the boundary is crossed only inside the padding blocks of block 2")
    sim("t10_simulateV1_arsia_cross_in_padding_first", A - 4, [{**first, "blockOverrides": {"number": h(A + 2)}}],
        "the very first simulated block jumps over the boundary (padding crosses it)")
    sim("t10_simulateV1_arsia_cross_by_time", A - 2, [{**first, "blockOverrides": {"time": h(int(block(A + 1)["timestamp"], 16) + 5)}}],
        "explicit time beyond the Arsia activation time while the number stays below")
    sim("t10_simulateV1_arsia_base_last_pre", A - 1, [first], "base block A-1, one simulated block (number == A)")
    sim("t10_simulateV1_arsia_base_last_pre_three", A - 1, [first, blk([binfo]), blk([binfo])], "base block A-1, three simulated blocks")
    sim("t10_simulateV1_arsia_base_arsia", A, [first, blk([binfo])], "base block A (control, post)")
    sim("t10_simulateV1_arsia_base_post", A + 1, [first, blk([binfo])], "base block A+1 (control, post)")
    sim("t10_simulateV1_arsia_cross_validation", A - 2, [first, blk([binfo]), blk([binfo])], "same with validation enabled", validation=True)
    # deployed contract selfdestruct (existing contract, no code override)
    nb = int(TX["sc_selfdestruct_self"]["block"], 16)
    st = {S.F: {"balance": S.ETH}}
    for tag, data, val in (("self", S.cd("selfDestructSelf()"), "0x0"), ("other", S.cd("selfDestructOther(address)", S.A3), "0x0"),
                           ("create_and_destroy", S.cd("createAndDestroy()"), "0x5"), ("pay", S.cd("pay(address)", S.A1), "0x7")):
        sim(f"t10_simulateV1_deployed_{tag}", nb, [{"stateOverrides": st, "calls": [{"from": S.F, "to": SCEN, "data": data, "value": val}]}],
            f"deployed Scenarios contract ({tag}), traceTransfers on", traceTransfers=True)
    sim("t10_simulateV1_deployed_self_twice", nb, [{"stateOverrides": st, "calls": [{"from": S.F, "to": SCEN, "data": S.cd("selfDestructSelf()")}] * 2}],
        "deployed contract self-destructs to itself twice in one block", traceTransfers=True)
    return cs


def deployed_misc_cases():
    cs = []
    k33 = "0x" + "00" * 33
    k33b = "0x00" + "00" * 31 + "01"
    for tag, k in (("k33", k33), ("k33_lead_zero_slot1", k33b), ("k64", "0x" + "ab" * 64), ("k32", "0x" + "00" * 32)):
        cs.append(case(f"t10_deployed_getStorageAt_{tag}", "eth_getStorageAt", [SCEN, k, "latest"],
                       f"alloy#4434: key '{tag}' on the deployed Scenarios contract (slot0 is set by sc_static_write path)"))
        cs.append(case(f"t10_deployed_getProof_{tag}", "eth_getProof", [SCEN, [k], "latest"], f"alloy#4434: getProof key '{tag}' on the deployed contract"))
        cs.append(case(f"t10_deployed_getProof_{tag}_historic", "eth_getProof", [SCEN, [k], TX["sc_sstore_clear"]["block"]],
                       f"alloy#4434: getProof key '{tag}' at the block of sc_sstore_clear"))
    al = {"from": S.F, "to": SCEN, "data": S.cd("precompiles()")}
    cs.append(case("t10_deployed_createAccessList_precompiles", "eth_createAccessList", [al, "latest"], "precompile calls on a deployed contract"))
    cs.append(case("t10_deployed_createAccessList_precompiles_to_in_list", "eth_createAccessList",
                   [{**al, "accessList": [{"address": SCEN, "storageKeys": ["0x" + "00" * 32]}]}, "latest"], "request list contains `to`"))
    cs.append(case("t10_deployed_createAccessList_writeSlot", "eth_createAccessList",
                   [{"from": S.F, "to": SCEN, "data": S.cd("writeSlot(uint256)", 5)}, "latest"], "SSTORE on a deployed contract"))
    cs.append(case("t10_deployed_createAccessList_writeSlot_with_list", "eth_createAccessList",
                   [{"from": S.F, "to": SCEN, "data": S.cd("writeSlot(uint256)", 5),
                     "accessList": [{"address": SCEN, "storageKeys": ["0x" + "00" * 32]}, {"address": FX["logEmitter"], "storageKeys": []}]}, "latest"],
                   "SSTORE with `to` and an unrelated address in the request list"))
    return cs


def main():
    write("t10_chain_trace_tx.json", trace_tx_cases())
    write("t10_chain_trace_block.json", trace_block_cases())
    write("t10_chain_logs_feehistory.json", logs_cases())
    fee = fee_update_cases()
    if fee:
        write("t10_chain_fee_update.json", fee)
    else:
        print("no token-ratio update block given: t10_chain_fee_update.json not generated")
    write("t10_chain_pre_arsia_gas.json", pre_arsia_cases())
    write("t10_chain_simulate_timeline.json", simulate_timeline_cases())
    write("t10_chain_deployed_misc.json", deployed_misc_cases())


main()
