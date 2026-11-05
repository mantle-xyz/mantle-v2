// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

// Fixture contracts for the T10 op-rpc-compat corpus.
// Compile: solc --evm-version cancun --optimize --bin --bin-runtime Fixtures.sol

contract LogEmitter {
    event Ping(uint256 indexed id, address indexed who, bytes data);
    event Plain(uint256 a, uint256 b);

    function ping(uint256 id, bytes calldata d) external {
        emit Ping(id, msg.sender, d);
    }

    function burst(uint256 start, uint256 n) external {
        for (uint256 i; i < n; i++) {
            emit Ping(start + i, msg.sender, abi.encodePacked(start + i));
            emit Plain(i, start);
        }
    }
}

contract Scenarios {
    uint256 public slot0;
    mapping(uint256 => uint256) public m;

    event Note(uint256 v);

    error CustomErr(uint256 code);

    receive() external payable {}

    function pay(address payable to) external payable {
        (bool ok,) = to.call{value: msg.value}("");
        require(ok, "pay failed");
    }

    function writeSlot(uint256 v) external {
        slot0 = v;
        m[v] = v + 1;
    }

    function doRevert() external pure {
        revert("scenario revert");
    }

    function doRevertCustom() external pure {
        revert CustomErr(7);
    }

    // Runs until the transaction gas limit is exhausted.
    function burnAll() external {
        uint256 i;
        while (true) {
            slot0 = ++i;
        }
    }

    function staticWrite() external {
        (bool ok,) = address(this).staticcall(abi.encodeCall(this.writeSlot, (1)));
        emit Note(ok ? 1 : 0);
    }

    function selfDestructSelf() external {
        selfdestruct(payable(address(this)));
    }

    function selfDestructOther(address payable to) external {
        selfdestruct(to);
    }

    // A contract created and destroyed inside this transaction.
    function createAndDestroy() external payable {
        address c;
        assembly {
            mstore(0, 0x30ff)
            c := create(callvalue(), 30, 2)
        }
        emit Note(uint160(c) == 0 ? 0 : 1);
    }

    function delegateWithValue(address impl) external payable {
        (bool ok,) = impl.delegatecall(abi.encodeCall(this.writeSlot, (msg.value)));
        require(ok, "delegate failed");
    }

    function precompiles() external view returns (bytes32 r) {
        bool ok;
        bytes memory out;
        (ok, out) = address(2).staticcall(abi.encodePacked("abc"));
        r ^= abi.decode(out, (bytes32));
        (ok, out) = address(4).staticcall(abi.encodePacked("identity"));
        r ^= keccak256(out);
        (ok, out) = address(1).staticcall(abi.encode(bytes32(uint256(1)), uint256(27), uint256(1), uint256(1)));
        r ^= keccak256(out);
    }

    function sstoreClear() external {
        slot0 = 5;
        slot0 = 0;
    }

    function logThenRevert() external {
        emit Note(42);
        revert("log then revert");
    }

    function createOk() external returns (address c) {
        // runtime: STOP
        bytes memory code = hex"600160005360016000f3";
        assembly {
            c := create(0, add(code, 32), mload(code))
        }
    }

    // Initcode reverts with data.
    function createFail() external returns (address c) {
        bytes memory code = hex"60aa60005360016000fd";
        assembly {
            c := create(0, add(code, 32), mload(code))
        }
        emit Note(uint160(c) == 0 ? 0 : 1);
    }

    function create2Fail(bytes32 salt) external returns (address c) {
        bytes memory code = hex"60aa60005360016000fd";
        assembly {
            c := create2(0, add(code, 32), mload(code), salt)
        }
        emit Note(uint160(c) == 0 ? 0 : 1);
    }

    function deep(uint256 n) external {
        if (n == 0) return;
        (bool ok,) = address(this).call(abi.encodeCall(this.deep, (n - 1)));
        if (!ok) emit Note(n);
    }

    // Deploys `runtime` as a fresh contract and calls it with `cd`.
    // The result is returned instead of reverting, so odd opcodes can be traced.
    function runCode(bytes calldata runtime, bytes calldata cd) external payable returns (bool ok, bytes memory ret) {
        bytes memory init = abi.encodePacked(hex"61", uint16(runtime.length), hex"80600c6000396000f3", runtime);
        address c;
        assembly {
            c := create(0, add(init, 32), mload(init))
        }
        require(c != address(0), "deploy failed");
        (ok, ret) = c.call{value: msg.value}(cd);
    }
}

// Reads many addresses and storage slots: used as a state-override target.
contract AccessProbe {
    function touch() external returns (uint256 r) {
        r += address(0x1111111111111111111111111111111111111111).balance;
        r += address(0x2222222222222222222222222222222222222222).code.length;
        bytes32 h;
        address a = 0x3333333333333333333333333333333333333333;
        assembly {
            h := extcodehash(a)
            r := add(r, sload(1))
            r := add(r, sload(2))
            r := add(r, sload(3))
            r := add(r, sload(0x100))
        }
        (bool ok,) = address(0x4444444444444444444444444444444444444444).staticcall("");
        if (ok) r += 1;
        (ok,) = address(0x5555555555555555555555555555555555555555).call("");
        if (ok) r += 1;
        r += uint256(h) & 1;
        // a write, so that the list also records written slots
        assembly {
            sstore(7, add(r, 1))
        }
    }

    // Reads `to` (self) storage again and calls a precompile: exercises the access-list exclusion rules.
    function touchSelfAndPrecompile() external view returns (bytes32 r) {
        bool ok;
        bytes memory out;
        (ok, out) = address(2).staticcall(abi.encodePacked("x"));
        r = abi.decode(out, (bytes32));
        assembly {
            r := xor(r, sload(9))
        }
        r ^= bytes32(address(this).balance);
    }
}

// Consumes a controllable amount of gas.
contract GasBurner {
    function burn(uint256 n) external pure returns (bytes32 h) {
        // scratch-space hashing only: gas grows linearly with n
        for (uint256 i; i < n; i++) {
            assembly {
                mstore(0, h)
                mstore(32, i)
                h := keccak256(0, 64)
            }
        }
    }

    function store(uint256 k, uint256 v) external {
        assembly {
            sstore(k, v)
        }
    }

    function load(uint256 k) external view returns (uint256 v) {
        assembly {
            v := sload(k)
        }
    }

    function blockInfo() external view returns (uint256, uint256, uint256, uint256) {
        return (block.number, block.timestamp, block.gaslimit, block.basefee);
    }
}
