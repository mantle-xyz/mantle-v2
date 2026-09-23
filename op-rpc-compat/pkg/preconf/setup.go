package preconf

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/rpc"
	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/tx"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

// oneMNT is 1e18 wei.
var oneMNT = new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)

// Standard dev/anvil private keys (accounts #1..#9); account #0 is the funder. Used only to top up
// the funder on a devnet where the funder's genesis balance has been drawn down.
var devTopUpKeys = []string{
	"0x59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d",
	"0x5de4111afa1a4b94908f83103eb1f1706367c2e68ca870fc3fb9a804cdab365a",
	"0x7c852118294e51e653712a81e05800f419141751be58f605c371e15141b007a6",
	"0x47e179ec197488593b187f80a00eb0da91f1b9d0b13f8733639f19c30a34926a",
	"0x8b3a350cf5c34c9194ca85829a2df0ec3153be0318b5e2d3348e872092edffba",
	"0x92db14e403b83dfe3df233f83dfa3a0d7096f21ca9b0d6d6b8d88b2b4ec1564e",
	"0x4bbbf85ce3377467afe5d46f804f221813b2bb87f24d81f60f1fcdbf7cbf4356",
	"0xdbda1821b80551c9d65939329250298aa3472ba22feea921c0cf5d620ea67b97",
	"0x2a871d0798f97d79848a013d4936a73bf4cc922c825d33c1cf7073dff6d409c6",
}

// ensureFunded tops up the funder from dev accounts if it is low, then funds Addr1.
func (r *Runner) ensureFunded(ctx context.Context) error {
	funderMin := new(big.Int).Mul(big.NewInt(50), oneMNT)    // keep funder >= 50 MNT
	funderTopUp := new(big.Int).Mul(big.NewInt(200), oneMNT) // target after top-up
	addr1Min := new(big.Int).Mul(big.NewInt(10), oneMNT)
	addr1Give := new(big.Int).Mul(big.NewInt(50), oneMNT)

	funder := r.funder.Address()
	bal, err := r.tester.GetBalance(ctx, r.seq, funder, "latest")
	if err != nil {
		return err
	}
	fmt.Printf("funder %s balance: %s MNT\n", funder.Hex(), weiToMNT(bal))

	for _, key := range devTopUpKeys {
		if bal.Cmp(funderTopUp) >= 0 {
			break
		}
		b, err := tx.NewBuilder(r.tester.ChainID(), key)
		if err != nil {
			continue
		}
		src := b.Address()
		srcBal, err := r.tester.GetBalance(ctx, r.seq, src, "latest")
		if err != nil || srcBal.Cmp(new(big.Int).Mul(big.NewInt(10), oneMNT)) < 0 {
			continue
		}
		send := new(big.Int).Sub(srcBal, new(big.Int).Mul(big.NewInt(5), oneMNT)) // leave ~5 for gas
		if send.Sign() <= 0 {
			continue
		}
		if err := r.sendNativeAndWait(ctx, b, funder, send); err != nil {
			fmt.Printf("  top-up from %s failed: %v\n", src.Hex(), err)
			continue
		}
		bal, _ = r.tester.GetBalance(ctx, r.seq, funder, "latest")
		fmt.Printf("  topped up funder to %s MNT (from %s)\n", weiToMNT(bal), src.Hex())
	}
	if bal.Cmp(funderMin) < 0 {
		return fmt.Errorf("funder balance %s MNT below minimum %s; no dev accounts left to top up", weiToMNT(bal), weiToMNT(funderMin))
	}

	// fund Addr1 (preconf sender for TestPay calls)
	addr1 := r.addr1.Address()
	a1, err := r.tester.GetBalance(ctx, r.seq, addr1, "latest")
	if err != nil {
		return err
	}
	if a1.Cmp(addr1Min) < 0 {
		if err := r.sendNativeAndWait(ctx, r.funder, addr1, addr1Give); err != nil {
			return fmt.Errorf("fund addr1: %w", err)
		}
		fmt.Printf("funded Addr1 %s with %s MNT\n", addr1.Hex(), weiToMNT(addr1Give))
	}
	return nil
}

// ensureContracts deploys TestERC20/TestPay to the sequencer if missing (requires funder nonce 0
// for the deterministic addresses); otherwise it's a no-op.
func (r *Runner) ensureContracts(ctx context.Context) error {
	erc20Present, err := r.hasCode(ctx, TestERC20Addr)
	if err != nil {
		return err
	}
	payPresent, err := r.hasCode(ctx, TestPayAddr)
	if err != nil {
		return err
	}
	if erc20Present && payPresent {
		fmt.Println("contracts already deployed (TestERC20 + TestPay)")
		return nil
	}

	n, err := r.nonce(ctx, r.funder.Address())
	if err != nil {
		return err
	}
	if n != 0 {
		return fmt.Errorf("contracts missing and funder nonce is %d (need 0 for deterministic addresses); redeploy on a fresh chain (task up-all) before running", n)
	}

	if err := r.deployAt(ctx, TestERC20Bytecode(), TestERC20Addr, "TestERC20"); err != nil {
		return err
	}
	if err := r.deployAt(ctx, TestPayBytecode(), TestPayAddr, "TestPay"); err != nil {
		return err
	}
	return nil
}

// deployAt sends a create tx (funder, next nonce) and verifies the contract landed at want.
func (r *Runner) deployAt(ctx context.Context, bytecodeHex string, want common.Address, name string) error {
	gp, err := r.gasPrice(ctx)
	if err != nil {
		return err
	}
	n, err := r.nonce(ctx, r.funder.Address())
	if err != nil {
		return err
	}
	signed, err := r.signedLegacy(r.funder, nil, big.NewInt(0), common.FromHex(bytecodeHex), 3_000_000, n, gp)
	if err != nil {
		return err
	}
	hash, err := r.tester.SendRawTransaction(ctx, r.seq, signed, name+" deploy")
	if err != nil {
		return fmt.Errorf("%s deploy send: %w", name, err)
	}
	rcpt, err := r.tester.WaitForReceipt(ctx, r.seq, hash, 30*time.Second)
	if err != nil {
		return fmt.Errorf("%s deploy receipt: %w", name, err)
	}
	if !common.IsHexAddress(rcpt.ContractAddress) || common.HexToAddress(rcpt.ContractAddress) != want {
		return fmt.Errorf("%s deployed at %s, expected %s (funder nonce not 0?)", name, rcpt.ContractAddress, want.Hex())
	}
	fmt.Printf("deployed %s at %s\n", name, want.Hex())
	return nil
}

// hasCode reports whether an address has non-empty code on the sequencer.
func (r *Runner) hasCode(ctx context.Context, addr common.Address) (bool, error) {
	resp := r.seq.Call(ctx, rpc.NewRequest("eth_getCode", []interface{}{addr.Hex(), "latest"}))
	if resp.Error != nil {
		return false, resp.Error
	}
	if resp.Response.Error != nil {
		return false, fmt.Errorf("eth_getCode: %s", resp.Response.Error.Message)
	}
	var code string
	if err := json.Unmarshal(resp.Response.Result, &code); err != nil {
		return false, err
	}
	return len(code) > 2, nil // more than "0x"
}

// sendNativeAndWait sends a native transfer and waits for its receipt.
func (r *Runner) sendNativeAndWait(ctx context.Context, from *tx.Builder, to common.Address, amount *big.Int) error {
	gp, err := r.gasPrice(ctx)
	if err != nil {
		return err
	}
	n, err := r.tester.GetNonce(ctx, r.seq, from.Address())
	if err != nil {
		return err
	}
	signed, err := r.signedLegacy(from, &to, amount, nil, 21000, n, gp)
	if err != nil {
		return err
	}
	hash, err := r.tester.SendRawTransaction(ctx, r.seq, signed, "fund")
	if err != nil {
		return err
	}
	_, err = r.tester.WaitForReceipt(ctx, r.seq, hash, 30*time.Second)
	return err
}

func weiToMNT(w *big.Int) string {
	if w == nil {
		return "0"
	}
	f := new(big.Float).Quo(new(big.Float).SetInt(w), new(big.Float).SetInt(oneMNT))
	return f.Text('f', 4)
}

// ensureERC20State makes Addr3 an ERC20 holder with a large TestPay allowance, so that
// TestPay.transferTo runs past the allowance check — a prerequisite for the "out of gas" and
// "underflow balance sender" reason cases. Idempotent: skips approve/mint when already sufficient.
func (r *Runner) ensureERC20State(ctx context.Context) error {
	addr3 := r.addr3.Address()

	// 1) fund Addr3 with gas (it sends the approve tx).
	a3, err := r.tester.GetBalance(ctx, r.seq, addr3, "latest")
	if err != nil {
		return err
	}
	if a3.Cmp(new(big.Int).Mul(big.NewInt(10), oneMNT)) < 0 {
		if err := r.sendNativeAndWait(ctx, r.funder, addr3, new(big.Int).Mul(big.NewInt(50), oneMNT)); err != nil {
			return fmt.Errorf("fund addr3: %w", err)
		}
		fmt.Printf("funded Addr3 %s with 50 MNT (gas)\n", addr3.Hex())
	}

	// 2) allowance[Addr3][TestPay] must be large. Approve hugeAmount if below 1e24.
	allowance, err := r.callERC20Uint(ctx, ERC20AllowanceCalldata(addr3, TestPayAddr))
	if err != nil {
		return fmt.Errorf("read allowance: %w", err)
	}
	allowanceFloor := new(big.Int).Exp(big.NewInt(10), big.NewInt(24), nil)
	if allowance.Cmp(allowanceFloor) < 0 {
		if err := r.sendContractTxAndWait(ctx, r.addr3, TestERC20Addr, ERC20ApproveCalldata(TestPayAddr, hugeAmount), 100_000); err != nil {
			return fmt.Errorf("addr3 approve TestPay: %w", err)
		}
		fmt.Printf("Addr3 approved TestPay (allowance set)\n")
	}

	// 3) Addr3 must hold >= 1 MNT-worth of TestERC20; mint 1e18 if below.
	bal, err := r.callERC20Uint(ctx, ERC20BalanceOfCalldata(addr3))
	if err != nil {
		return fmt.Errorf("read erc20 balance: %w", err)
	}
	if bal.Cmp(oneMNT) < 0 {
		if err := r.sendContractTxAndWait(ctx, r.funder, TestERC20Addr, ERC20MintCalldata(addr3, oneMNT), 100_000); err != nil {
			return fmt.Errorf("mint TestERC20 to addr3: %w", err)
		}
		fmt.Printf("minted 1 TestERC20 to Addr3\n")
	}
	return nil
}

// callERC20Uint does an eth_call to TestERC20 and parses the uint256 result.
func (r *Runner) callERC20Uint(ctx context.Context, data []byte) (*big.Int, error) {
	call := map[string]interface{}{"to": TestERC20Addr.Hex(), "data": hexutil.Encode(data)}
	resp := r.seq.Call(ctx, rpc.NewRequest("eth_call", []interface{}{call, "latest"}))
	if resp.Error != nil {
		return nil, resp.Error
	}
	if resp.Response.Error != nil {
		return nil, fmt.Errorf("eth_call: %s", resp.Response.Error.Message)
	}
	var hexStr string
	if err := json.Unmarshal(resp.Response.Result, &hexStr); err != nil {
		return nil, err
	}
	// ABI-encoded uint256 is a zero-padded 32-byte word; DecodeBig rejects leading zeros, so parse
	// the raw bytes.
	return new(big.Int).SetBytes(common.FromHex(hexStr)), nil
}

// sendContractTxAndWait signs a contract call from `from` and waits for its receipt.
func (r *Runner) sendContractTxAndWait(ctx context.Context, from *tx.Builder, to common.Address, data []byte, gas uint64) error {
	gp, err := r.gasPrice(ctx)
	if err != nil {
		return err
	}
	n, err := r.tester.GetNonce(ctx, r.seq, from.Address())
	if err != nil {
		return err
	}
	signed, err := r.signedLegacy(from, &to, big.NewInt(0), data, gas, n, gp)
	if err != nil {
		return err
	}
	hash, err := r.tester.SendRawTransaction(ctx, r.seq, signed, "erc20-setup")
	if err != nil {
		return err
	}
	_, err = r.tester.WaitForReceipt(ctx, r.seq, hash, 30*time.Second)
	return err
}
