#!/usr/bin/env python3
"""Turns prepare.sh output (tx.log, addr.env) into fixtures.json using an RPC endpoint.

usage: collect.py <prepare-out-dir> <rpc-url> <fixtures.json>
"""
import json
import sys
import urllib.request

out, rpc_url, dest = sys.argv[1:4]


def rpc(method, params):
    req = urllib.request.Request(
        rpc_url,
        json.dumps({"jsonrpc": "2.0", "id": 1, "method": method, "params": params}).encode(),
        {"content-type": "application/json"},
    )
    body = json.load(urllib.request.urlopen(req))
    if "error" in body:
        raise SystemExit(f"{method}: {body['error']}")
    return body["result"]


addr = dict(l.strip().split("=", 1) for l in open(out + "/addr.env") if "=" in l)
txs = {}
for line in open(out + "/tx.log"):
    parts = line.split()
    if len(parts) < 3 or not parts[1].startswith("0x"):
        continue
    name, h = parts[0], parts[1]
    rc = rpc("eth_getTransactionReceipt", [h])
    blk = rpc("eth_getBlockByNumber", [rc["blockNumber"], False])
    txs[name] = {
        "hash": h,
        "status": rc["status"],
        "block": rc["blockNumber"],
        "blockHash": rc["blockHash"],
        "blockTimestamp": blk["timestamp"],
        "gasUsed": rc["gasUsed"],
        "contractAddress": rc.get("contractAddress"),
        "logs": len(rc["logs"]),
    }
json.dump(
    {
        "chainId": rpc("eth_chainId", []),
        "logEmitter": addr["LOG_EMITTER"],
        "scenarios": addr["SCENARIOS"],
        "txs": txs,
    },
    open(dest, "w"),
    indent=1,
)
print(len(txs), "txs")
